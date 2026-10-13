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
	"github.com/vmaltarello/whyd/internal/report"
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
	ev := res.Findings[min(m.selP, len(res.Findings)-1)]
	var items []item
	for _, e := range ev.Entries {
		prefix := e.Time.Format("15:04:05") + " " + e.Source + ": "
		items = append(items, item{stDim.Render(prefix) + e.Message, prefix + e.Message, len(prefix), 0})
	}
	title := fmt.Sprintf("Journal lines: %s  (%s)", ev.Title, report.Plural(len(ev.Entries), "line"))
	return m.listView(title, "", items, [][2]string{{"Esc", "Back"}})
}

// ---- the last minutes ----

func (m *model) tailView() string {
	res, _ := m.cur()
	title := fmt.Sprintf("Last %s of the %s, before %s", analyze.Duration(m.window), report.SessionName(res), report.When(res.Boot.Last))
	hidden := "Show hidden"
	if m.showHidden {
		hidden = "Fold hidden"
	}
	return m.listView(title, "", m.tailItems(res), [][2]string{{"Enter", "Open block"}, {"+-", "Window"}, {"N", hidden}, {"Esc", "Back"}})
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
		t := fmt.Sprintf("%13s… %s, Enter to show", "", report.Plural(n, "hidden line"))
		if open {
			t = fmt.Sprintf("%13s… %s, Enter to fold", "", report.Plural(n, "hidden line"))
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
	end := "journal ends " + res.Boot.Last.Format("15:04:05")
	if g := res.Gap(); g > 0 {
		end += ", next boot after " + analyze.Duration(g)
	}
	if q := report.QuietLine(res); q != "" {
		end += "; " + q
	}
	endSt := endStyle(res.Verdict.Kind)
	items = append(items, item{endSt.Render("-- " + end + " --"), end, 3, 0})
	m.tailKey, m.tailCache = key, items
	return items
}

// tailItem is one (collapsed) line of the tail: mark, time, source, message.
func (m *model) tailItem(g analyze.TailGroup, st lipgloss.Style) item {
	mark := " "
	if g.Matched {
		mark = stYellowB.Render("*")
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
	title := "Report, redacted"
	redact := "Show all"
	if !m.redacted {
		title, redact = "Report, NOT redacted", "Redact"
	}
	return m.fullScreen(title, lines, [][2]string{{"↑↓", "Scroll"}, {"X", redact}, {"Y", "Copy"}, {"S", "Save"}, {"Esc", "Back"}})
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
	rows := [][2]string{
		{"↑ ↓", "move in the focused panel or list"},
		{"← →", "sessions panel / problems panel"},
		{"Enter", "on a problem: its journal lines; on \"older\": 30 more days of sessions"},
		{"L", "the last minutes of the session"},
		{"R", "the report to paste on a forum (X redaction, Y copy, S save)"},
		{"W", "warnings and errors no rule recognised"},
		{"U", "only sessions without a clean shutdown"},
		{"/", "in a list: search"},
		{"Esc", "clear the search, then back"},
		{"Q", "quit"},
		{"", ""},
		{"err", "can explain a crash or a freeze by itself"},
		{"warn", "worth a look"},
		{"info", "context, known and harmless issues"},
	}
	var lines []string
	for _, r := range rows {
		lines = append(lines, stBold.Render(pad(r[0], 8))+r[1])
	}
	lines = append(lines, "", stDim.Render("Rules: built in, plus ~/.config/whyd/rules/*.toml"))
	return m.fullScreen("Help", lines, [][2]string{{"any key", "Back"}})
}

// ---- lines no rule recognised ----

func (m *model) unknownView() string {
	res, _ := m.cur()
	var items []item
	for _, g := range res.Unknown {
		prio, st := "warning", stYellowB // the priority the program logged it with
		if g.Priority <= 3 {
			prio, st = "err", stRedB
		}
		head := st.Render(pad(prio, 10)) + stDim.Render(pad(fmt.Sprint(g.Count), 7)) + pad(report.Within(g.First, res.Boot.Last), 11) +
			stCyan.Render(pad(g.Source, 15)) + " "
		items = append(items, item{head + g.Message, prio + " " + g.Source + " " + g.Message, 44, 0})
	}
	header := pad("PRIORITY", 10) + pad("TIMES", 7) + pad("FIRST", 11) + pad("SOURCE", 16) + "MESSAGE"
	title := fmt.Sprintf("Not recognised: %d errors, %d warnings, %d messages", res.UnknownErr, res.UnknownW, len(res.Unknown))
	return m.listView(title, header, items, [][2]string{{"Esc", "Back"}})
}
