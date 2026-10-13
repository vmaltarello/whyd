// Package rules turns journal entries into events a human understands, using rules written as
// TOML data (format: docs/RULES.md).
package rules

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"reflect"
	"regexp"
	"regexp/syntax"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/vmaltarello/whyd/internal/journal"
	builtin "github.com/vmaltarello/whyd/rules"
)

// Severity of an event, from least to most serious.
type Severity int

const (
	Noise Severity = iota
	Info
	Warning
	Critical
)

func (s Severity) String() string {
	return [...]string{"noise", "info", "warning", "critical"}[s]
}

func parseSeverity(s string) (Severity, error) {
	switch s {
	case "noise":
		return Noise, nil
	case "info":
		return Info, nil
	case "warning":
		return Warning, nil
	case "critical":
		return Critical, nil
	}
	return 0, fmt.Errorf("unknown severity %q", s)
}

// Rule recognises one kind of problem. Fields mirror the TOML format.
type Rule struct {
	ID          string    `toml:"id"`
	Kind        string    `toml:"kind"` // match | group | noise
	Category    string    `toml:"category"`
	SeverityStr string    `toml:"severity"`
	Title       string    `toml:"title"`
	Source      string    `toml:"source"`   // exact SYSLOG_IDENTIFIER / _COMM
	Priority    string    `toml:"priority"` // "<=4"
	Message     string    `toml:"message"`  // match, noise
	Start       string    `toml:"start"`    // group
	End         string    `toml:"end"`      // group
	MaxLines    int       `toml:"max_lines"`
	MaxSeconds  int       `toml:"max_seconds"`
	Extract     []string  `toml:"extract"`
	Merge       string    `toml:"merge"`         // "" = same title, "rule" = any occurrence of the rule
	MergeWindow string    `toml:"merge_window"`  // "5m"; empty = the whole boot
	MergeTitle  string    `toml:"merge_title"`   // title when merged, may use {count}
	Timeline    bool      `toml:"timeline_only"` // context, not a finding: shown in the timeline only
	Note        string    `toml:"note"`          // a few words shown under the title, when nothing better is known
	Explain     string    `toml:"explain"`
	Examples    []Example `toml:"example"`

	Origin    string // "built-in" or the file it came from
	Overrides string // the origin of the rule this one replaced, if any

	Severity    Severity
	maxPriority int // -1 = any
	window      time.Duration
	message     *regexp.Regexp
	start, end  *regexp.Regexp
	// text each pattern needs to match, checked with strings.Contains before the regexp runs:
	// most lines are ruled out at that point, which makes the rules several times faster
	messageLit, startLit, endLit []string
	extract                      []*regexp.Regexp
	file                         string
}

// Example is a self-test line: Match must be recognised, NoMatch must not.
type Example struct {
	Match   string `toml:"match"`
	NoMatch string `toml:"nomatch"`
}

type ruleFile struct {
	Rules []*Rule `toml:"rule"`
}

// Set is an ordered collection of rules: on a line, the first rule that matches wins.
type Set struct {
	Rules    []*Rule
	Problems []string // user rules that were skipped, and why
	Hash     string   // of every file read: changes when any rule changes
	byID     map[string]*Rule
}

// Source is a place rules come from. Rules from a strict source must all be valid (the built-in
// ones: an error there is a bug); in other sources a broken file or rule is skipped and noted in
// Set.Problems, so one mistake in a user's file doesn't stop whyd.
type Source struct {
	Name   string // "built-in" or a directory
	FS     fs.FS
	Strict bool
}

