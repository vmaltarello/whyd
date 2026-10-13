// whyd finds out why the last session stopped.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vmaltarello/whyd/internal/analyze"
	"github.com/vmaltarello/whyd/internal/cache"
	"github.com/vmaltarello/whyd/internal/journal"
	"github.com/vmaltarello/whyd/internal/report"
	"github.com/vmaltarello/whyd/internal/rules"
	"github.com/vmaltarello/whyd/internal/ui"
)

// version is set by the release build; go install takes it from the module version.
var version = "dev"

var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

const usage = `whyd: find out why your last session stopped

usage:
  whyd [-b N] [--file PATH] [--text]   open the previous boot (text when not in a terminal)
  whyd list [-u] [--days N | --all]    the sessions of the last 30 days and how they ended
                                       (-u: only the bad ones)
  whyd report [-b N] [--file PATH] [--raw]
                                       Markdown summary, redacted, ready to paste
  whyd rules                           the rules in use, and where each one comes from
  whyd rules test [file|dir...]        check rules against their examples (default: your rules)
  whyd --version

  -b N         boot, as in journalctl -b: 0 this one, -1 the previous (default), 1 the oldest
  --file PATH  analyse one recorded boot (journalctl -b N -o json --all, .gz allowed, - for stdin)
  --text       print the summary instead of opening the interface
  --raw        don't redact the report
  --days N     how far back whyd list and the interface go at first (default 30)
  --all        every session in the journal

Rules: built in, plus any *.toml in ~/.config/whyd/rules/ (same id = replace).
A broken user rule is skipped with a message, it never stops whyd.
`

func main() {
	if info, ok := debug.ReadBuildInfo(); ok && version == "dev" && releaseTag.MatchString(info.Main.Version) {
		version = strings.TrimPrefix(info.Main.Version, "v") // go install …@v0.1.0
	}
	report.Version = version

	fl := flag.NewFlagSet("whyd", flag.ContinueOnError)
	fl.Usage = func() { fmt.Print(usage) }
	boot := fl.Int("b", -1, "")
	file := fl.String("file", "", "")
	text := fl.Bool("text", false, "")
	raw := fl.Bool("raw", false, "")
	unclean := fl.Bool("u", false, "")
	showVersion := fl.Bool("version", false, "")
	days := fl.Int("days", int(ui.Recent.Hours()/24), "")
	all := fl.Bool("all", false, "")
	// the command may come before or after the flags: "whyd report -b -2", "whyd -b -2 report"
	cmd, rest := "", []string(nil)
	args := os.Args[1:]
	for {
		if err := fl.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return
			}
			os.Exit(2)
		}
		if fl.NArg() == 0 {
			break
		}
		if cmd == "" {
			cmd = fl.Arg(0)
		} else {
			rest = append(rest, fl.Arg(0))
		}
		args = fl.Args()[1:]
	}
	if *showVersion {
		fmt.Println("whyd", version)
		return
	}

	if cmd != "rules" && len(rest) > 0 {
		fmt.Fprintf(os.Stderr, "whyd: unexpected %q\n", rest[0])
		os.Exit(2)
	}

	ctx := context.Background()
	var err error
	switch cmd {
	case "":
		err = runOpen(ctx, *boot, *file, *text || !isTerminal(), since(*days, *all))
	case "list":
		err = runList(ctx, *unclean, since(*days, *all))
	case "report":
		err = runReport(ctx, *boot, *file, *raw)
	case "rules":
		err = runRules(rest)
	case "help":
		fmt.Print(usage)
	case "version":
		fmt.Println("whyd", version)
	default:
		fmt.Fprintf(os.Stderr, "whyd: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "whyd:", err)
		os.Exit(1)
	}
}

