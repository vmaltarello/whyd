package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/vmaltarello/whyd/internal/analyze"
	"github.com/vmaltarello/whyd/internal/rules"
)

// Only the terminal's own palette, and colour only where it means something: red for errors and
// sessions that ended badly, yellow for warnings, green for clean ends; cyan only for program
// names in journal lines, to scan them. Bold for titles, verdicts, the selection and key letters.
// The selection is a bar on the left, so it works with any theme.
var (
	red    = lipgloss.ANSIColor(1)
	green  = lipgloss.ANSIColor(2)
	yellow = lipgloss.ANSIColor(3)
	cyan   = lipgloss.ANSIColor(6)
	white  = lipgloss.ANSIColor(7)
	gray   = lipgloss.ANSIColor(8)

	stPlain  = lipgloss.NewStyle()
	stBold   = lipgloss.NewStyle().Bold(true)
	stDim    = lipgloss.NewStyle().Foreground(white)
	stGray   = lipgloss.NewStyle().Foreground(gray)
	stRed    = lipgloss.NewStyle().Foreground(red)
	stGreen  = lipgloss.NewStyle().Foreground(green)
	stYellow = lipgloss.NewStyle().Foreground(yellow)
	stCyan   = lipgloss.NewStyle().Foreground(cyan)
)

func levelStyle(l rules.Level) lipgloss.Style {
	switch l {
	case rules.Err:
		return stRed
	case rules.Warn:
		return stYellow
	case rules.Hidden:
		return stGray
	default: // rules.Info, rules.Context
		return stDim
	}
}

func endStyle(k analyze.EndKind) lipgloss.Style {
	switch {
	case k.Clean():
		return stGreen
	case k == analyze.EndRunning:
		return stPlain
	}
	return stRed
}

// mark is the left edge of a list row: a bar on the selected one, white when its panel has the
// focus, grey when not.
func mark(sel, focused bool) string {
	switch {
	case !sel:
		return "  "
	case focused:
		return "▌ "
	}
	return stGray.Render("▌") + " "
}

// fit cuts a (possibly styled) string to w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// pad fills a (possibly styled) string with spaces up to w cells, cutting it if longer.
func pad(s string, w int) string {
	s = fit(s, w)
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

// spread puts left and right on one line of width w, right-aligned; left is cut if needed.
func spread(left, right string, w int) string {
	rw := lipgloss.Width(right)
	left = fit(left, w-rw-1)
	return left + strings.Repeat(" ", max(1, w-lipgloss.Width(left)-rw)) + right
}

// frame draws a rounded border of w×h around lines, with the title in the top edge. The border
// is bright when the panel has the focus. A title already styled keeps its styles.
func frame(lines []string, w, h int, title string, focused bool) []string {
	return frameNote(lines, w, h, title, "", focused)
}

// frameNote is frame with a note in the bottom edge, on the right: what the panel doesn't show.
func frameNote(lines []string, w, h int, title, note string, focused bool) []string {
	bst := stGray
	if focused {
		bst = stPlain
	}
	if !strings.Contains(title, "\x1b") {
		title = stBold.Render(title)
	}
	top := bst.Render("╭─") + " " + title + " "
	top += bst.Render(strings.Repeat("─", max(0, w-5-lipgloss.Width(title))) + "╮")
	out := []string{fit(top, w)}
	for i := range h - 2 {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, bst.Render("│")+" "+pad(l, w-4)+" "+bst.Render("│"))
	}
	if note == "" || lipgloss.Width(note) > w-8 {
		return append(out, bst.Render("╰"+strings.Repeat("─", max(0, w-2))+"╯"))
	}
	return append(out, bst.Render("╰"+strings.Repeat("─", w-5-lipgloss.Width(note)))+" "+note+" "+bst.Render("─╯"))
}

// wrap breaks plain text into lines of at most w cells.
func wrap(s string, w int) []string {
	var out []string
	for para := range strings.SplitSeq(strings.TrimSpace(s), "\n") {
		out = append(out, strings.Split(ansi.Wordwrap(para, w, ""), "\n")...)
	}
	return out
}

// joinParagraphs turns TOML multi-line text into paragraphs: single newlines become spaces.
func joinParagraphs(s string) string {
	paras := strings.Split(strings.TrimSpace(s), "\n\n")
	for i, p := range paras {
		paras[i] = strings.Join(strings.Fields(p), " ")
	}
	return strings.Join(paras, "\n\n")
}

func window[T any](lines []T, off, h int) []T {
	off = max(0, min(off, len(lines)-h))
	return lines[off:min(len(lines), off+h)]
}

// scrollTo returns the offset that keeps line sel (and span lines after it) visible.
func scrollTo(off, sel, span, h int) int {
	if sel < off {
		return sel
	}
	if sel+span > off+h {
		return sel + span - h
	}
	return off
}
