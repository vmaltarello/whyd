package report

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/vmaltarello/whyd/internal/analyze"
)

// Text writes the summary as plain text, for pipes, SSH sessions and scripts.
// It returns the first error from w.
func Text(w io.Writer, r *analyze.Result) error {
	var werr error // the first write error, later writes are skipped
	p := func(format string, a ...any) {
		if werr == nil {
			_, werr = fmt.Fprintf(w, format, a...)
		}
	}

	p("whyd: %s, %s (%s)\n\n", SessionName(r), Span(r), Length(r))
	p("end     %s\n", r.Verdict.Summary)
	if q := QuietLine(r); q != "" {
		p("        %s\n", q)
	}
	if b := Before(r); b != "" {
		label := "before"
		if r.Verdict.Kind == analyze.EndRunning {
			label = "so far"
		}
		p("%-7s %s\n", label, b)
	}
	p("\n")

	if len(r.Findings) == 0 {
		p("No problems recognised.\n")
	} else {
		tw := 13 // "yest. 18:49"; a date of another year is longer
		for _, ev := range r.Findings {
			tw = max(tw, utf8.RuneCountInString(WhenShort(ev.First))+1)
		}
		p("%-*s %-6s %-52s %s\n", tw, "TIME", "LEVEL", "WHAT", "TIMES")
		for _, ev := range r.Findings {
			what := ev.Title
			p("%-*s %-6s %s%s%d\n", tw, WhenShort(ev.First), ev.Rule.Level, what,
				strings.Repeat(" ", max(1, 53-utf8.RuneCountInString(what))), ev.Count)
			if d := Detail(r, ev); d != "" {
				p("%*s%s\n", tw+8, "", d)
			}
		}
	}
	p("\n")
	if n := r.UnknownErr + r.UnknownW; n > 0 {
		p("unknown %d warning and error lines matched no rule (%d errors, %d messages)\n", n, r.UnknownErr, len(r.Unknown))
	}
	if n := HiddenSummary(r); n != "" {
		p("hidden  %s\n", n)
	}
	facts := []string{Plural(r.Suspends, "suspend")}
	if r.Hibernations > 0 {
		facts = append(facts, Plural(r.Hibernations, "hibernation"))
	}
	label := "session"
	if r.Kernel != "" {
		label, facts = "kernel", append([]string{r.Kernel}, facts...)
	}
	p("%-7s %s\n", label, strings.Join(facts, ", "))
	return werr
}

// Plural writes "1 suspend", "2 suspends".
func Plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