// since is when the sessions shown start: --days ago, or the beginning of time with --all.
func since(days int, all bool) time.Time {
	if all {
		return time.Unix(0, 0)
	}
	return time.Now().AddDate(0, 0, -days)
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// userRuleDirs are where whyd looks for the user's own rules.
func userRuleDirs() []string {
	if dir, err := os.UserConfigDir(); err == nil {
		return []string{filepath.Join(dir, "whyd", "rules")}
	}
	return nil
}

// loadRules reads the built-in rules and the user's own, reporting user rules it had to skip.
func loadRules() (*rules.Set, error) {
	set, err := rules.LoadDefault(userRuleDirs()...)
	if err != nil {
		return nil, err
	}
	for _, p := range set.Problems {
		fmt.Fprintln(os.Stderr, "whyd: "+p)
	}
	return set, nil
}

// session is everything needed to analyse boots: the rule set, the boot list and the cache of
// summaries.
type session struct {
	rules *rules.Set
	needs *journal.Selection // the lines of a boot the analysis reads
	boots []journal.Boot     // oldest first
	cache *cache.Cache
}

func newSession(ctx context.Context, withBoots bool) (*session, error) {
	if withBoots && !journal.SystemReadable() {
		return nil, errors.New("you can only read your own messages, not the system journal.\n" +
			"Run whyd with sudo, or add yourself to the systemd-journal group and log in again:\n" +
			"  sudo usermod -aG systemd-journal $USER")
	}
	set, err := loadRules()
	if err != nil {
		return nil, err
	}
	s := &session{rules: set, needs: analyze.Needs(set), cache: cache.Open(cacheKey(set))}
	if withBoots {
		if s.boots, err = journal.ListBoots(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// analyse runs the analysis of the boot with that index. A positive index counts from the
// oldest boot, like journalctl -b 1.
func (s *session) analyse(ctx context.Context, index int) (*analyze.Result, error) {
	if index > 0 && index <= len(s.boots) {
		index = s.boots[index-1].Index
	}
	for i, b := range s.boots {
		if b.Index != index {
			continue
		}
		var next *journal.Boot
		if i+1 < len(s.boots) {
			next = &s.boots[i+1]
		}
		return analyze.Run(analyze.Input{
			Boot: b, Current: b.Index == 0, Next: next, Rules: s.rules,
			Read: func(fn func(journal.Entry) error) error { return journal.ReadBoot(ctx, b, s.needs, fn) },
		})
	}
	if index == -1 && len(s.boots) == 1 {
		return nil, errors.New("this is the only boot in the journal: nothing before it to look at")
	}
	return nil, fmt.Errorf("no boot %d in the journal (see whyd list)", index)
}

// summary returns how a boot ended, from the cache for ended boots already seen.
func (s *session) summary(ctx context.Context, index int) (analyze.Summary, error) {
	for _, b := range s.boots {
		if b.Index != index || b.Index == 0 { // the running boot changes: never cached
			continue
		}
		if sum, ok := s.cache.Get(b.ID, b.Last); ok {
			return sum, nil
		}
		res, err := s.analyse(ctx, index)
		if err != nil {
			return analyze.Summary{}, err
		}
		sum := res.Summarize()
		s.cache.Put(b.ID, sum)
		return sum, nil
	}
	res, err := s.analyse(ctx, index)
	if err != nil {
		return analyze.Summary{}, err
	}
	return res.Summarize(), nil
}

// saveCache writes the cache, keeping only the boots still in the journal.
func (s *session) saveCache() {
	ids := make([]string, len(s.boots))
	for i, b := range s.boots {
		ids[i] = b.ID
	}
	_ = s.cache.Save(ids) // only a speed-up: nothing to tell the user if it fails
}

// cacheKey is what the cached summaries depend on: whyd itself and the rules.
func cacheKey(set *rules.Set) string {
	key := []string{version, set.Hash}
	if info, ok := debug.ReadBuildInfo(); ok && version == "dev" { // a development build: its version and commit
		key = append(key, info.Main.Version) // go install …@latest: a pseudo-version, no commit
		for _, kv := range info.Settings {
			if kv.Key == "vcs.revision" || kv.Key == "vcs.time" {
				key = append(key, kv.Value)
			}
		}
	}
	return strings.Join(key, " ")
}

// analyseFile runs the analysis of a recorded journal.
func (s *session) analyseFile(path string) (*analyze.Result, error) {
	return analyze.Run(analyze.Input{
		Rules: s.rules,
		Read:  func(fn func(journal.Entry) error) error { return journal.ReadFile(path, fn) },
	})
}

func (s *session) result(ctx context.Context, index int, file string) (*analyze.Result, error) {
	if file != "" {
		return s.analyseFile(file)
	}
	return s.analyse(ctx, index)
}

func runOpen(ctx context.Context, index int, file string, asText bool, from time.Time) error {
	s, err := newSession(ctx, file == "")
	if err != nil {
		return err
	}
	res, err := s.result(ctx, index, file)
	if err != nil {
		return err
	}
	if asText {
		return report.Text(os.Stdout, res)
	}
	src := ui.Source{Skipped: len(s.rules.Problems), Since: from, Boots: s.boots}
	if file == "" {
		src.Analyse = func(i int) (*analyze.Result, error) { return s.analyse(ctx, i) }
		src.Summarize = func(i int) (analyze.Summary, error) { return s.summary(ctx, i) }
	}
	err = ui.Run(res, src)
	s.saveCache()
	return err
}

func runReport(ctx context.Context, index int, file string, raw bool) error {
	s, err := newSession(ctx, file == "")
	if err != nil {
		return err
	}
	res, err := s.result(ctx, index, file)
	if err != nil {
		return err
	}
	var red *report.Redactor
	if !raw {
		red = report.NewRedactor(res)
	}
	return report.Markdown(os.Stdout, res, red)
}

func runList(ctx context.Context, onlyUnclean bool, since time.Time) error {
	s, err := newSession(ctx, true)
	if err != nil {
		return err
	}
	all := s.boots
	for len(s.boots) > 1 && s.boots[0].Last.Before(since) { // oldest first: drop the old ones
		s.boots = s.boots[1:]
	}
	older := len(all) - len(s.boots)
	sums := s.summaries(ctx)
	s.saveCache()
	unclean := 0
	for i, b := range slices.Backward(s.boots) { // newest first
		r := sums[i]
		if r.err != nil {
			fmt.Printf("%4d  %s  error: %v\n", b.Index, b.First.Format("Mon 2 Jan 15:04"), r.err)
			continue
		}
		if r.sum.Bad() {
			unclean++
		}
		if onlyUnclean && !r.sum.Bad() {
			continue
		}
		fmt.Printf("%4d  %-17s %7s  %-13s %s\n", b.Index, report.When(b.First),
			analyze.Duration(b.Duration()), r.sum.Kind, r.sum.Worst)
	}
	fmt.Printf("\n%d of %d sessions didn't shut down properly", unclean, len(s.boots))
	if older > 0 {
		fmt.Printf(" (%d older ones not shown: --days N or --all)", older)
	}
	fmt.Println()
	return nil
}

type listResult struct {
	sum analyze.Summary
	err error
}

// summaries gets the summary of every boot, a few at a time (each new one is a journalctl run).
func (s *session) summaries(ctx context.Context) []listResult {
	out := make([]listResult, len(s.boots))
	sem := make(chan struct{}, max(2, runtime.NumCPU()/2))
	var wg sync.WaitGroup
	for i, b := range s.boots {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			sum, err := s.summary(ctx, b.Index)
			out[i] = listResult{sum, err}
		})
	}
	wg.Wait()
	return out
}
