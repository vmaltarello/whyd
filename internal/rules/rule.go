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
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/vmaltarello/whyd/internal/journal"
	builtin "github.com/vmaltarello/whyd/rules"
)

// Level is what whyd does with the lines of a rule, from least to most visible.
type Level int

const (
	Hidden  Level = iota // hidden and only counted
	Context              // taken and marked in the last minutes, not listed as a problem
	Info                 // listed: context, known and harmless issues
	Warn                 // listed: worth a look
	Err                  // listed: can explain a crash or a freeze by itself
)

var levelNames = [...]string{"hidden", "context", "info", "warn", "err"}

// String is the level as rules write it and whyd shows it: "err", "warn", "info"…
func (l Level) String() string { return levelNames[l] }

// Rule recognises one kind of problem, compiled from its [[rule]] block.
type Rule struct {
	ID         string
	Category   string // the part of the ID before the first dot: "gpu", "memory"…
	Level      Level
	Title      string
	Source     string
	Note       string
	Explain    string
	MergeTitle string   // set: every occurrence joins one problem, titled this way
	Match      []string // example lines the rule must recognise
	NoMatch    []string // example lines it must leave alone

	Origin  string // "built-in" or the file it came from
	Changes string // the origin of the rule this one changes, if any

	maxPriority int           // -1 = any
	window      time.Duration // merge only within this time; 0 = the whole boot
	take        []pattern     // the lines it takes (with until, the first one); none: every line
	until       []pattern     // the last line of a block; none: one line at a time
	maxLines    int
	file        string
}

const (
	maxBlock        = time.Minute // how long a block that never reaches its until line goes on
	defaultMaxLines = 300         // how many lines it takes at most, without max_lines
)

// pattern is one text of a rule, compiled.
type pattern struct {
	re *regexp.Regexp
	// a piece of text every match contains, checked with strings.Contains before the regexp
	// runs: most lines are ruled out at that point, which makes the rules several times faster
	lit string
}

// spec is a [[rule]] block as written.
type spec struct {
	ID          string   `toml:"id"`
	Level       string   `toml:"level"`
	Title       string   `toml:"title"`
	Text        []string `toml:"text"`
	Until       []string `toml:"until"`
	MaxLines    int      `toml:"max_lines"`
	Source      string   `toml:"source"`
	Priority    string   `toml:"priority"`
	MergeTitle  string   `toml:"merge_title"`
	MergeWindow string   `toml:"merge_window"`
	Note        string   `toml:"note"`
	Explain     string   `toml:"explain"`
	Match       []string `toml:"match"`
	NoMatch     []string `toml:"nomatch"`
}

// Set is an ordered collection of rules: on a line, the first rule that matches wins.
type Set struct {
	Rules    []*Rule
	Off      []*Rule  // rules turned off with level = "off": only their ID, Origin and Changes
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

// loaded is a rule read so far, with its fields as written to apply later changes to them.
type loaded struct {
	raw    map[string]any
	rule   *Rule  // nil when turned off
	off    *Rule  // when turned off: who did it
	origin string // where the rule was first defined
}

// Load reads every *.toml file of each source, in order. New rules of the sources that aren't
// strict (the user's) come first, so they see each line before the built-in ones. A rule with
// the ID of an earlier one changes it: the fields it writes replace those, the others stay.
func Load(sources ...Source) (*Set, error) {
	byID := map[string]*loaded{}
	var mine, others []*loaded // new rules of the user, the rest; each in the order read
	var problems []string
	h := sha256.New()
	for _, src := range sources {
		names, err := fs.Glob(src.FS, "*.toml")
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			origin := name
			if !src.Strict {
				origin = path.Join(src.Name, name)
			}
			fail := func(err error) error {
				if src.Strict {
					return err
				}
				problems = append(problems, err.Error())
				return nil
			}
			data, err := fs.ReadFile(src.FS, name)
			var blocks []map[string]any
			if err == nil {
				_, _ = fmt.Fprintf(h, "%s\x00%s\x00", src.Name+"/"+name, data)
				blocks, err = readFile(data)
			}
			if err != nil {
				if err := fail(fmt.Errorf("%s: %w", origin, err)); err != nil {
					return nil, err
				}
				continue
			}
			for i, raw := range blocks {
				id, _ := raw["id"].(string)
				old := byID[id]
				r, err := load(raw, old, src.Strict)
				if err != nil {
					label := cmp.Or(id, fmt.Sprintf("number %d", i+1))
					if err := fail(fmt.Errorf("%s: rule %q skipped: %w", origin, label, err)); err != nil {
						return nil, err
					}
					continue
				}
				if r != nil {
					r.file, r.Origin = origin, "built-in"
					if !src.Strict {
						r.Origin = origin
					}
				}
				if old != nil { // a change: same place, new fields
					old.rule, old.off = r, nil
					if r == nil {
						old.off = &Rule{ID: id, Origin: origin, Changes: old.origin}
					} else {
						r.Changes = old.origin
					}
					maps.Copy(old.raw, raw)
					continue
				}
				l := &loaded{raw: raw, rule: r, origin: r.Origin}
				byID[id] = l
				if src.Strict {
					others = append(others, l)
				} else {
					mine = append(mine, l)
				}
			}
		}
	}
	s := &Set{Problems: problems, Hash: hex.EncodeToString(h.Sum(nil)[:8]), byID: map[string]*Rule{}}
	for _, l := range append(mine, others...) {
		if l.rule != nil {
			s.Rules = append(s.Rules, l.rule)
			s.byID[l.rule.ID] = l.rule
		} else if l.off != nil {
			s.Off = append(s.Off, l.off)
		}
	}
	return s, nil
}

