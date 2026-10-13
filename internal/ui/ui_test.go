package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/vmaltarello/whyd/internal/analyze"
	"github.com/vmaltarello/whyd/internal/journal"
	"github.com/vmaltarello/whyd/internal/report"
	"github.com/vmaltarello/whyd/internal/rules"
)

func load(t *testing.T, name string, index int) *analyze.Result {
	t.Helper()
	set, err := rules.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	res, err := analyze.Run(analyze.Input{Rules: set, Read: func(fn func(journal.Entry) error) error {
		return journal.ReadFile("../../testdata/boot-"+name+".jsonl.gz", fn)
	}})
	if err != nil {
		t.Fatal(err)
	}
	res.Boot.Index, res.Boot.ID = index, name
	return res
}

// a model over the recorded journals, as if they were the last four sessions
func recorded(t *testing.T) *model {
	names := []string{"unclean-quiet", "clean-reboot-suspend", "clean-poweroff", "unclean-oom"}
	var boots []journal.Boot
	results := map[int]*analyze.Result{}
	for i, name := range names {
		r := load(t, name, i-len(names))
		results[r.Boot.Index] = r
		boots = append(boots, r.Boot)
	}
	m := newModel(results[-1], Source{Boots: boots})
	for idx, r := range results {
		m.results[idx] = bootState{res: r}
		m.summaries[idx] = sumState{sum: r.Summarize()}
		m.touch(idx)
	}
	m.touch(-1) // the selected one
	return m
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		m.key(k)
	}
}

// Every screen renders inside the terminal, at the comfortable and at the minimum size.
func TestScreensFit(t *testing.T) {
	report.Now = func() time.Time { return time.Date(2026, 10, 9, 18, 30, 0, 0, time.Local) }
	defer func() { report.Now = time.Now }()
	report.Version = "0.1"

	for _, size := range [][2]int{{100, 32}, {100, 28}} {
		m := recorded(t)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		steps := []struct {
			name string
			keys []string
		}{
			{"main", nil},
			{"problems", []string{"right", "down"}},
			{"lines", []string{"enter"}},
			{"tail", []string{"esc", "l"}},
			{"report", []string{"esc", "r"}},
			{"help", []string{"esc", "?"}},
			{"unknown", []string{"x", "w"}},
			{"search", []string{"/", "q", "m", "l", "enter", "down"}},
			{"other", []string{"esc", "left", "down", "down"}},
		}
		for _, st := range steps {
			press(m, st.keys...)
			out := m.View().Content
			lines := strings.Split(out, "\n")
			if len(lines) != size[1] {
				t.Errorf("%dx%d %s: %d lines", size[0], size[1], st.name, len(lines))
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w > size[0] {
					t.Errorf("%dx%d %s line %d: width %d: %q", size[0], size[1], st.name, i, w, ansi.Strip(l))
				}
			}
			if strings.Contains(ansi.Strip(out), "—") {
				t.Errorf("%s: em dash on screen", st.name)
			}
			if dir := os.Getenv("WHYD_DUMP"); dir != "" { // WHYD_DUMP=dir writes every screen there
				base := filepath.Join(dir, fmt.Sprintf("whyd-%s-%dx%d", st.name, size[0], size[1]))
				if err := os.WriteFile(base+".txt", []byte(ansi.Strip(out)), 0o644); err != nil {
					t.Error(err)
				}
				if err := os.WriteFile(base+".ansi", []byte(out), 0o644); err != nil {
					t.Error(err)
				}
			}
		}
	}
}

