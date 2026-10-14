package ui

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/vmaltarello/whyd/internal/analyze"
	"github.com/vmaltarello/whyd/internal/journal"
	"github.com/vmaltarello/whyd/internal/report"
	"github.com/vmaltarello/whyd/internal/rules"
)

// keys of the full-screen views
func (m *model) viewKey(k string) tea.Cmd {
	if m.view == vHelp {
		m.view = vMain
		return nil
	}
	if isList(m.view) {
		if m.view == vTail && !m.searching && m.tailKeys(k) {
			return nil
		}
		if m.view == vLines && !m.searching && k == "enter" {
			if m.sel < len(m.shown) && m.shown[m.sel].block > 0 {
				b := m.shown[m.sel].block
				m.openHidden[b] = !m.openHidden[b]
			}
			return nil
		}
		if m.listKey(k) {
			return nil
		}
		if k == "left" || k == "q" || k == "Q" {
			m.view = vMain
		}
		return nil
	}
	switch k {
	case "esc", "left", "q", "Q", "backspace":
		m.view = vMain
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-(m.h - 4))
	case "pgdown", "space":
		m.move(m.h - 4)
	case "home", "g":
		m.off = 0
	case "end", "G":
		m.off = 1 << 20
	case "x", "X":
		m.redacted = !m.redacted
	case "y", "Y":
		return copyCmd(m.reportText())
	case "s", "S":
		res, _ := m.cur()
		text, base := m.reportText(), "whyd-"+res.Boot.First.Format("2006-01-02-1504")
		return func() tea.Msg {
			name, err := saveNew(base, ".md", text)
			if err != nil {
				return statusMsg("can't save: " + err.Error())
			}
			return statusMsg("saved " + name)
		}
	}
	return nil
}

// saveNew writes text to base+ext in the current directory, or base-2+ext and so on when that
// file exists: a report saved earlier is never overwritten.
func saveNew(base, ext, text string) (string, error) {
	for i := 1; ; i++ {
		name := base + ext
		if i > 1 {
			name = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.WriteString(text); err != nil {
			_ = f.Close()
			return "", err
		}
		return name, f.Close()
	}
}

func (m *model) tailKeys(k string) bool {
	windows := []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}
	i := 0
	for i < len(windows)-1 && windows[i] < m.window {
		i++
	}
	switch k {
	case "enter":
		if m.sel < len(m.shown) && m.shown[m.sel].block > 0 {
			b := m.shown[m.sel].block
			m.openHidden[b] = !m.openHidden[b]
		}
	case "+", "=":
		m.openHidden = map[int]bool{}
		m.window, m.sel = windows[min(i+1, len(windows)-1)], 1<<20
	case "-":
		m.openHidden = map[int]bool{}
		m.window, m.sel = windows[max(i-1, 0)], 1<<20
	case "n", "N":
		m.showHidden = !m.showHidden
	default:
		return false
	}
	return true
}

// fullScreen frames lines over the whole screen, scrolled by m.off, with a key bar.
func (m *model) fullScreen(title string, lines []string, keys [][2]string) string {
	h := m.h - 2
	m.off = min(m.off, max(0, len(lines)-(h-2)))
	out := []string{m.header()}
	out = append(out, frame(window(lines, m.off, h-2), m.w, h, title, true)...)
	return strings.Join(append(out, m.keyBar(keys)), "\n")
}

// ---- raw lines of the selected problem ----

func (m *model) linesView() string {
	res, _ := m.cur()
	ev := m.selectedProblem(res)
	title := stPlain.Render(ev.Title) + "  " + stDim.Render(report.Plural(len(ev.Entries), "journal line"))
	return m.listView(title, "", "", m.problemLines(ev), [][2]string{{"enter", "open"}, {"esc", "back"}})
}

// problemLines are the journal lines of a problem. An occurrence over many lines (a kernel
// report, a stack trace) shows its first and last line, what the rule recognised, and folds the
// lines between: Enter opens them.
func (m *model) problemLines(ev *rules.Event) []item {
	line := func(e journal.Entry) item {
		head := stDim.Render(e.Time.Format("15:04:05")) + "  " + stCyan.Render(pad(e.Source, 12)) + " "
		return item{head + e.Message, e.Source + " " + e.Message, 24, 0}
	}
	var items []item
	for i, start := range ev.Starts {
		end := len(ev.Entries)
		if i+1 < len(ev.Starts) {
			end = ev.Starts[i+1]
		}
		occ := ev.Entries[start:end]
		if len(occ) <= 3 {
			for _, e := range occ {
				items = append(items, line(e))
			}
			continue
		}
		items = append(items, line(occ[0]))
		open := m.openHidden[i+1]
		sign := "▸"
		if open {
			sign = "▾"
		}
		t := fmt.Sprintf("%22s%s %s", "", sign, report.Plural(len(occ)-2, "more line")+" of this one")
		items = append(items, item{stGray.Render(t), t, 0, i + 1})
		if open {
			for _, e := range occ[1 : len(occ)-1] {
				items = append(items, line(e))
			}
		}
		items = append(items, line(occ[len(occ)-1]))
	}
	return items
}

