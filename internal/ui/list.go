package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// item is one entry of a list view.
type item struct {
	text   string // styled; may be wider than the screen and hold several lines (stack traces)
	plain  string // what / searches in
	indent int    // where continuation lines start when the item is expanded
	block  int    // in the tail: the routine block this item opens or closes, 0 if none
}

func isList(v view) bool { return v == vLines || v == vTail || v == vUnknown }

// openList switches to a list view, selecting the first item, or the last one (the tail).
func (m *model) openList(v view, atEnd bool) {
	m.view, m.sel, m.off, m.search, m.searching = v, 0, 0, "", false
	m.openRoutine = map[int]bool{}
	if atEnd {
		m.sel = 1 << 20
	}
}

// listKey handles the keys every list view shares; it reports whether it used the key.
func (m *model) listKey(k string) bool {
	if m.searching {
		switch k {
		case "enter":
			m.searching = false
		case "esc":
			m.searching, m.search = false, ""
		case "backspace":
			if r := []rune(m.search); len(r) > 0 {
				m.search = string(r[:len(r)-1])
			}
		case "space":
			m.search += " "
		default:
			if len([]rune(k)) == 1 {
				m.search += k
			}
		}
		m.sel, m.off = 0, 0
		return true
	}
	switch k {
	case "/":
		m.searching, m.search = true, ""
	case "esc":
		if m.search != "" {
			m.search, m.sel, m.off = "", 0, 0
			return true
		}
		m.view = vMain
	case "up", "k":
		m.sel = max(0, m.sel-1)
	case "down", "j":
		m.sel++
	case "pgup":
		m.sel = max(0, m.sel-(m.h-6))
	case "pgdown", "space":
		m.sel += m.h - 6
	case "home", "g":
		m.sel = 0
	case "end", "G":
		m.sel = 1 << 20
	default:
		return false
	}
	return true
}

// listView draws items full screen: the selected one in reverse video and expanded to its whole
// text, the others on one line; "/" filters them.
func (m *model) listView(title, header string, items []item, keys [][2]string) string {
	w, h := m.w-4, m.h-4 // inside the frame
	if header != "" {
		h-- // the column names stay on top, out of the scrolling
	}
	shown := items
	if m.search != "" {
		q := strings.ToLower(m.search)
		shown = nil
		for _, it := range items {
			if strings.Contains(strings.ToLower(it.plain), q) {
				shown = append(shown, it)
			}
		}
		title += fmt.Sprintf("   /%s  %d of %d", m.search, len(shown), len(items))
	}
	if m.searching {
		title += "▏"
	}
	m.sel = max(0, min(m.sel, len(shown)-1))
	m.shown = shown

	// one line per item, the selected one expanded: only the lines on screen are drawn, a list
	// can hold thousands
	var exp []string
	if len(shown) > 0 {
		exp = expand(shown[m.sel], w)
	}
	total := len(shown) - 1 + len(exp)
	m.off = scrollTo(m.off, m.sel, min(len(exp), h), h)
	var lines []string
	for l := max(0, min(m.off, total-h)); l < min(total, max(0, min(m.off, total-h))+h); l++ {
		switch {
		case l < m.sel:
			lines = append(lines, firstLine(shown[l], w))
		case l < m.sel+len(exp):
			lines = append(lines, stSel.Render(pad(ansi.Strip(exp[l-m.sel]), w)))
		default:
			lines = append(lines, firstLine(shown[l-len(exp)+1], w))
		}
	}
	if len(shown) == 0 {
		lines = append(lines, stDim.Render("no lines match /"+m.search))
	}

	keys = append([][2]string{{"↑↓", "Select"}, {"/", "Search"}}, keys...)
	if m.searching {
		keys = [][2]string{{"Enter", "Keep filter"}, {"Esc", "Clear"}}
	}
	out := []string{m.header()}
	shownLines := lines
	if header != "" {
		shownLines = append([]string{stGray.Bold(true).Render(fit(header, w))}, shownLines...)
	}
	out = append(out, frame(shownLines, m.w, m.h-2, title, true)...)
	return strings.Join(append(out, m.keyBar(keys)), "\n")
}

// firstLine is an item on one line, cut to w cells.
func firstLine(it item, w int) string {
	first, _, _ := strings.Cut(it.text, "\n")
	return fit(first, w)
}

// expand wraps an item's whole text to width w: the first line takes what fits, the rest (and
// any following lines, like a stack trace) continue under the item's indent.
func expand(it item, w int) []string {
	ind := min(it.indent, w/2)
	pad := strings.Repeat(" ", ind)
	var out []string
	for i, part := range strings.Split(it.text, "\n") {
		if i == 0 { // the first line keeps its columns; cut it at a word boundary
			first, _, _ := strings.Cut(ansi.Wrap(part, w, ""), "\n")
			first = strings.TrimRight(first, " ")
			out = append(out, first)
			part = ansi.TruncateLeft(part, ansi.StringWidth(first), "")
		}
		part = strings.TrimSpace(ansi.Strip(part)) // continuation lines are plain text
		if part == "" {
			continue
		}
		for _, l := range strings.Split(ansi.Wrap(part, w-ind, ""), "\n") {
			out = append(out, pad+strings.TrimLeft(l, " "))
		}
	}
	return out
}
