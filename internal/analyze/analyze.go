// Package analyze turns the entries of one boot into a verdict, findings, a timeline and the
// last minutes before the end.
package analyze

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vmaltarello/whyd/internal/journal"
	"github.com/vmaltarello/whyd/internal/rules"
)

// Input is what an analysis needs: the boot, how to read its entries, and the rules.
type Input struct {
	Boot    journal.Boot  // may be zero when reading a file: filled from the entries
	Current bool          // the boot that is running now
	Next    *journal.Boot // the boot after this one, if any
	Rules   *rules.Set
	Read    func(fn func(journal.Entry) error) error
}

// TailLine is one entry of the last minutes, with what the rules made of it.
type TailLine struct {
	journal.Entry
	Hidden  bool // hidden by a rule
	Matched bool // part of an event
}

// Result is everything the outputs (text, report, TUI) show about a boot.
type Result struct {
	Boot         journal.Boot
	Current      bool
	Next         *journal.Boot
	Verdict      Verdict
	Kernel       string
	Machine      Machine
	Lines        int // lines read: with a journal.Selection, not every line of the boot
	HiddenLines  int
	Suspends     int
	Hibernations int
	Asleep       time.Duration
	Sleeps       []Interval     // when the system was asleep; the last one is open (To = end) if it never woke up
	Events       []*rules.Event // every event, by time of first occurrence
	Host         string         // the machine's hostname, from the journal ("" if not recorded)
	Users        []string       // people who logged in (uid 1000 and up), for the redaction of reports
	Findings     []*rules.Event // what's worth a look, most serious first
	Hidden       []rules.HiddenCount
	Tail         []TailLine    // the last TailKeep before the end (capped)
	Quiet        time.Duration // from the last warning to the end, for unclean ends
	OOM          *OOMReport    // details of the first out-of-memory report, if any
	ooms         map[*rules.Event]*OOMReport

	// warnings and errors no rule recognised, grouped by message: nothing disappears silently
	Unknown              []UnknownGroup
	UnknownErr, UnknownW int // lines (not groups)
}

// UnknownGroup is a message no rule recognised, with how often it appeared.
type UnknownGroup struct {
	Source   string
	Message  string // first line of the first occurrence
	Priority int    // the most serious occurrence
	Count    int
	First    time.Time
	Last     time.Time
}

const (
	TailWindow   = 15 * time.Minute // what outputs show by default
	TailKeep     = time.Hour        // what the result keeps, so a viewer can widen the window
	maxTailLines = 5000
	quietMin     = 5 * time.Minute // shorter silences before an unclean end are not worth a finding
	maxUnknown   = 2000
)

// numbers in a message (counters, addresses, PIDs) don't make it a different message
var numbers = regexp.MustCompile(`0x[0-9a-fA-F]+|[0-9]+`)

// pam_unix: "session opened for user alex(uid=1000) by (uid=0)"
var sessionUser = regexp.MustCompile(`session opened for user ([^\s(]+)\(uid=(\d+)\)`)

// QuietRule is the synthetic rule behind the "quiet before the end" finding.
var QuietRule = &rules.Rule{
	ID:       "whyd.quiet-end",
	Category: "end",
	Level:    rules.Info,
	Explain: "No warnings or errors in the last minutes, only routine lines: the system didn't " +
		"crash, it stopped. Typical of a frozen desktop or GPU (the kernel keeps logging while the " +
		"screen doesn't move) or of a power cut.",
}

