package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/vmaltarello/whyd/internal/analyze"
	"github.com/vmaltarello/whyd/internal/rules"
)

// Version is printed in reports; set by main.
var Version = "dev"

// HiddenSummary lists hidden lines per rule: "437 firewall log lines, 149 rtw88 push errors".
func HiddenSummary(r *analyze.Result) string {
	var parts []string
	for _, n := range r.Hidden {
		parts = append(parts, fmt.Sprintf("%d %s", n.Count, n.Rule.Title))
	}
	return strings.Join(parts, ", ")
}

// Detail is the line under a finding: the most useful facts, or the rule's note.
func Detail(r *analyze.Result, ev *rules.Event) string {
	if o := r.OOMOf(ev); o != nil {
		return MemoryLine(o)
	}
	if ev.Count > 1 && ev.Last.Sub(ev.First) > time.Minute {
		return fmt.Sprintf("from %s to %s", Within(ev.First, r.Boot.Last), Within(ev.Last, r.Boot.Last))
	}
	return ev.Note()
}

// MemoryLine is "procs 6.0G, shmem 4.4G, gpu 7.5G, swap 0K/4.0G".
func MemoryLine(o *analyze.OOMReport) string {
	s := fmt.Sprintf("procs %s, shmem %s", Size(o.AnonKB), Size(o.ShmemKB))
	if o.GPUKB >= 0 {
		s += ", gpu " + Size(o.GPUKB)
	}
	return s + fmt.Sprintf(", swap %s/%s", Size(o.SwapFreeKB), Size(o.SwapKB))
}

// Size formats kilobytes like top does: "7.5G", "210M", "512K".
func Size(kb int64) string {
	switch {
	case kb >= 1024*1024:
		return fmt.Sprintf("%.1fG", float64(kb)/1024/1024)
	case kb >= 1024:
		return fmt.Sprintf("%dM", kb/1024)
	}
	return fmt.Sprintf("%dK", kb)
}

// Markdown writes the report to paste on a forum: absolute dates, a table, the last lines.
// With red nil, nothing is hidden.
// It returns the first error from w.
func Markdown(w io.Writer, r *analyze.Result, red *Redactor) error {
	clean := func(s string) string {
		if red == nil {
			return s
		}
		return red.Redact(s)
	}
	var werr error // the first write error, later writes are skipped
	p := func(format string, a ...any) {
		if werr == nil {
			_, werr = fmt.Fprintf(w, format, a...)
		}
	}

	p("## whyd: %s\n\n", SessionName(r))
	p("- **when:** %s to %s (%s)", r.Boot.First.Format("Mon 2 Jan 15:04"), r.Boot.Last.Format("Mon 2 Jan 15:04"), Length(r))
	if r.Kernel != "" {
		p(", kernel %s", r.Kernel)
	}
	if mc := r.Machine.Line(); mc != "" {
		p("\n- **machine:** %s", mc)
	}
	p("\n- **end:** %s", clean(r.Verdict.Summary))
	if q := QuietLine(r); q != "" {
		p("; %s", q)
	}
	p("\n")
	if b := Before(r); b != "" {
		p("- **before:** %s\n", clean(b))
	}

	p("\n### Problems\n\n")
	if len(r.Findings) == 0 {
		p("None recognised.\n")
	} else {
		p("| time | level | what | times |\n|---|---|---|---|\n")
		for _, ev := range r.Findings {
			what := clean(ev.Title)
			if d := Detail(r, ev); d != "" {
				what += " (" + clean(d) + ")"
			}
			p("| %s | %s | %s | %d |\n", ev.First.Format("Mon 15:04"), ev.Rule.Level.Word(), cell(what), ev.Count)
		}
	}
	if r.OOM != nil && len(r.OOM.Top) > 0 {
		var top []string
		for _, pr := range r.OOM.Top {
			top = append(top, clean(pr.Name)+" "+Size(pr.RSSKB))
		}
		p("\nLargest processes at the first out-of-memory report: %s\n", strings.Join(top, ", "))
	}
	if len(r.Unknown) > 0 {
		p("\n### Other warnings\n\n%s. The most serious and frequent:\n\n", Others(r))
		p("| level | times | program | message |\n|---|---|---|---|\n")
		for _, g := range r.Unknown[:min(10, len(r.Unknown))] {
			prio := "warning"
			if g.Priority <= 3 {
				prio = "error"
			}
			p("| %s | %d | %s | %s |\n", prio, g.Count, cell(clean(g.Source)), cell(clean(g.Message)))
		}
	}
	if n := HiddenSummary(r); n != "" {
		p("\nHarmless lines hidden: %s\n", n)
	}

	var groups []analyze.TailGroup
	for _, g := range analyze.Collapse(r.TailSince(analyze.TailWindow)) {
		if !g.Hidden {
			groups = append(groups, g)
		}
	}
	var last []string
	for _, g := range groups[max(0, len(groups)-15):] {
		first, _, _ := strings.Cut(g.Message, "\n")
		repeat := ""
		if g.Count > 1 {
			repeat = fmt.Sprintf("  (×%d)", g.Count)
		}
		// the source too: logger tags lines with the user name by default
		last = append(last, fmt.Sprintf("%s %s: %s%s", g.Time.Format("15:04:05"), clean(g.Source), clean(first), repeat))
	}
	fence := "```" // longer than any run of backticks in the lines, so none closes the block
	for strings.Contains(strings.Join(last, "\n"), fence) {
		fence += "`"
	}
	p("\n### Last lines\n\n%s\n", fence)
	for _, l := range last {
		p("%s\n", l)
	}
	p("%s\n\n", fence)
	note := "nothing redacted"
	if red != nil {
		note = "addresses, serial numbers, hostname, users and network names redacted"
	}
	p("_whyd %s, %s_\n", Version, note)
	return werr
}

// cell makes text safe inside a Markdown table cell.
func cell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
