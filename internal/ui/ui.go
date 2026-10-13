// Package ui is the interactive interface: a sessions panel on the left, the selected session
// and its problems on the right, and full-screen views for raw lines, the last minutes, the
// report and help. Arrows do everything: up/down move, left/right change panel.
package ui

import (
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/vmaltarello/whyd/internal/analyze"
	"github.com/vmaltarello/whyd/internal/journal"
	"github.com/vmaltarello/whyd/internal/report"
)

// Recent is how far back the sessions list goes at first; each "older" step adds as much again.
const Recent = 30 * 24 * time.Hour

// Source lets the interface analyse other sessions.
type Source struct {
	Boots     []journal.Boot // oldest first; empty when reading a file
	Analyse   func(index int) (*analyze.Result, error)
	Summarize func(index int) (analyze.Summary, error) // usually from the cache
	Skipped   int                                      // user rules that couldn't be loaded
	Since     time.Time                                // sessions that ended before this are loaded on request; zero: Recent
}

type view int

const (
	vMain view = iota
	vLines
	vTail
	vReport
	vHelp
	vUnknown
)

const (
	focusSessions = 0
	focusProblems = 1
)

type bootState struct {
	res *analyze.Result
	err error
}

type sumState struct {
	sum analyze.Summary
	err error
}

type bootMsg struct {
	index int
	res   *analyze.Result // nil when the session was no longer selected (skipped)
	err   error
}

type sumMsg struct {
	index int
	sum   analyze.Summary
	err   error
}

// keepResults is how many full analyses stay in memory, so going back and forth is instant.
const keepResults = 6

type statusMsg string

type model struct {
	src       Source
	w, h      int
	summaries map[int]sumState  // every listed session: how it ended, for the list
	results   map[int]bootState // full analyses, the selected session and a few recent ones
	used      []int             // results, least recently selected first
	order     []int             // session indexes shown, newest first: those since `since`
	since     time.Time         // sessions that ended before this are not loaded yet
	older     int               // how many sessions that leaves out
	reading   map[int]bool      // full analyses running, by session index
	want      *atomic.Int64     // the selected session: analyses of others are skipped

	selS, offS int // sessions panel
	onlyBad    bool
	focus      int
	selP, offP int // problems panel

	view       view
	shown      []item // the items of the list view on screen, after the search filter
	tailKey    string // what tailCache was built for
	tailCache  []item
	openHidden map[int]bool // folded blocks of hidden lines opened in the tail, by block number
	off        int          // scroll of the full-screen view (first line shown)
	sel        int          // selected item in a list view
	search     string
	searching  bool
	window     time.Duration
	showHidden bool
	redacted   bool
	status     string
}

func newModel(res *analyze.Result, src Source) *model {
	m := &model{
		src: src, results: map[int]bootState{res.Boot.Index: {res: res}},
		summaries: map[int]sumState{res.Boot.Index: {sum: res.Summarize()}}, used: []int{res.Boot.Index},
		reading: map[int]bool{}, want: new(atomic.Int64), window: analyze.TailWindow, redacted: true,
	}
	m.want.Store(int64(res.Boot.Index))
	m.since = src.Since
	if m.since.IsZero() {
		m.since = report.Now().Add(-Recent)
	}
	if res.Boot.Last.Before(m.since) { // opened on an old session (whyd -b -300): start from there
		m.since = res.Boot.First
	}
	m.loadOrder()
	if len(m.order) == 0 {
		m.order = []int{res.Boot.Index}
	}
	for i, idx := range m.order {
		if idx == res.Boot.Index {
			m.selS = i
		}
	}
	if src.Skipped > 0 {
		m.status = fmt.Sprintf("%d of your rules skipped: run whyd rules", src.Skipped)
	}
	return m
}

// loadOrder lists the sessions that ended since m.since, newest first, and counts the others.
func (m *model) loadOrder() {
	m.order, m.older = nil, 0
	for _, b := range slices.Backward(m.src.Boots) {
		if b.Last.Before(m.since) {
			m.older++
		} else {
			m.order = append(m.order, b.Index)
		}
	}
}