// readFile decodes a rule file into its [[rule]] blocks, each with its fields as written.
func readFile(data []byte) ([]map[string]any, error) {
	var f struct {
		Rules []map[string]any `toml:"rule"`
	}
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, err
	}
	for _, k := range md.Undecoded() {
		if k[0] != "rule" {
			return nil, fmt.Errorf("unknown table or field %q: rules go in [[rule]] blocks", k.String())
		}
	}
	if len(f.Rules) == 0 {
		return nil, errors.New("no [[rule]] block")
	}
	return f.Rules, nil
}

// load compiles a rule block, applied to the earlier rule with its ID if there is one. It
// returns nil for a rule turned off.
func load(raw map[string]any, old *loaded, strict bool) (*Rule, error) {
	if old != nil && strict {
		return nil, errors.New("another rule has this id")
	}
	if old != nil {
		raw = maps.Clone(raw)
		for k, v := range old.raw {
			if _, set := raw[k]; !set {
				raw[k] = v
			}
		}
	}
	sp, err := decode(raw)
	if err != nil {
		return nil, err
	}
	if sp.Level == "off" {
		if old == nil {
			return nil, fmt.Errorf(`level "off" turns off a rule, and no rule has the id %q`, sp.ID)
		}
		return nil, nil
	}
	return sp.compile()
}

// decode reads the fields of a block into a spec, saying which field is wrong and how.
func decode(raw map[string]any) (spec, error) {
	var sp spec
	for _, k := range slices.Sorted(maps.Keys(raw)) {
		if !ruleFields[k] {
			return sp, fmt.Errorf("unknown field %q%s", k, suggest(k, ruleFields))
		}
	}
	v := reflect.ValueOf(&sp).Elem()
	for i := range v.NumField() {
		name := v.Type().Field(i).Tag.Get("toml")
		val, ok := raw[name]
		if !ok {
			continue
		}
		switch dst := v.Field(i).Addr().Interface().(type) {
		case *string:
			s, ok := val.(string)
			if !ok {
				return sp, fmt.Errorf("%s must be text in quotes, got %v", name, val)
			}
			*dst = s
		case *int:
			n, ok := val.(int64)
			if !ok {
				return sp, fmt.Errorf("%s must be a number, got %v", name, val)
			}
			*dst = int(n)
		case *[]string:
			list, err := texts(name, val)
			if err != nil {
				return sp, err
			}
			*dst = list
		}
	}
	return sp, nil
}

// texts reads a field that takes one text or a list of them.
func texts(name string, val any) ([]string, error) {
	list, ok := val.([]any)
	if !ok {
		list = []any{val}
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, ok := x.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("%s must be text in quotes, or a list of them, got %v", name, val)
		}
		out = append(out, s)
	}
	return out, nil
}

// ruleFields are the fields of a rule, from the struct tags.
var ruleFields = func() map[string]bool {
	out := map[string]bool{}
	for f := range reflect.TypeFor[spec]().Fields() {
		out[f.Tag.Get("toml")] = true
	}
	return out
}()

