package report

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/vmaltarello/whyd/internal/analyze"
	"github.com/vmaltarello/whyd/internal/rules"
)

// Now is the clock used for relative times; tests replace it.
var Now = time.Now

// beforeWindow is how close to the end a warning must be to count as a possible cause.
const beforeWindow = 2 * time.Hour

// When writes a moment relative to now: "today 10:53", "yesterday 18:49", "Thu 18:49" within
// the last week, "8 Oct 18:49" before, with the year when it isn't this one.
func When(t time.Time) string {
	now := Now()
	day := func(x time.Time) time.Time { y, m, d := x.Date(); return time.Date(y, m, d, 0, 0, 0, 0, x.Location()) }
	days := int(math.Round(day(now).Sub(day(t)).Hours() / 24)) // a day is 23 or 25 hours when the clocks change
	clock := t.Format("15:04")
	switch {
	case days == 0:
		return "today " + clock
	case days == 1:
		return "yesterday " + clock
	case days > 1 && days < 7:
		return t.Format("Mon ") + clock
	case t.Year() == now.Year():
		return t.Format("2 Jan ") + clock
	}
	return t.Format("2 Jan 2006 ") + clock
}

// Within writes a moment of a session compactly: the time alone on the day the session ended,
// the weekday before it in the same week, the date otherwise. The session line says which days.
func Within(t, end time.Time) string {
	y1, m1, d1 := t.Date()
	y2, m2, d2 := end.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return t.Format("15:04")
	case end.Sub(t) < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	}
	return t.Format("2 Jan 15:04")
}

// WhenShort is When in at most 12 cells, for table columns: "yest. 18:49".
func WhenShort(t time.Time) string {
	return strings.Replace(When(t), "yesterday", "yest.", 1)
}

// SessionName names the session: "previous session", "this session", "session -4".
func SessionName(r *analyze.Result) string {
	switch {
	case r.Current:
		return "this session"
	case r.Boot.ID == "":
		return "recorded session"
	case r.Boot.Index == -1:
		return "previous session"
	}
	return fmt.Sprintf("session %d", r.Boot.Index)
}

// Span is "yesterday 18:49 to today 12:11", or "today 12:12 to now" for the running session.
func Span(r *analyze.Result) string {
	if r.Current {
		return When(r.Boot.First) + " to now"
	}
	return When(r.Boot.First) + " to " + When(r.Boot.Last)
}

// Words is what a request adds after a time on screen: " with sudo reboot", " by alex, with sudo
// reboot". The user is named only when it isn't the one running whyd, or the session was recorded
// on another machine; the report never names them.
func Words(r *analyze.Result, q *analyze.Request) string {
	return q.Words(q != nil && q.User != "" && (r.Boot.ID == "" || q.User != Me()))
}

// By is who or what asked for the end, as a part of its own: "with sudo reboot", "by alex, with
// sudo reboot". "" if the journal doesn't say.
func By(r *analyze.Result) string { return strings.TrimLeft(Words(r, r.Verdict.By), ", ") }

// EndLine is the verdict's summary with Words in place of the words that don't name the user.
func EndLine(r *analyze.Result) string {
	w := r.Verdict.By.Words(false)
	if w == "" {
		return r.Verdict.Summary
	}
	return strings.Replace(r.Verdict.Summary, w, Words(r, r.Verdict.By), 1)
}

// Length is "17h22m", with the time asleep when there was any: "17h22m, 10h38m asleep".
func Length(r *analyze.Result) string {
	s := analyze.Duration(r.Boot.Duration())
	if r.Asleep >= time.Minute {
		s += ", " + analyze.Duration(r.Asleep) + " asleep"
	}
	return s
}

// QuietLine is "nothing abnormal logged after 11:57" when the end came after a silence.
func QuietLine(r *analyze.Result) string {
	if r.Quiet == 0 {
		return ""
	}
	return "nothing abnormal logged after " + r.Boot.Last.Add(-r.Quiet).Format("15:04")
}

// Before names what most likely led to the end: "10:53 out of memory, 4 processes killed; sunshine
// killed by SIGTRAP".
// Errors count whatever their time; warnings only within beforeWindow of the end, because a
// warning hours earlier (often one logged at every boot) explains nothing. At most two, one per
// category, in time order. Empty for a clean shutdown: there is nothing to explain.
func Before(r *analyze.Result) string {
	if r.Verdict.Kind.Clean() {
		return ""
	}
	var cands []*rules.Event
	for _, ev := range r.Findings {
		recent := r.Boot.Last.Sub(ev.Last) <= beforeWindow
		if ev.Rule.Level == rules.Err || ev.Rule.Level == rules.Warn && recent {
			cands = append(cands, ev)
		}
	}
	slices.SortStableFunc(cands, func(a, b *rules.Event) int {
		return cmp.Or(cmp.Compare(b.Rule.Level, a.Rule.Level), b.Last.Compare(a.Last))
	})
	var serious []*rules.Event
	seen := map[string]bool{} // one per category: allocation stalls add nothing to an OOM
	for _, ev := range cands {
		if len(serious) < 2 && !seen[ev.Rule.Category] {
			seen[ev.Rule.Category] = true
			serious = append(serious, ev)
		}
	}
	slices.SortStableFunc(serious, func(a, b *rules.Event) int { return a.First.Compare(b.First) })
	var parts []string
	last := ""
	for _, ev := range serious {
		at := ev.First.Format("15:04")
		p := ev.Title
		if at != last {
			p = at + " " + p
		}
		last = at
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ") // titles can hold commas
}