// ---- the last minutes ----

func (m *model) tailView() string {
	res, _ := m.cur()
	title := stPlain.Render("Last "+analyze.Duration(m.window)) + "  " + stDim.Render("before "+report.When(res.Boot.Last))
	hidden := "show harmless"
	if m.showHidden {
		hidden = "fold harmless"
	}
	return m.listView(title, "", "", m.tailItems(res), [][2]string{{"enter", "open"}, {"+ -", "window"}, {"N", hidden}, {"esc", "back"}})
}

// tailItems builds the lines of the tail view, once for each session, window and folding: the
// last hour can hold thousands of lines, too many to style again at every key.
func (m *model) tailItems(res *analyze.Result) []item {
	key := fmt.Sprintf("%p %v %v %v", res, m.window, m.showHidden, m.openHidden)
	if key == m.tailKey {
		return m.tailCache
	}
	var items []item
	var hiddenLines []analyze.TailGroup
	block := 0
	flush := func() {
		if len(hiddenLines) == 0 {
			return
		}
		block++
		n := 0
		for _, g := range hiddenLines {
			n += g.Count
		}
		open := m.openHidden[block]
		t := fmt.Sprintf("%13s▸ %s", "", report.Plural(n, "harmless line"))
		if open {
			t = fmt.Sprintf("%13s▾ %s", "", report.Plural(n, "harmless line"))
		}
		items = append(items, item{stGray.Render(t), t, 0, block})
		if open {
			for _, g := range hiddenLines {
				items = append(items, m.tailItem(g, stGray))
			}
		}
		hiddenLines = nil
	}
	for _, g := range analyze.Collapse(res.TailSince(m.window)) {
		if g.Hidden && !m.showHidden {
			hiddenLines = append(hiddenLines, g)
			continue
		}
		flush()
		st := stPlain
		if g.Hidden {
			st = stGray
		}
		items = append(items, m.tailItem(g, st))
	}
	flush()
	end := "the journal stops at " + res.Boot.Last.Format("15:04:05")
	if res.Verdict.Kind.Clean() {
		end = res.Verdict.Summary
	}
	if q := report.QuietLine(res); q != "" {
		end += " · " + q
	}
	if g := res.Gap(); g > 0 {
		end += " · next boot " + analyze.Duration(g) + " later"
	}
	endSt := endStyle(res.Verdict.Kind)
	items = append(items, item{endSt.Render("── " + end), end, 3, 0})
	m.tailKey, m.tailCache = key, items
	return items
}

// tailItem is one (collapsed) line of the tail: mark, time, source, message.
func (m *model) tailItem(g analyze.TailGroup, st lipgloss.Style) item {
	mark := " "
	if g.Matched {
		mark = stYellow.Render("*")
	}
	count := ""
	if g.Count > 1 {
		count = fmt.Sprintf("(×%d) ", g.Count)
	}
	head := mark + " " + stDim.Render(g.Time.Format("15:04:05")) + "  " + stCyan.Render(pad(g.Source, 12)) + " "
	return item{head + stDim.Render(count) + st.Render(g.Message), g.Source + " " + g.Message, 26, 0}
}

// ---- report ----

func (m *model) reportText() string {
	res, _ := m.cur()
	var b bytes.Buffer
	var red *report.Redactor
	if m.redacted {
		red = report.NewRedactor(res)
	}
	_ = report.Markdown(&b, res, red) // a bytes.Buffer never fails to write
	return b.String()
}

func (m *model) reportView() string {
	var lines []string
	for l := range strings.SplitSeq(strings.TrimRight(m.reportText(), "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "#"):
			lines = append(lines, stBold.Render(l))
		case strings.HasPrefix(l, "```"), strings.HasPrefix(l, "_"):
			lines = append(lines, stGray.Render(l))
		default:
			lines = append(lines, fit(l, m.w-4))
		}
	}
	title := stPlain.Render("Report") + "  " + stDim.Render("redacted: no addresses, host, user or Wi-Fi names")
	redact := "show unredacted"
	if !m.redacted {
		title, redact = stPlain.Render("Report")+"  "+stRed.Render("NOT redacted: addresses and names included"), "redact"
	}
	return m.fullScreen(title, lines, [][2]string{{"↑↓", "scroll"}, {"Y", "copy"}, {"S", "save"}, {"X", redact}, {"esc", "back"}})
}

