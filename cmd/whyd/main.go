// whyd finds out why the last session stopped.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"

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
  whyd [-b N] [--file PATH]          the session in the interface (as text when the output
                                     is not a terminal)
  whyd report [-b N] [--file PATH]   the session as a Markdown report, redacted, to paste
  whyd rules [FILE|DIR...]           the rules in use, checked; with files, check those

  -b N         the session, as in journalctl -b: -1 the previous one (default), 0 this one,
               1 the oldest
  --file PATH  a session recorded on another machine, with
               journalctl -b -1 -o json --all > PATH (.gz allowed, - for standard input)
  --version    the version of whyd

examples:
  whyd -b -3                   three sessions ago
  whyd report > report.md      the report of the previous session, in a file
  whyd rules mine.toml         check a rule file before putting it in ~/.config/whyd/rules

Rules: built in, plus any *.toml in ~/.config/whyd/rules/ (yours first; same id: changes it).
Exit status: 0 done, 1 an error or a problem in the rules, 2 a wrong command line.
`

func main() {
	if info, ok := debug.ReadBuildInfo(); ok && version == "dev" && releaseTag.MatchString(info.Main.Version) {
		version = strings.TrimPrefix(info.Main.Version, "v") // go install …@v0.1.0
	}
	report.Version = version

	o, err := parseArgs(os.Args[1:])
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "whyd: %v (see whyd --help)\n", err)
		os.Exit(2)
	case o.help:
		fmt.Print(usage)
		return
	case o.version:
		fmt.Println("whyd", version)
		return
	}
	ctx := context.Background()
	switch o.cmd {
	case "":
		err = runOpen(ctx, o.boot, o.file)
	case "report":
		err = runReport(ctx, o.boot, o.file)
	case "rules":
		err = runRules(o.paths)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "whyd:", err)
		os.Exit(1)
	}
}

// options are what the command line asks for.
type options struct {
	cmd     string   // "" (the interface), "report" or "rules"
	boot    int      // -b
	file    string   // --file
	paths   []string // the rule files and directories to check, for rules
	help    bool
	version bool
}

// parseArgs reads the command line. The command may come before or after the options:
// "whyd report -b -2", "whyd -b -2 report".
func parseArgs(args []string) (options, error) {
	o := options{boot: -1}
	fl := flag.NewFlagSet("whyd", flag.ContinueOnError)
	fl.SetOutput(io.Discard) // main writes the error, once
	fl.IntVar(&o.boot, "b", -1, "")
	fl.StringVar(&o.file, "file", "", "")
	fl.BoolVar(&o.version, "version", false, "")
	var words []string
	for {
		if err := fl.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				o.help = true
				return o, nil
			}
			return o, optionError(err)
		}
		if fl.NArg() == 0 {
			break
		}
		words = append(words, fl.Arg(0))
		args = fl.Args()[1:]
	}
	if len(words) > 0 {
		o.cmd, words = words[0], words[1:]
	}
	switch o.cmd {
	case "", "report":
		if len(words) > 0 {
			return o, fmt.Errorf("unexpected %q", words[0])
		}
	case "rules":
		o.paths = words
		var err error
		fl.Visit(func(f *flag.Flag) {
			if f.Name == "b" || f.Name == "file" {
				err = fmt.Errorf("%s is for a session, not for rules", dashes("-"+f.Name))
			}
		})
		if err != nil {
			return o, err
		}
	default:
		return o, fmt.Errorf("unknown command %q: the commands are report and rules", o.cmd)
	}
	return o, nil
}

// optionError says in plain words what the flag package found wrong.
func optionError(err error) error {
	msg := err.Error()
	if name, ok := strings.CutPrefix(msg, "flag provided but not defined: "); ok {
		return fmt.Errorf("unknown option %s", dashes(name))
	}
	if name, ok := strings.CutPrefix(msg, "flag needs an argument: "); ok {
		return fmt.Errorf("%s needs a value", dashes(name))
	}
	if strings.Contains(msg, "for flag -b") {
		return errors.New("-b needs a number, as in -b -1")
	}
	return err
}

// dashes writes an option the way it is documented: -b, --file.
func dashes(name string) string {
	if len(name) > 2 && !strings.HasPrefix(name, "--") {
		return "-" + name
	}
	return name
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
	return nil, fmt.Errorf("no session %d in the journal (journalctl --list-boots lists them)", index)
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

func runOpen(ctx context.Context, index int, file string) error {
	s, err := newSession(ctx, file == "")
	if err != nil {
		return err
	}
	res, err := s.result(ctx, index, file)
	if err != nil {
		return err
	}
	if !isTerminal() { // a pipe or a file: the summary as text
		return report.Text(os.Stdout, res)
	}
	src := ui.Source{Skipped: len(s.rules.Problems), Boots: s.boots}
	if file == "" {
		src.Analyse = func(i int) (*analyze.Result, error) { return s.analyse(ctx, i) }
		src.Summarize = func(i int) (analyze.Summary, error) { return s.summary(ctx, i) }
	}
	err = ui.Run(res, src)
	s.saveCache()
	return err
}

func runReport(ctx context.Context, index int, file string) error {
	s, err := newSession(ctx, file == "")
	if err != nil {
		return err
	}
	res, err := s.result(ctx, index, file)
	if err != nil {
		return err
	}
	return report.Markdown(os.Stdout, res, report.NewRedactor(res))
}