func TestArrowsOnly(t *testing.T) {
	m := recorded(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	press(m, "down")
	if res, _ := m.cur(); res.Boot.Index != -2 {
		t.Errorf("down should select the older session, got %d", res.Boot.Index)
	}
	press(m, "up", "right")
	if m.focus != focusProblems {
		t.Error("right should move to the problems panel")
	}
	press(m, "down", "left")
	if m.focus != focusSessions || m.selP != 1 {
		t.Errorf("left should go back keeping the problem: focus %d, problem %d", m.focus, m.selP)
	}
}

// The selected line of a list opens to its whole message; the others stay on one line.
func TestExpandAndSearch(t *testing.T) {
	m := recorded(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	press(m, "w")
	res, _ := m.cur()
	long := -1
	for i, g := range res.Unknown {
		if len(g.Message) > 120 {
			long = i
			break
		}
	}
	if long < 0 {
		t.Skip("no long message in the recorded journal")
	}
	m.sel = long
	out := ansi.Strip(m.View().Content)
	words := strings.Fields(res.Unknown[long].Message)
	if last := words[len(words)-1]; !strings.Contains(out, last) {
		t.Errorf("the selected long message is not shown in full (missing %q)", last)
	}
	press(m, "/")
	for _, r := range "qmllint" {
		press(m, string(r))
	}
	press(m, "enter")
	out = ansi.Strip(m.View().Content)
	if !strings.Contains(out, "/qmllint") {
		t.Error("search not shown in the title")
	}
	for _, l := range strings.Split(out, "\n")[2:21] {
		if strings.Contains(l, "warn") && !strings.Contains(l, "qmllint") {
			t.Errorf("a line that doesn't match the search is shown: %q", l)
		}
	}
	press(m, "esc")
	if m.search != "" || m.view != vUnknown {
		t.Error("first esc should clear the search and stay")
	}
	press(m, "esc")
	if m.view != vMain {
		t.Error("second esc should go back")
	}
}

func TestTooSmall(t *testing.T) {
	m := recorded(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "The terminal is 80×24.") || !strings.Contains(out, "at least 100×28") {
		t.Errorf("no size message:\n%s", out)
	}
	if cmd := m.key("q"); cmd == nil {
		t.Error("q should still quit")
	}
}

func TestReviewDetails(t *testing.T) {
	m := recorded(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})

	// a long problem title is readable in full once selected
	press(m, "right")
	res, _ := m.cur()
	for i, ev := range res.Findings {
		if len(ev.Title) > 45 {
			m.selP = i
			out := ansi.Strip(m.View().Content)
			last := strings.Fields(ev.Title)
			if !strings.Contains(out, last[len(last)-1]) {
				t.Errorf("title %q not shown in full", ev.Title)
			}
			break
		}
	}

	// a folded block of hidden lines opens and closes with Enter
	press(m, "l", "home")
	m.View()
	for i, it := range m.shown {
		if it.block > 0 {
			m.sel = i
			break
		}
	}
	before := len(m.shown)
	press(m, "enter")
	m.View()
	if len(m.shown) <= before {
		t.Error("Enter on a block of hidden lines should show its lines")
	}
	press(m, "enter")
	m.View()
	if len(m.shown) != before {
		t.Error("a second Enter should fold the block again")
	}
}

// Selecting a session analyses it when its analysis isn't in memory; an analysis whose session
// is no longer selected is skipped; only keepResults analyses stay in memory.
func TestSessionLoading(t *testing.T) {
	m := recorded(t)
	full := map[int]*analyze.Result{}
	for idx, st := range m.results {
		full[idx] = st.res
	}
	runs := 0
	m.src.Analyse = func(idx int) (*analyze.Result, error) { runs++; return full[idx], nil }
	delete(m.results, -2)
	cmd := m.key("down") // to session -2, not in memory
	if res, _ := m.cur(); res != nil || cmd == nil {
		t.Fatal("session -2 should be read on selection")
	}
	if m.key("l") != nil || m.view != vMain || m.status != "still reading this session" {
		t.Fatalf("L while reading: view %v, status %q", m.view, m.status)
	}
	m.Update(cmd())
	if res, _ := m.cur(); res == nil || runs != 1 {
		t.Fatalf("session -2 not loaded (%d runs)", runs)
	}

	delete(m.results, -3)
	stale := m.key("down") // to -3, then away before its analysis runs
	m.key("up")
	m.Update(stale())
	if runs != 1 {
		t.Error("an analysis ran for a session no longer selected")
	}

	for i := range keepResults + 3 {
		m.results[100+i] = bootState{}
		m.touch(100 + i)
	}
	if len(m.results) != keepResults {
		t.Errorf("%d analyses in memory, want %d", len(m.results), keepResults)
	}
}

// Only the recent sessions are listed at first; the "older" row loads the period before.
func TestOlderSessions(t *testing.T) {
	report.Now = func() time.Time { return time.Date(2026, 10, 9, 18, 30, 0, 0, time.Local) }
	defer func() { report.Now = time.Now }()
	r := recorded(t)
	src := r.src
	src.Since = time.Date(2026, 10, 6, 0, 0, 0, 0, time.Local) // leaves out Saturday's session
	m := newModel(r.results[-1].res, src)
	m.results, m.summaries = r.results, r.summaries
	if len(m.order) != 3 || m.older != 1 {
		t.Fatalf("order %v, older %d; want 3 sessions and 1 older", m.order, m.older)
	}
	press(m, "down", "down", "down", "down") // past the last session: the "older" row
	if !m.onOlder() {
		t.Fatal("the older row can't be selected")
	}
	m.key("enter")
	if len(m.order) != 4 || m.older != 0 || m.onOlder() {
		t.Errorf("after loading: order %v, older %d", m.order, m.older)
	}
}

// With U, sessions found bad while the list loads join it: the selection must stay on the same
// session, and a view of a session that is no longer selected must close instead of crashing.
func TestOnlyBadWhileLoading(t *testing.T) {
	m := recorded(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	delete(m.summaries, -1) // the newest bad session hasn't been summarised yet
	press(m, "u")
	if idx, _ := m.selected(); idx != -4 {
		t.Fatalf("selected %d, want -4", idx)
	}
	m.Update(sumMsg{index: -1, sum: m.results[-1].res.Summarize()})
	if idx, _ := m.selected(); idx != -4 {
		t.Errorf("selection moved to %d", idx)
	}

	m.view = vLines
	delete(m.results, -4)
	m.Update(statusMsg(""))
	if m.view != vMain {
		t.Errorf("view %d still open on a session not read", m.view)
	}
	_ = m.View()
}

func TestSaveNeverOverwrites(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, want := range []string{"whyd-x.md", "whyd-x-2.md"} {
		if name, err := saveNew("whyd-x", ".md", want); err != nil || name != want {
			t.Errorf("saved %q, %v; want %q", name, err, want)
		}
	}
	if b, _ := os.ReadFile("whyd-x.md"); string(b) != "whyd-x.md" {
		t.Errorf("first file overwritten: %q", b)
	}
}