// Load reads every *.toml file of each source, in order. A rule whose ID already exists replaces
// the earlier one, so user rules (loaded last) override the built-in ones.
func Load(sources ...Source) (*Set, error) {
	s := &Set{byID: map[string]*Rule{}}
	h := sha256.New()
	defer func() { s.Hash = hex.EncodeToString(h.Sum(nil)[:8]) }()
	for _, src := range sources {
		names, err := fs.Glob(src.FS, "*.toml")
		if err != nil {
			return nil, err
		}
		sort.Strings(names)
		for _, name := range names {
			origin := name
			if !src.Strict {
				origin = path.Join(src.Name, name)
			}
			fail := func(err error) error {
				if src.Strict {
					return err
				}
				s.Problems = append(s.Problems, err.Error())
				return nil
			}
			data, err := fs.ReadFile(src.FS, name)
			if err != nil {
				if err := fail(err); err != nil {
					return nil, err
				}
				continue
			}
			_, _ = fmt.Fprintf(h, "%s\x00%s\x00", src.Name+"/"+name, data)
			var f ruleFile
			var raw struct {
				Rules []map[string]any `toml:"rule"`
			}
			md, err := toml.Decode(string(data), &raw)
			if err == nil {
				_, err = toml.Decode(string(data), &f)
			}
			if err == nil {
				for _, k := range md.Undecoded() {
					if k[0] != "rule" {
						err = fmt.Errorf("unknown table or field %q: rules go in [[rule]] blocks", k.String())
						break
					}
				}
			}
			if err == nil && len(f.Rules) == 0 {
				err = errors.New("no [[rule]] block")
			}
			if err != nil {
				if err := fail(fmt.Errorf("%s: %w", origin, err)); err != nil {
					return nil, err
				}
				continue
			}
			for i, r := range f.Rules {
				if err := checkKeys(raw.Rules[i]); err != nil {
					name := r.ID
					if name == "" {
						name = fmt.Sprintf("number %d", i+1)
					}
					if err := fail(fmt.Errorf("%s: rule %q skipped: %w", origin, name, err)); err != nil {
						return nil, err
					}
					continue
				}
				r.file = origin
				r.Origin = "built-in"
				if !src.Strict {
					r.Origin = origin
				}
				if err := r.compile(); err != nil {
					if err := fail(fmt.Errorf("%s: rule %q skipped: %w", origin, r.ID, err)); err != nil {
						return nil, err
					}
					continue
				}
				if old, ok := s.byID[r.ID]; ok {
					r.Overrides = old.Origin
					*old = *r // keep its position, take the new definition
					continue
				}
				s.byID[r.ID] = r
				s.Rules = append(s.Rules, r)
			}
		}
	}
	return s, nil
}

// Sources returns the sources the rules name, in order, each once.
func (s *Set) Sources() []string {
	var out []string
	for _, r := range s.Rules {
		if r.Source != "" && !slices.Contains(out, r.Source) {
			out = append(out, r.Source)
		}
	}
	return out
}

// Rule returns the rule with that ID, or nil.
func (s *Set) Rule(id string) *Rule { return s.byID[id] }

func (r *Rule) compile() error {
	if r.ID == "" {
		return errors.New("missing id")
	}
	if r.Title == "" {
		return errors.New("missing title")
	}
	// fields that would have no effect are mistakes: say so instead of ignoring them
	unused := func(field string, set bool, why string) error {
		if set {
			return fmt.Errorf("%s has no effect %s", field, why)
		}
		return nil
	}
	var err error
	switch r.Kind {
	case "match", "noise":
		err = cmp.Or(
			unused("start", r.Start != "", "in a "+r.Kind+" rule (it is for kind = \"group\")"),
			unused("end", r.End != "", "in a "+r.Kind+" rule (it is for kind = \"group\")"),
			unused("extract", len(r.Extract) > 0, "in a "+r.Kind+" rule: name the groups in message instead"),
			unused("max_lines", r.MaxLines != 0, "in a "+r.Kind+" rule (it is for kind = \"group\")"),
			unused("max_seconds", r.MaxSeconds != 0, "in a "+r.Kind+" rule (it is for kind = \"group\")"))
	case "group":
		err = unused("message", r.Message != "", "in a group rule: use start and end")
		if err == nil && (r.MaxLines < 0 || r.MaxSeconds < 0) {
			err = errors.New("max_lines and max_seconds can't be negative")
		}
	case "":
		return errors.New(`missing kind: "match", "group" or "noise"`)
	default:
		return fmt.Errorf(`kind must be "match", "group" or "noise", got %q`, r.Kind)
	}
	if err != nil {
		return err
	}
	if r.Kind == "noise" { // hidden and counted: nothing is shown about each occurrence
		if r.SeverityStr == "" {
			r.SeverityStr = "noise"
		}
		why := "in a noise rule: its lines are only counted"
		err = cmp.Or(
			unused("merge", r.Merge != "", why), unused("merge_window", r.MergeWindow != "", why),
			unused("merge_title", r.MergeTitle != "", why), unused("timeline_only", r.Timeline, why),
			unused("note", r.Note != "", why), unused("explain", r.Explain != "", why))
		if err == nil && r.SeverityStr != "noise" {
			err = fmt.Errorf(`severity must be "noise" in a noise rule (or left out), got %q`, r.SeverityStr)
		}
		if err != nil {
			return err
		}
	} else {
		if r.SeverityStr == "noise" {
			return errors.New(`severity "noise" is for kind = "noise": to hide lines, make it a noise rule`)
		}
		if r.Category == "" {
			return errors.New(`missing category (a word such as "gpu" or "disk")`)
		}
	}
	sev, err := parseSeverity(r.SeverityStr)
	if err != nil {
		return err
	}
	r.Severity = sev
	r.maxPriority = -1
	if r.Priority != "" {
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(r.Priority, "<=")))
		if err != nil || !strings.HasPrefix(r.Priority, "<=") || n < 0 || n > 7 {
			return fmt.Errorf(`priority must be "<=N" with N from 0 to 7, such as "<=4" for warnings and worse, got %q`, r.Priority)
		}
		r.maxPriority = n
	}
	switch r.Merge {
	case "", "rule":
	default:
		return fmt.Errorf(`merge must be "rule" or left out, got %q`, r.Merge)
	}
	if r.MergeWindow != "" {
		if r.window, err = time.ParseDuration(r.MergeWindow); err != nil || r.window <= 0 {
			return fmt.Errorf(`merge_window must be a duration such as "30s", "10m" or "1h", got %q`, r.MergeWindow)
		}
	}
	compile := func(name, expr string) (*regexp.Regexp, error) {
		if expr == "" {
			return nil, fmt.Errorf("missing %s", name)
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return re, nil
	}
	if r.Kind == "group" {
		if r.start, err = compile("start", r.Start); err == nil {
			r.end, err = compile("end", r.End)
		}
		if r.MaxLines == 0 {
			r.MaxLines = 300
		}
		if r.MaxSeconds == 0 {
			r.MaxSeconds = 60
		}
	} else {
		r.message, err = compile("message", r.Message)
	}
	if err != nil {
		return err
	}
	r.messageLit, r.startLit, r.endLit = required(r.Message), required(r.Start), required(r.End)
	for _, x := range r.Extract {
		re, err := compile("extract", x)
		if err != nil {
			return err
		}
		r.extract = append(r.extract, re)
	}
	if err := r.checkTemplates(); err != nil {
		return err
	}
	for _, ex := range r.Examples {
		if (ex.Match == "") == (ex.NoMatch == "") {
			return errors.New("each [[rule.example]] needs either match or nomatch")
		}
	}
	return nil
}