// copyCmd puts text on the clipboard: with wl-copy or xclip when the session has a display for
// them, otherwise through the terminal (OSC 52, which most terminals support, also over SSH).
func copyCmd(text string) tea.Cmd {
	for _, c := range []struct {
		env  string
		argv []string
	}{{"WAYLAND_DISPLAY", []string{"wl-copy"}}, {"DISPLAY", []string{"xclip", "-selection", "clipboard"}}} {
		if os.Getenv(c.env) == "" {
			continue
		}
		if _, err := exec.LookPath(c.argv[0]); err != nil {
			continue
		}
		return func() tea.Msg {
			cmd := exec.Command(c.argv[0], c.argv[1:]...)
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err != nil {
				return statusMsg("copy failed: " + err.Error())
			}
			return statusMsg("copied")
		}
	}
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg { return statusMsg("copied through the terminal") })
}

// ---- help ----

func (m *model) helpView() string {
	section := func(name string) string { return stGray.Render(strings.ToUpper(name)) }
	key := func(k, what string, kw int) string { return "  " + stBold.Render(pad(k, kw)) + what }
	moving := []string{
		section("moving around"), "",
		key("↑ ↓", "move in the list", 8),
		key("← →", "sessions / problems", 8),
		key("enter", "a problem's journal lines", 8),
		key("T", "problems by importance / by time", 8),
		key("/", "search in a list", 8),
		key("esc", "clear the search, then back", 8),
		key("q", "quit", 8),
	}
	views := []string{
		section("views"), "",
		key("L", "the last minutes of the session", 4),
		key("R", "the report to paste on a forum", 4),
		key("W", "warnings no rule recognises", 4),
		key("U", "only the sessions that ended badly", 4),
	}
	half := (m.w - 4) / 2
	var lines []string
	for i := range max(len(moving), len(views)) {
		l, r := "", ""
		if i < len(moving) {
			l = moving[i]
		}
		if i < len(views) {
			r = views[i]
		}
		lines = append(lines, pad(l, half)+r)
	}
	word := func(st lipgloss.Style, w, what string) string { return "  " + st.Render(pad(w, 16)) + what }
	lines = append(lines, "", stGray.Render(strings.Repeat("┄", m.w-4)), "", section("how a session ended"), "",
		word(stDim, "shut down", "turned off from the system: a normal end"),
		word(stDim, "restarted", "restarted from the system: a normal end"),
		word(stRed, "abrupt end", "no shutdown: a power cut, a held power button, or a freeze"),
		word(stRed, "never woke up", "suspended or hibernated, and never resumed"),
		word(stRed, "kernel panic", "the kernel stopped on an error it could not survive"),
		"", section("levels"), "",
		word(stRed, "error", "can explain a crash or a freeze by itself"),
		word(stYellow, "warning", "worth a look"),
		word(stGray, "info", "context, known and harmless issues"),
		"", stGray.Render("Rules: built in, plus ~/.config/whyd/rules/*.toml"))
	return m.fullScreen("Help", lines, [][2]string{{"any key", "back"}})
}

// ---- lines no rule recognised ----

func (m *model) unknownView() string {
	res, _ := m.cur()
	var items []item
	for _, g := range res.Unknown {
		prio, st := "warning", stYellow // the priority the program logged it with
		if g.Priority <= 3 {
			prio, st = "error", stRed
		}
		head := stDim.Render(fmt.Sprintf("%5d  ", g.Count)) + stDim.Render(pad(m.when(res, g.First), 13)) + st.Render(pad(prio, 9)) +
			stCyan.Render(pad(g.Source, 15)) + " "
		items = append(items, item{head + g.Message, prio + " " + g.Source + " " + g.Message, 45, 0})
	}
	header := fmt.Sprintf("%5s  ", "TIMES") + pad("FIRST", 13) + pad("LEVEL", 9) + pad("PROGRAM", 16) + "MESSAGE"
	title := stPlain.Render("Other warnings") + "  " + stDim.Render(report.Plural(res.UnknownErr+res.UnknownW, "line")+", "+report.Plural(len(res.Unknown), "different message"))
	note := stGray.Render("a rule for one of them: docs/RULES.md")
	return m.listView(title, header, note, items, [][2]string{{"esc", "back"}})
}
