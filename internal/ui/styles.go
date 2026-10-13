package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/vmaltarello/whyd/internal/analyze"
	"github.com/vmaltarello/whyd/internal/rules"
)

// Only the terminal's own palette, and colour only where it means something: red for errors and
// sessions that didn't shut down, yellow for warnings, green for clean ends. Selection and key
// labels use reverse video, so they work with any theme.
var (
	red    = lipgloss.ANSIColor(1)
	green  = lipgloss.ANSIColor(2)
	yellow = lipgloss.ANSIColor(3)
	cyan   = lipgloss.ANSIColor(6)
	white  = lipgloss.ANSIColor(7)
	gray   = lipgloss.ANSIColor(8)

	stPlain   = lipgloss.NewStyle()
	stBold    = lipgloss.NewStyle().Bold(true)
	stDim     = lipgloss.NewStyle().Foreground(white)
	stGray    = lipgloss.NewStyle().Foreground(gray)
	stRed     = lipgloss.NewStyle().Foreground(red)
	stRedB    = stRed.Bold(true)
	stGreen   = lipgloss.NewStyle().Foreground(green)
	stYellow  = lipgloss.NewStyle().Foreground(yellow)
	stYellowB = stYellow.Bold(true)
	stCyan    = lipgloss.NewStyle().Foreground(cyan)
	stSel     = lipgloss.NewStyle().Reverse(true)              // selection in the focused panel
	stSelBlur = lipgloss.NewStyle().Bold(true).Underline(true) // selection in the other panel
	stKey     = lipgloss.NewStyle().Reverse(true)              // key labels in the key bar
)

func levelStyle(s rules.Severity) lipgloss.Style {
	switch s {
	case rules.Critical:
		return stRedB
	case rules.Warning:
		return stYellowB
	case rules.Noise:
		return stGray
	default: // rules.Info
		return stPlain
	}
}

func endStyle(k analyze.EndKind) lipgloss.Style {
	switch {
	case k.Clean():
		return stGreen
	case k == analyze.EndRunning:
		return stPlain
	}
	return stRedB
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

// frame draws a square border of w×h around lines, with the title in the top edge.
// The border is bright when the panel has the focus.
func frame(lines []string, w, h int, title string, focused bool) []string {
	bst, tst := stGray, stDim
	if focused {
		bst, tst = stPlain, stBold
	}
	top := bst.Render("┌─") + tst.Render(" "+title+" ")
	top += bst.Render(strings.Repeat("─", max(0, w-3-lipgloss.Width(title)-2)) + "┐")
	out := []string{fit(top, w)}
	for i := range h - 2 {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, bst.Render("│")+" "+pad(l, w-4)+" "+bst.Render("│"))
	}
	return append(out, bst.Render("└"+strings.Repeat("─", max(0, w-2))+"┘"))
}

// wrap breaks plain text into lines of at most w cells.
func wrap(s string, w int) []string {
	var out []string
	for _, para := range strings.Split(strings.TrimSpace(s), "\n") {
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