// loadOlder adds the sessions of the next Recent period back and analyses them.
func (m *model) loadOlder() tea.Cmd {
	m.since = m.since.Add(-Recent)
	m.loadOrder()
	return m.Init()
}

// Run opens the interface on a result and blocks until the user quits.
func Run(res *analyze.Result, src Source) error {
	_, err := tea.NewProgram(newModel(res, src)).Run()
	return err
}

// a few analyses at a time: each one runs journalctl. The selected session has its own slots,
// so it doesn't wait behind the summaries of the whole list.
var (
	analyseSlots = make(chan struct{}, 4)
	openSlots    = make(chan struct{}, 2)
)

// Init gets the summary of every listed session (from the cache when it can) for the list.
func (m *model) Init() tea.Cmd {
	if m.src.Summarize == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, idx := range m.order {
		if _, done := m.summaries[idx]; done {
			continue
		}
		cmds = append(cmds, func() tea.Msg {
			analyseSlots <- struct{}{}
			defer func() { <-analyseSlots }()
			sum, err := m.src.Summarize(idx)
			return sumMsg{idx, sum, err}
		})
	}
	return tea.Batch(cmds...)
}

// selected is the index of the selected session, false when there is none.
func (m *model) selected() (int, bool) {
	vis := m.visible()
	if len(vis) == 0 {
		return 0, false
	}
	return vis[min(m.selS, len(vis)-1)], true
}

// full analyses the selected session when its analysis isn't in memory. Moving fast through the
// list starts one per session, but those no longer selected when their turn comes are skipped.
func (m *model) full() tea.Cmd {
	idx, ok := m.selected()
	if !ok {
		return nil
	}
	m.want.Store(int64(idx))
	if _, done := m.results[idx]; done {
		m.touch(idx)
		return nil
	}
	if m.src.Analyse == nil || m.reading[idx] {
		return nil
	}
	m.reading[idx] = true
	want := m.want
	return func() tea.Msg {
		openSlots <- struct{}{}
		defer func() { <-openSlots }()
		if want.Load() != int64(idx) {
			return bootMsg{index: idx}
		}
		res, err := m.src.Analyse(idx)
		return bootMsg{idx, res, err}
	}
}

// touch marks a result as just used and forgets the oldest ones beyond keepResults.
func (m *model) touch(idx int) {
	m.used = append(slices.DeleteFunc(m.used, func(i int) bool { return i == idx }), idx)
	for len(m.used) > keepResults {
		delete(m.results, m.used[0])
		m.used = m.used[1:]
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.checkView()
	return m, cmd
}

func (m *model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case sumMsg:
		// with U, a session that turns out bad joins the list: keep the same one selected
		idx, ok := m.selected()
		m.summaries[msg.index] = sumState{msg.sum, msg.err}
		if ok && !m.onOlder() {
			if i := slices.Index(m.visible(), idx); i >= 0 {
				m.selS = i
			}
		}
	case bootMsg:
		delete(m.reading, msg.index)
		if msg.res != nil || msg.err != nil {
			m.results[msg.index] = bootState{msg.res, msg.err}
			if msg.res != nil {
				m.summaries[msg.index] = sumState{sum: msg.res.Summarize()}
			}
			m.touch(msg.index)
		}
		return m.full() // the selection may have moved meanwhile
	case statusMsg:
		m.status = string(msg)
	case tea.KeyPressMsg:
		return m.key(msg.String())
	}
	return nil
}

// checkView goes back to the main screen when the open view has nothing to show anymore: the
// selected session isn't read yet, or has no problem to list the lines of.
func (m *model) checkView() {
	if m.view == vMain || m.view == vHelp {
		return
	}
	res, _ := m.cur()
	if res == nil || m.view == vLines && len(res.Findings) == 0 || m.view == vUnknown && len(res.Unknown) == 0 {
		m.view = vMain
	}
}