// checkTemplates makes sure every {name} in title, note and merge_title is a field the rule
// fills: a named group of its patterns, {source}, and {count} in merge_title.
func (r *Rule) checkTemplates() error {
	names := map[string]bool{"source": true}
	for _, re := range append([]*regexp.Regexp{r.message, r.start, r.end}, r.extract...) {
		if re != nil {
			for _, n := range re.SubexpNames() {
				names[n] = n != ""
			}
		}
	}
	for _, t := range []struct{ field, text string }{{"title", r.Title}, {"note", r.Note}, {"merge_title", r.MergeTitle}} {
		for _, m := range placeholder.FindAllStringSubmatch(t.text, -1) {
			if !names[m[1]] && (m[1] != "count" || t.field != "merge_title") {
				if hint := suggest(m[1], names); hint != "" {
					return fmt.Errorf("%s uses {%s}, but no pattern has a group named %s%s", t.field, m[1], m[1], hint)
				}
				return fmt.Errorf("%s uses {%s}, but no pattern has a group named %s: write (?P<%s>...) in the pattern", t.field, m[1], m[1], m[1])
			}
		}
	}
	return nil
}

// checkKeys reports the fields of a [[rule]] (or of its examples) that whyd doesn't know:
// most often a typo, which would otherwise silently change what the rule does.
func checkKeys(rule map[string]any) error {
	var bad []string
	for _, k := range slices.Sorted(maps.Keys(rule)) {
		if !ruleFields[k] {
			bad = append(bad, fmt.Sprintf("%q%s", k, suggest(k, ruleFields)))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("unknown field %s", strings.Join(bad, "; unknown field "))
	}
	exs, _ := rule["example"].([]map[string]any)
	for _, ex := range exs {
		for ek := range ex {
			if ek != "match" && ek != "nomatch" {
				return fmt.Errorf("unknown field %q in [[rule.example]]: only match and nomatch", ek)
			}
		}
	}
	return nil
}

// ruleFields are the TOML fields of a rule, from the struct tags.
var ruleFields = func() map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeFor[Rule]()
	for field := range t.Fields() {
		if tag := field.Tag.Get("toml"); tag != "" {
			out[tag] = true
		}
	}
	return out
}()