// Run reads the boot once and builds the result.
func Run(in Input) (*Result, error) {
	res := &Result{Boot: in.Boot, Current: in.Current, Next: in.Next}
	eng := rules.NewEngine(in.Rules)
	var st endState
	var ring tailRing
	var first time.Time
	var lastWarn time.Time

	unknown := map[string]*UnknownGroup{}
	err := in.Read(func(e journal.Entry) error {
		if res.Lines == 0 {
			first = e.Time
		}
		res.Lines++
		hidden, matched := eng.Feed(e)
		// a warning or error no rule took; an empty one says nothing a rule could explain (a
		// message too long for a JSON recording made without --all reads as empty)
		if !hidden && !matched && e.Priority >= 0 && e.Priority <= 4 && strings.TrimSpace(e.Message) != "" {
			if e.Priority <= 3 {
				res.UnknownErr++
			} else {
				res.UnknownW++
			}
			line1, _, _ := strings.Cut(e.Message, "\n")
			key := e.Source + "\x00" + numbers.ReplaceAllString(line1, "#")
			if g := unknown[key]; g != nil {
				g.Count++
				g.Last = e.Time
				g.Priority = min(g.Priority, e.Priority)
			} else if len(unknown) < maxUnknown {
				unknown[key] = &UnknownGroup{e.Source, line1, e.Priority, 1, e.Time, e.Time}
			}
		}
		if hidden {
			res.HiddenLines++
		} else if e.Priority >= 0 && e.Priority <= 4 {
			lastWarn = e.Time
		}
		st.feed(e, res.Lines)
		if e.Host != "" {
			res.Host = e.Host
		}
		if m := sessionUser.FindStringSubmatch(e.Message); m != nil && !slices.Contains(res.Users, m[1]) {
			if uid, _ := strconv.Atoi(m[2]); uid >= 1000 && uid < 60000 { // not system users
				res.Users = append(res.Users, m[1])
			}
		}
		if e.Kernel {
			if res.Kernel == "" && strings.HasPrefix(e.Message, "Linux version ") {
				if f := strings.Fields(e.Message); len(f) > 2 {
					res.Kernel = f[2]
				}
			}
			res.Machine.feed(e.Message)
		}
		ring.add(TailLine{e, hidden, matched})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if res.Lines == 0 {
		return nil, fmt.Errorf("no journal entries for this boot")
	}

	last := ring.lines()
	end := last[len(last)-1].Time
	if res.Boot.First.IsZero() {
		res.Boot.First, res.Boot.Last = first, end
	}
	if res.Boot.Last.Before(end) {
		res.Boot.Last = end
	}

	res.Events, res.Hidden = eng.Finish()
	res.Suspends, res.Hibernations, res.Asleep, res.Sleeps = st.suspends, st.hibernations, st.asleep, st.sleeps
	if st.sleeping {
		res.Sleeps = append(res.Sleeps, Interval{st.sleepStart, end, st.hibernate})
	}
	res.Verdict = st.verdict(res)

	cut := end.Add(-TailKeep)
	i, _ := slices.BinarySearchFunc(last, cut, func(l TailLine, t time.Time) int { return l.Time.Compare(t) })
	res.Tail = last[i:]

	if !res.Verdict.Kind.Clean() && res.Verdict.Kind != EndRunning && !lastWarn.IsZero() {
		if q := end.Sub(lastWarn); q >= quietMin {
			res.Quiet = q
			res.Events = append(res.Events, &rules.Event{
				Rule: QuietRule, First: lastWarn, Last: end, Count: 1,
				Title: fmt.Sprintf("quiet for %s before the end", Duration(q)),
			})
		}
	}

	// groups are recorded when they close, and merged events keep their first time
	slices.SortStableFunc(res.Events, func(a, b *rules.Event) int { return a.First.Compare(b.First) })
	for _, ev := range res.Events { // every out-of-memory report, whatever rule took it
		if o := parseOOM(ev); o != nil {
			if res.OOM == nil {
				res.OOM, res.ooms = o, map[*rules.Event]*OOMReport{}
			}
			res.ooms[ev] = o
		}
	}
	res.Findings = findings(res.Events)
	for _, g := range unknown {
		res.Unknown = append(res.Unknown, *g)
	}
	slices.SortFunc(res.Unknown, func(a, b UnknownGroup) int {
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), cmp.Compare(b.Count, a.Count), a.First.Compare(b.First))
	})
	return res, nil
}

// tailRing keeps the last maxTailLines lines read, without copying them as the boot goes on.
type tailRing struct {
	buf  []TailLine
	next int // where the next line goes once buf is full
}

func (r *tailRing) add(l TailLine) {
	if len(r.buf) < maxTailLines {
		r.buf = append(r.buf, l)
		return
	}
	r.buf[r.next] = l
	r.next = (r.next + 1) % maxTailLines
}

// lines returns the lines kept, oldest first.
func (r *tailRing) lines() []TailLine {
	return append(r.buf[r.next:len(r.buf):len(r.buf)], r.buf[:r.next]...)
}