// visible is the list of sessions shown, newest first.
func (m *model) visible() []int {
	if !m.onlyBad {
		return m.order
	}
	var out []int
	for _, idx := range m.order {
		if st, ok := m.summaries[idx]; ok && st.err == nil && st.sum.Bad() {
			out = append(out, idx)
		}
	}
	return out
}

// cur is the selected session's analysis (nil while it is being read).
func (m *model) cur() (*analyze.Result, error) {
	idx, ok := m.selected()
	if !ok {
		return nil, nil
	}
	st := m.results[idx]
	return st.res, st.err
}

func (m *model) key(k string) tea.Cmd {
	m.status = ""
	if k == "ctrl+c" {
		return tea.Quit
	}
	if m.view != vMain {
		return m.viewKey(k)
	}
	res, _ := m.cur()
	switch k {
	case "q", "Q":
		return tea.Quit
	case "?":
		m.view = vHelp
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-10)
	case "pgdown":
		m.move(10)
	case "home", "g":
		m.move(-1 << 20)
	case "end", "G":
		m.move(1 << 20)
	case "right":
		switch {
		case m.focus != focusSessions:
		case res != nil && len(res.Findings) > 0:
			m.focus = focusProblems
		case res != nil:
			m.status = "no problems in this session"
		}
	case "left", "esc":
		m.focus = focusSessions
	case "enter":
		switch {
		case m.onOlder():
			return m.loadOlder()
		case m.focus == focusSessions && res != nil && len(res.Findings) > 0:
			m.focus = focusProblems
		case m.focus == focusProblems && m.ready(res):
			m.openList(vLines, false)
		}
	case "l", "L":
		if m.ready(res) {
			m.openList(vTail, true)
		}
	case "r", "R":
		if m.ready(res) {
			m.view, m.off = vReport, 0
		}
	case "w", "W":
		if res != nil && len(res.Unknown) > 0 {
			m.openList(vUnknown, false)
		}
	case "u", "U":
		m.onlyBad = !m.onlyBad
		m.selS, m.offS, m.selP, m.offP, m.focus = 0, 0, 0, 0, focusSessions
	}
	return m.full()
}

// onOlder tells whether the "older sessions" row is selected.
func (m *model) onOlder() bool {
	return m.focus == focusSessions && m.older > 0 && m.selS >= len(m.visible())
}

// ready tells whether the views with journal lines can open on this session, and says so when
// it is still being read.
func (m *model) ready(res *analyze.Result) bool {
	if res == nil {
		m.status = "still reading this session"
	}
	return res != nil
}

// move changes the selection of the focused panel, or scrolls the open view.
func (m *model) move(d int) {
	switch {
	case isList(m.view):
		m.sel = max(0, m.sel+d)
		return
	case m.view != vMain:
		m.off = max(0, m.off+d)
		return
	}
	if m.focus == focusSessions {
		n := len(m.visible())
		if m.older > 0 {
			n++ // the "older sessions" row at the end
		}
		old := m.selS
		m.selS = max(0, min(n-1, m.selS+d))
		if m.selS != old {
			m.selP, m.offP = 0, 0
		}
		return
	}
	if res, _ := m.cur(); res != nil {
		m.selP = max(0, min(len(res.Findings)-1, m.selP+d))
	}
}

func (m *model) View() tea.View {
	content := "whyd: reading the journal…"
	if m.w > 0 && m.h > 0 && (m.w < minW || m.h < minH) {
		content = m.tooSmall()
	} else if m.w > 0 && m.h > 0 {
		switch m.view {
		case vLines:
			content = m.linesView()
		case vTail:
			content = m.tailView()
		case vReport:
			content = m.reportView()
		case vHelp:
			content = m.helpView()
		case vUnknown:
			content = m.unknownView()
		default:
			content = m.mainView()
		}
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "whyd"
	return v
}
