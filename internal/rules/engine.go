package rules

import (
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/vmaltarello/whyd/internal/journal"
)

// Event is one problem (or a burst of the same problem) recognised by a rule.
type Event struct {
	Rule    *Rule
	First   time.Time
	Last    time.Time
	Count   int
	Title   string
	Fields  map[string]string   // fields of the first occurrence
	Samples []map[string]string // fields of each occurrence (capped)
	Entries []journal.Entry     // raw lines behind it (capped)
	Starts  []int               // where each occurrence begins in Entries
}

const (
	maxSamples = 50
	maxEntries = 2000
)

// Engine feeds entries through a rule set, one at a time, and collects events.
type Engine struct {
	set    *Set
	events []*Event
	hidden map[*Rule]int
	group  *openGroup
}

type openGroup struct {
	rule    *Rule
	fields  map[string]string
	entries []journal.Entry
}

// NewEngine starts an empty analysis with a rule set.
func NewEngine(s *Set) *Engine {
	return &Engine{set: s, hidden: map[*Rule]int{}}
}

// Feed processes one entry and reports whether a hidden rule took it, or another rule used it.
func (e *Engine) Feed(en journal.Entry) (hidden, matched bool) {
	if g := e.group; g != nil {
		if e.feedGroup(g, en) {
			return false, true
		}
	}
	for _, r := range e.set.Rules {
		if !r.applies(en.Source, en.Priority) {
			continue
		}
		if r.Level == Hidden {
			if matches(r.take, en.Message) {
				e.hidden[r]++
				return true, false
			}
			continue
		}
		f := find(r.take, en.Message)
		if f == nil {
			continue
		}
		f["source"] = en.Source
		if len(r.until) == 0 {
			e.emit(r, f, []journal.Entry{en})
			return false, true
		}
		if e.group != nil { // a new block starts while another is open: close that one first
			e.closeGroup()
		}
		e.group = &openGroup{rule: r, fields: f, entries: []journal.Entry{en}}
		return false, true
	}
	return false, false
}

// feedGroup adds the entry to the open group if it belongs to it, and closes the group on its
// end line or when it grows too long. It returns true when the entry was consumed.
func (e *Engine) feedGroup(g *openGroup, en journal.Entry) bool {
	r := g.rule
	if len(g.entries) >= r.maxLines || en.Time.Sub(g.entries[0].Time) > maxBlock {
		e.closeGroup()
		return false
	}
	if r.Source != "" && en.Source != r.Source {
		return false // an unrelated line in the middle of the group: other rules can have it
	}
	if matches(r.take, en.Message) { // the next occurrence starts before this one ended
		e.closeGroup()
		return false
	}
	g.entries = append(g.entries, en)
	if f := find(r.until, en.Message); f != nil {
		maps.Copy(g.fields, f)
		e.closeGroup()
	}
	return true
}

func (e *Engine) closeGroup() {
	g := e.group
	e.group = nil
	e.emit(g.rule, g.fields, g.entries)
}

// emit records an occurrence, merging it into the previous event of the same rule with the same
// title (any title, when the rule has a merge_title) if it falls inside the window.
func (e *Engine) emit(r *Rule, f map[string]string, entries []journal.Entry) {
	title := fill(r.Title, f)
	first, last := entries[0].Time, entries[len(entries)-1].Time
	for _, ev := range slices.Backward(e.events) {
		if ev.Rule != r || (r.MergeTitle == "" && ev.Title != title) {
			continue
		}
		if r.window > 0 && first.Sub(ev.Last) > r.window {
			break
		}
		ev.Count++
		ev.Last = last
		if len(ev.Samples) < maxSamples {
			ev.Samples = append(ev.Samples, f)
		}
		if room := maxEntries - len(ev.Entries); room > 0 {
			ev.Starts = append(ev.Starts, len(ev.Entries))
			ev.Entries = append(ev.Entries, entries[:min(room, len(entries))]...)
		}
		return
	}
	e.events = append(e.events, &Event{
		Rule: r, First: first, Last: last, Count: 1, Title: title,
		Fields: f, Samples: []map[string]string{f}, Entries: entries, Starts: []int{0},
	})
}

// HiddenCount is how many lines a hidden rule took.
type HiddenCount struct {
	Rule  *Rule
	Count int
}

// Finish closes any open group and returns the events in time order, and the counts of hidden
// lines in rule order.
func (e *Engine) Finish() ([]*Event, []HiddenCount) {
	if e.group != nil {
		e.closeGroup()
	}
	for _, ev := range e.events {
		if ev.Count > 1 && ev.Rule.MergeTitle != "" {
			f := map[string]string{"count": strconv.Itoa(ev.Count)}
			maps.Copy(f, ev.Fields)
			ev.Title = fill(ev.Rule.MergeTitle, f)
		}
	}
	var hidden []HiddenCount
	for _, r := range e.set.Rules {
		if n := e.hidden[r]; n > 0 {
			hidden = append(hidden, HiddenCount{r, n})
		}
	}
	return e.events, hidden
}

// Note is the rule's short note with this event's fields filled in, or "".
func (ev *Event) Note() string { return fill(ev.Rule.Note, ev.Fields) }