// suggest names the known name closest to a misspelt one: ", did you mean "source"?".
func suggest(k string, known map[string]bool) string {
	best, dist := "", 3 // at most two edits away
	for f, ok := range known {
		if !ok {
			continue
		}
		if d := editDistance(k, f); d < dist || d == dist && f < best {
			best, dist = f, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(", did you mean %q?", best)
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// applies tells whether the rule looks at this kind of line at all.
func (r *Rule) applies(source string, priority int) bool {
	if r.Source != "" && r.Source != source {
		return false
	}
	if r.maxPriority >= 0 && (priority < 0 || priority > r.maxPriority) {
		return false
	}
	return true
}

// Test runs the rule's examples and returns a description of each failure.
func (r *Rule) Test() []string {
	var fails []string
	first := r.message
	if r.Kind == "group" {
		first = r.start
	}
	for _, ex := range r.Examples {
		if ex.Match != "" && !first.MatchString(ex.Match) {
			fails = append(fails, fmt.Sprintf("%s (%s): should match %q", r.ID, r.file, ex.Match))
		}
		if ex.NoMatch != "" && first.MatchString(ex.NoMatch) {
			fails = append(fails, fmt.Sprintf("%s (%s): should not match %q", r.ID, r.file, ex.NoMatch))
		}
	}
	return fails
}

// Test runs the examples of every rule, and checks that each line a rule must match reaches it:
// a wider rule earlier in the set would take the line first and this one would never fire.
func (s *Set) Test() []string {
	var fails []string
	for _, r := range s.Rules {
		own := r.Test()
		fails = append(fails, own...)
		if len(own) > 0 {
			continue
		}
		for _, ex := range r.Examples {
			if ex.Match == "" {
				continue
			}
			if by := s.taker(r, ex.Match); by != r {
				name := "no rule"
				if by != nil {
					name = by.ID + " (" + by.file + ")"
				}
				fails = append(fails, fmt.Sprintf("%s (%s): %q is taken by %s first", r.ID, r.file, ex.Match, name))
			}
		}
	}
	return fails
}

// taker is the rule that recognises the line when it comes from r's source and priority.
func (s *Set) taker(r *Rule, line string) *Rule {
	prio := 3
	if r.maxPriority >= 0 {
		prio = r.maxPriority
	}
	e := NewEngine(s)
	e.Feed(journal.Entry{Source: r.Source, Priority: prio, Message: line})
	events, noise := e.Finish()
	switch {
	case len(noise) > 0:
		return noise[0].Rule
	case len(events) > 0:
		return events[0].Rule
	}
	return nil
}

// required returns pieces of text such that every match of the regular expression contains at
// least one of them, or nil when there are none simple to find (case-insensitive parts, a
// branch with no fixed text).
func required(expr string) []string {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		return nil
	}
	return literalsIn(re.Simplify())
}

func literalsIn(re *syntax.Regexp) []string {
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase == 0 {
			return []string{string(re.Rune)}
		}
	case syntax.OpCapture, syntax.OpPlus:
		return literalsIn(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min > 0 {
			return literalsIn(re.Sub[0])
		}
	case syntax.OpConcat: // any part will do: keep the one whose shortest piece is longest
		var best []string
		for _, sub := range re.Sub {
			if l := literalsIn(sub); l != nil && (best == nil || shortest(l) > shortest(best)) {
				best = l
			}
		}
		return best
	case syntax.OpAlternate: // every branch must have one
		var all []string
		for _, sub := range re.Sub {
			l := literalsIn(sub)
			if l == nil {
				return nil
			}
			all = append(all, l...)
		}
		return all
	}
	return nil
}

func shortest(l []string) int {
	n := len(l[0])
	for _, s := range l[1:] {
		n = min(n, len(s))
	}
	return n
}

// may tells whether s can match a pattern that needs one of lits (nil: anything can match).
func may(lits []string, s string) bool {
	if lits == nil {
		return true
	}
	for _, l := range lits {
		if strings.Contains(s, l) {
			return true
		}
	}
	return false
}

// fields returns the named groups of re matched in s, or nil when it doesn't match.
func fields(re *regexp.Regexp, s string) map[string]string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	out := map[string]string{}
	for i, name := range re.SubexpNames() {
		if name != "" && m[i] != "" {
			out[name] = m[i]
		}
	}
	return out
}

var placeholder = regexp.MustCompile(`\{(\w+)\}`)

// fill replaces {name} in a template with the matching field (left as is when missing).
func fill(tmpl string, f map[string]string) string {
	return placeholder.ReplaceAllStringFunc(tmpl, func(p string) string {
		if v, ok := f[p[1:len(p)-1]]; ok {
			return v
		}
		return p
	})
}

// LoadDefault loads the built-in rules and then every existing user directory, in order.
func LoadDefault(userDirs ...string) (*Set, error) {
	sources := []Source{{Name: "built-in", FS: builtin.Builtin, Strict: true}}
	for _, d := range userDirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			sources = append(sources, Source{Name: d, FS: os.DirFS(d)})
		}
	}
	return Load(sources...)
}
