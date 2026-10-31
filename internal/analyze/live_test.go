package analyze

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/vmaltarello/whyd/internal/journal"
	"github.com/vmaltarello/whyd/internal/rules"
)

// WHYD_LIVE=1 go test -run TestSelection ./internal/analyze checks, on every boot of this
// machine's journal, that reading the selection (Needs) gives what reading the whole boot gives.
func TestSelectionMatchesFullRead(t *testing.T) {
	if os.Getenv("WHYD_LIVE") == "" {
		t.Skip("set WHYD_LIVE=1 to run on this machine's journal")
	}
	set, err := rules.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	boots, err := journal.ListBoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	compared, readFull, readSel := 0, 0, 0
	defer func() {
		t.Logf("%d boots compared, %d lines read instead of %d", compared, readSel, readFull)
	}()
	for i, b := range boots {
		if b.Index == 0 {
			continue // still growing: the two reads would see different lines
		}
		var next *journal.Boot
		if i+1 < len(boots) {
			next = &boots[i+1]
		}
		run := func(sel *journal.Selection) *Result {
			r, err := Run(Input{Boot: b, Next: next, Rules: set, Read: func(fn func(journal.Entry) error) error {
				return journal.ReadBoot(ctx, b, sel, fn)
			}})
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		full, sel := run(nil), run(Needs(set))
		compared, readFull, readSel = compared+1, readFull+full.Lines, readSel+sel.Lines
		if x, y := shown(full), shown(sel); x != y {
			n := 0
			for n < min(len(x), len(y)) && x[n] == y[n] {
				n++
			}
			t.Errorf("boot %d (%d lines read of %d): results differ from\nfull:     %.300s\nselected: %.300s", b.Index, sel.Lines, full.Lines, x[n:], y[n:])
		}
	}
}

// shown is everything whyd shows of a result, as text.
func shown(r *Result) string {
	s := fmt.Sprintf("%v %s %q|%v|%d %d %v|%s %s %q|%d %d|%v|%+v|", r.Verdict.Kind, r.Verdict.Summary,
		r.Verdict.By.Words(true), r.Quiet, r.Suspends, r.Hibernations, r.Asleep, r.Kernel, r.Host, r.Users,
		r.UnknownErr, r.UnknownW, r.HiddenLines, r.OOM)
	s += fmt.Sprintf("%+v|%v|", r.Boot, r.Sleeps)
	for _, ev := range r.Events {
		s += fmt.Sprintf("%s %q %d %v %v %v %d|", ev.Rule.ID, ev.Title, ev.Count, ev.First, ev.Last, ev.Fields, len(ev.Entries))
		for _, e := range ev.Entries {
			s += e.Message
		}
	}
	for _, n := range r.Hidden {
		s += fmt.Sprintf("%s %d|", n.Rule.ID, n.Count)
	}
	for _, g := range r.Unknown {
		s += fmt.Sprintf("%+v|", g)
	}
	for _, l := range r.Tail {
		s += fmt.Sprintf("%v %s %v %v %s|", l.Time, l.Source, l.Hidden, l.Matched, l.Message)
	}
	return s
}