// compile checks a rule and turns it into a Rule.
func (sp spec) compile() (*Rule, error) {
	if sp.ID == "" {
		return nil, errors.New("missing id")
	}
	if !validID.MatchString(sp.ID) {
		return nil, fmt.Errorf(`id must be area.name, in lowercase letters, digits and dashes, such as "mine.udev-command", got %q`, sp.ID)
	}
	r := &Rule{
		ID: sp.ID, Title: sp.Title, Source: sp.Source, Note: sp.Note, Explain: sp.Explain,
		MergeTitle: sp.MergeTitle, Match: sp.Match, NoMatch: sp.NoMatch, maxPriority: -1,
	}
	r.Category, _, _ = strings.Cut(sp.ID, ".")
	if sp.Level == "" {
		return nil, errors.New(`missing level: "err", "warn", "info", "context", "hidden" or "off"`)
	}
	i := slices.Index(levelNames[:], sp.Level)
	if i < 0 {
		return nil, fmt.Errorf(`level must be "err", "warn", "info", "context", "hidden" or "off", got %q`, sp.Level)
	}
	r.Level = Level(i)
	// fields that would have no effect are mistakes: say so instead of ignoring them
	unused := func(field string, set bool, why string) error {
		if set {
			return fmt.Errorf("%s has no effect %s", field, why)
		}
		return nil
	}
	var err error
	switch r.Level {
	case Hidden:
		why := "in a hidden rule: its lines are only counted"
		err = cmp.Or(unused("note", sp.Note != "", why), unused("explain", sp.Explain != "", why),
			unused("merge_title", sp.MergeTitle != "", why), unused("merge_window", sp.MergeWindow != "", why),
			unused("until", len(sp.Until) > 0, why))
		if err == nil && sp.Title == "" {
			err = errors.New(`missing title: what the hidden lines are, as in "437 firewall log lines"`)
		}
	case Context:
		why := "in a context rule: it is not listed as a problem"
		err = cmp.Or(unused("title", sp.Title != "", why), unused("note", sp.Note != "", why),
			unused("explain", sp.Explain != "", why), unused("merge_title", sp.MergeTitle != "", why),
			unused("merge_window", sp.MergeWindow != "", why))
	default:
		if sp.Title == "" {
			err = errors.New("missing title")
		}
	}
	if err == nil && sp.MaxLines != 0 && len(sp.Until) == 0 {
		err = errors.New("max_lines has no effect without until")
	}
	if err == nil && len(sp.NoMatch) > 0 && len(sp.Text) == 0 {
		err = errors.New("nomatch has no effect without text: the rule takes every line of its source")
	}
	if err != nil {
		return nil, err
	}
	if sp.Priority != "" {
		if r.maxPriority = slices.Index(priorities[:], sp.Priority); r.maxPriority < 0 {
			return nil, fmt.Errorf(`priority must be one of %s (that one or more serious, as journalctl -p), got %q`,
				strings.Join(priorities[:], ", "), sp.Priority)
		}
	}
	if sp.MergeWindow != "" {
		if r.window, err = time.ParseDuration(sp.MergeWindow); err != nil || r.window <= 0 {
			return nil, fmt.Errorf(`merge_window must be a duration such as "30s", "10m" or "1h", got %q`, sp.MergeWindow)
		}
	}
	if len(sp.Text) == 0 && (sp.Source == "" || len(sp.Until) > 0) {
		return nil, errors.New("missing text: the line to recognise, with {name} for the parts that change")
	}
	for _, t := range sp.Text {
		p, err := compileText("text", t)
		if err != nil {
			return nil, err
		}
		r.take = append(r.take, p)
	}
	for _, t := range sp.Until {
		p, err := compileText("until", t)
		if err != nil {
			return nil, err
		}
		r.until = append(r.until, p)
	}
	switch {
	case sp.MaxLines < 0:
		return nil, errors.New("max_lines can't be negative")
	case sp.MaxLines == 0:
		r.maxLines = defaultMaxLines
	default:
		r.maxLines = sp.MaxLines
	}
	if err := r.checkTemplates(); err != nil {
		return nil, err
	}
	return r, nil
}