// Needs is the part of a boot the analysis uses (see journal.Selection): warnings and errors,
// every line of the programs the rules name (the kernel, systemd…), logins for the redaction,
// and everything in the last TailKeep, where the verdict and the last minutes come from.
func Needs(set *rules.Set) *journal.Selection {
	sel := &journal.Selection{Recent: TailKeep, Matches: []string{"SYSLOG_FACILITY=10"}} // authpriv: logins
	for _, src := range set.Sources() {
		sel.Matches = append(sel.Matches, "SYSLOG_IDENTIFIER="+src, "_COMM="+src)
	}
	return sel
}

// findings keeps what a person should look at: everything but hidden lines and context
// (suspend/resume markers, duplicates of another finding).
func findings(events []*rules.Event) []*rules.Event {
	var out []*rules.Event
	for _, ev := range events {
		if ev.Rule.Level >= rules.Info {
			out = append(out, ev)
		}
	}
	slices.SortStableFunc(out, func(a, b *rules.Event) int {
		if c := cmp.Compare(b.Rule.Level, a.Rule.Level); c != 0 {
			return c
		}
		return a.First.Compare(b.First)
	})
	return out
}

// Summary is what lists show about a session: small, and stored in the cache for ended ones.
type Summary struct {
	Kind   EndKind
	Errors int       // findings of level err
	Worst  string    // title of the most serious warning or error, "" if none
	Last   time.Time // last entry: a cached summary is valid only for the same end
	Kernel string    `json:",omitempty"` // to tell when it changed from one session to the next
}

// Bad tells whether the session ended without a clean shutdown.
func (s Summary) Bad() bool { return !s.Kind.Clean() && s.Kind != EndRunning }

// Summarize returns the summary of the result.
func (r *Result) Summarize() Summary {
	s := Summary{Kind: r.Verdict.Kind, Errors: r.Count(rules.Err), Last: r.Boot.Last, Kernel: r.Kernel}
	if w := r.Worst(); w != nil && w.Rule.Level >= rules.Warn {
		s.Worst = w.Title
	}
	return s
}

// Worst returns the most serious finding, or nil.
func (r *Result) Worst() *rules.Event {
	if len(r.Findings) == 0 {
		return nil
	}
	return r.Findings[0]
}

// Count returns how many findings have that level.
func (r *Result) Count(l rules.Level) int {
	n := 0
	for _, ev := range r.Findings {
		if ev.Rule.Level == l {
			n++
		}
	}
	return n
}

// OOMOf returns the out-of-memory report read from this event's lines, or nil.
func (r *Result) OOMOf(ev *rules.Event) *OOMReport { return r.ooms[ev] }

// Gap is the time between the last entry and the start of the next boot (0 if unknown).
func (r *Result) Gap() time.Duration {
	if r.Next == nil {
		return 0
	}
	return r.Next.First.Sub(r.Boot.Last)
}

// Duration formats a duration compactly: "17h22m", "14m", "63s".
func Duration(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes()))
	}
	d = d.Round(time.Minute)
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// TailGroup is a run of consecutive tail lines that say the same thing.
type TailGroup struct {
	TailLine
	Count int
}

var digits = regexp.MustCompile(`[0-9]+`)

// Collapse groups lines from the same source whose messages differ only in numbers (timestamps,
// counters), when they repeat back to back or interleaved within the same second, so the last
// minutes read like a story instead of a wall.
func Collapse(lines []TailLine) []TailGroup {
	var out []TailGroup
	var keys []string
	key := func(l TailLine) string { return l.Source + "\x00" + digits.ReplaceAllString(l.Message, "#") }
	for _, l := range lines {
		k := key(l)
		merged := false
		for i := len(out) - 1; i >= 0 && i >= len(out)-3; i-- {
			g := &out[i]
			if g.Source != l.Source || (i < len(out)-1 && l.Time.Sub(g.Time) > time.Second) {
				break
			}
			if keys[i] == k && g.Hidden == l.Hidden {
				g.Count++
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, TailGroup{l, 1})
			keys = append(keys, k)
		}
	}
	return out
}

// TailSince returns the tail lines from end-window to the end.
func (r *Result) TailSince(window time.Duration) []TailLine {
	cut := r.Boot.Last.Add(-window)
	i, _ := slices.BinarySearchFunc(r.Tail, cut, func(l TailLine, t time.Time) int { return l.Time.Compare(t) })
	return r.Tail[i:]
}