// validID is area.name: the area is the rule's category.
var validID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*(\.[a-z0-9]+(-[a-z0-9]+)*)+$`)

// priorities are the journal priorities by number, as journalctl -p names them.
var priorities = [...]string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

// placeholder is {name} or {name:type}.
var placeholder = regexp.MustCompile(`\{(\w+)(?::(\w*))?\}`)

// compileText turns a text with placeholders into a pattern. The text is literal; {name} takes
// what comes up to the text after it (at the start: from the start of the message; at the end:
// the rest of the line); {name:num} takes a number; {_} anything, without keeping it.
func compileText(field, text string) (pattern, error) {
	var expr strings.Builder
	var lits []string
	names := map[string]bool{}
	at := 0
	locs := placeholder.FindAllStringSubmatchIndex(text, -1)
	for i, loc := range locs {
		lit := text[at:loc[0]]
		if i > 0 && lit == "" {
			return pattern{}, fmt.Errorf("%s %q: %s and %s are next to each other: write the text that separates them",
				field, text, text[locs[i-1][0]:locs[i-1][1]], text[loc[0]:loc[1]])
		}
		expr.WriteString(regexp.QuoteMeta(lit))
		lits = append(lits, lit)
		name, typ := text[loc[2]:loc[3]], ""
		if loc[4] >= 0 {
			typ = text[loc[4]:loc[5]]
		}
		last := loc[1] == len(text)
		switch {
		case name == "_" && typ != "":
			return pattern{}, fmt.Errorf("%s %q: {_} takes anything, it has no type", field, text)
		case name == "_" && !last:
			expr.WriteString(`.*?`)
		case name == "_": // at the end: nothing to match
		case names[name]:
			return pattern{}, fmt.Errorf("%s %q: {%s} appears twice", field, text, name)
		case typ == "num":
			fmt.Fprintf(&expr, `(?P<%s>\d+)`, name)
		case typ != "":
			return pattern{}, fmt.Errorf("%s %q: in {%s:%s} the type can only be num, for a number", field, text, name, typ)
		case last:
			fmt.Fprintf(&expr, `(?P<%s>.+)`, name)
		default:
			fmt.Fprintf(&expr, `(?P<%s>.+?)`, name)
		}
		names[name] = true
		at = loc[1]
	}
	expr.WriteString(regexp.QuoteMeta(text[at:]))
	lits = append(lits, text[at:])
	re, err := regexp.Compile(expr.String())
	if err != nil {
		return pattern{}, fmt.Errorf("%s %q: %w", field, text, err)
	}
	// every piece of text is in a match: the longest rules out the most lines
	return pattern{re, slices.MaxFunc(lits, func(a, b string) int { return len(a) - len(b) })}, nil
}

// checkTemplates makes sure every {name} in title, note and merge_title is a part the rule
// names in its text or until, {source}, or {count} in merge_title.
func (r *Rule) checkTemplates() error {
	names := map[string]bool{"source": true}
	for _, p := range append(slices.Clone(r.take), r.until...) {
		for _, n := range p.re.SubexpNames() {
			names[n] = n != ""
		}
	}
	for _, t := range []struct{ field, text string }{{"title", r.Title}, {"note", r.Note}, {"merge_title", r.MergeTitle}} {
		for _, m := range placeholder.FindAllStringSubmatch(t.text, -1) {
			if m[2] != "" {
				return fmt.Errorf("%s uses {%s:%s}: write {%s}, a type goes only in text", t.field, m[1], m[2], m[1])
			}
			if !names[m[1]] && (m[1] != "count" || t.field != "merge_title") {
				if hint := suggest(m[1], names); hint != "" {
					return fmt.Errorf("%s uses {%s}, but the rule names no part %s%s", t.field, m[1], m[1], hint)
				}
				return fmt.Errorf("%s uses {%s}, but the rule names no part %s: write {%s} in text", t.field, m[1], m[1], m[1])
			}
		}
	}
	return nil
}

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
	for _, line := range r.Match {
		if find(r.take, line) == nil {
			fails = append(fails, fmt.Sprintf("%s (%s): should match %q", r.ID, r.file, line))
		}
	}
	for _, line := range r.NoMatch {
		if find(r.take, line) != nil {
			fails = append(fails, fmt.Sprintf("%s (%s): should not match %q", r.ID, r.file, line))
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
		for _, line := range r.Match {
			if by := s.taker(r, line); by != r {
				name := "no rule"
				if by != nil {
					name = by.ID + " (" + by.file + ")"
				}
				fails = append(fails, fmt.Sprintf("%s (%s): %q is taken by %s first", r.ID, r.file, line, name))
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
	events, hidden := e.Finish()
	switch {
	case len(hidden) > 0:
		return hidden[0].Rule
	case len(events) > 0:
		return events[0].Rule
	}
	return nil
}

// find returns the named parts of the first pattern that matches s, nil when none does. With no
// patterns every line matches.
func find(patterns []pattern, s string) map[string]string {
	if len(patterns) == 0 {
		return map[string]string{}
	}
	for _, p := range patterns {
		if !strings.Contains(s, p.lit) {
			continue
		}
		m := p.re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		out := map[string]string{}
		for i, name := range p.re.SubexpNames() {
			if name != "" && m[i] != "" {
				out[name] = m[i]
			}
		}
		return out
	}
	return nil
}

// matches tells whether any of the patterns matches s (with none, every line does).
func matches(patterns []pattern, s string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if strings.Contains(s, p.lit) && p.re.MatchString(s) {
			return true
		}
	}
	return false
}

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
