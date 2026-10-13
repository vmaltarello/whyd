package rules

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vmaltarello/whyd/internal/journal"
	builtin "github.com/vmaltarello/whyd/rules"
)

func loadBuiltin(t *testing.T) *Set {
	t.Helper()
	s, err := LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// loadUser loads the built-in rules and one user file.
func loadUser(t *testing.T, text string) *Set {
	t.Helper()
	user := fstest.MapFS{"mine.toml": {Data: []byte(text)}}
	s, err := Load(Source{Name: "built-in", FS: builtin.Builtin, Strict: true}, Source{Name: "/home/u/.config/whyd/rules", FS: user})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Every example of the built-in rules.
func TestBuiltinExamples(t *testing.T) {
	s := loadBuiltin(t)
	if len(s.Rules) < 15 {
		t.Fatalf("only %d built-in rules loaded", len(s.Rules))
	}
	for _, r := range s.Rules {
		if len(r.Match) == 0 {
			t.Errorf("%s has no example", r.ID)
		}
	}
	for _, f := range s.Test() {
		t.Error(f)
	}
}

func TestText(t *testing.T) {
	for _, c := range []struct {
		text, line string
		want       map[string]string // nil: no match
	}{
		{
			"Out of memory: Killed process {pid:num} ({process})", "Out of memory: Killed process 12 (Web Content) total-vm:1kB",
			map[string]string{"pid": "12", "process": "Web Content"},
		},
		{
			"{unit}: Failed with result '{result}'", "a.service: Failed with result 'exit-code'.",
			map[string]string{"unit": "a.service", "result": "exit-code"},
		},
		{
			"Kernel panic - not syncing: {reason}", "Kernel panic - not syncing: Fatal exception in interrupt",
			map[string]string{"reason": "Fatal exception in interrupt"},
		},
		{
			"INFO: task {process}:{pid:num} blocked", "INFO: task kworker/u16:2:123 blocked for more than 122 seconds.",
			map[string]string{"process": "kworker/u16:2", "pid": "123"},
		},
		{
			"signal {n:num}/{signal}, processing", "terminated abnormally with signal 5/TRAP, processing...",
			map[string]string{"n": "5", "signal": "TRAP"},
		},
		{
			"[drm:{func} [amdgpu]] *ERROR*", "[drm:amdgpu_dm_plane_helper_prepare_fb [amdgpu]] *ERROR* bind failed",
			map[string]string{"func": "amdgpu_dm_plane_helper_prepare_fb"},
		},
		{
			"amdgpu{_}ring {ring} timeout", "amdgpu 0000:03:00.0: amdgpu: ring gfx_0.0.0 timeout, signaled seq=1",
			map[string]string{"ring": "gfx_0.0.0"},
		},
		{"IN={_} OUT={_} SRC=", "[UFW BLOCK] IN=wlan0 OUT= MAC=02:1a SRC=192.168.50.254", map[string]string{}},
		{"{n:num} attempts", "three attempts", nil},
		{"a.b", "axb", nil}, // the text is literal: a dot is a dot
	} {
		p, err := compileText("text", c.text)
		if err != nil {
			t.Errorf("%q: %v", c.text, err)
			continue
		}
		if got := find([]pattern{p}, c.line); !maps.Equal(got, c.want) || (got == nil) != (c.want == nil) {
			t.Errorf("%q on %q: got %v, want %v", c.text, c.line, got, c.want)
		}
	}
}

func TestUserRules(t *testing.T) {
	user := fstest.MapFS{
		"mine.toml": {Data: []byte(`
[[rule]]
id    = "hidden.firewall"
title = "nothing"

[[rule]]
id    = "known.rtw88-tx"
level = "off"

[[rule]]
id    = "mine.first"
level = "warn"
title = "mine"
text  = "Out of memory"

[[rule]]
id    = "mine.broken"
level = "loud"
title = "broken"
text  = "x"

[[rule]]
id    = "mine.nothing"
level = "off"
`)},
		"garbage.toml": {Data: []byte("this is [ not toml")},
	}
	s, err := Load(Source{Name: "built-in", FS: builtin.Builtin, Strict: true}, Source{Name: "/home/u/.config/whyd/rules", FS: user})
	if err != nil {
		t.Fatalf("a broken user file must not stop loading: %v", err)
	}
	// a change keeps the rest of the rule and its place
	r := s.Rule("hidden.firewall")
	if r.Title != "nothing" || r.Level != Hidden || r.Changes != "built-in" || r.Origin != "/home/u/.config/whyd/rules/mine.toml" {
		t.Errorf("change: title %q, level %v, changes %q, origin %q", r.Title, r.Level, r.Changes, r.Origin)
	}
	if e := NewEngine(s); func() bool { n, _ := e.Feed(journal.Entry{Source: "kernel", Message: "IN=a OUT= SRC=1"}); return !n }() {
		t.Error("the changed rule lost its text")
	}
	if s.Rule("known.rtw88-tx") != nil {
		t.Error(`level "off" should remove the rule`)
	}
	if len(s.Off) != 1 || s.Off[0].ID != "known.rtw88-tx" || s.Off[0].Origin != "/home/u/.config/whyd/rules/mine.toml" || s.Off[0].Changes != "built-in" {
		t.Errorf("rules turned off should stay listed: %+v", s.Off)
	}
	if s.Rules[0].ID != "mine.first" {
		t.Errorf("new user rules come first, got %s", s.Rules[0].ID)
	}
	if i, j := slices.Index(s.Rules, s.Rule("hidden.firewall")), slices.Index(s.Rules, s.Rule("hidden.wpa-multicast")); i < 0 || i > j {
		t.Error("a changed rule should keep its place among the built-in ones")
	}
	got := strings.Join(s.Problems, "\n")
	for _, want := range []string{`rule "mine.broken" skipped: level must be`, `no rule has the id "mine.nothing"`, "garbage.toml"} {
		if !strings.Contains(got, want) {
			t.Errorf("problems %q\nmissing %q", got, want)
		}
	}
	if len(s.Problems) != 3 {
		t.Errorf("%d problems: %q", len(s.Problems), s.Problems)
	}
	if _, err := Load(Source{Name: "built-in", FS: user, Strict: true}); err == nil {
		t.Error("a broken strict source must fail")
	}
	twice := fstest.MapFS{
		"a.toml": {Data: []byte("[[rule]]\nid = \"x.a\"\nlevel = \"info\"\ntitle = \"a\"\ntext = \"a\"\n")},
		"b.toml": {Data: []byte("[[rule]]\nid = \"x.a\"\nlevel = \"warn\"\n")},
	}
	if _, err := Load(Source{Name: "built-in", FS: twice, Strict: true}); err == nil {
		t.Error("two built-in rules with the same id must fail")
	}
}

func TestEngineOnRealFreeze(t *testing.T) {
	e := NewEngine(loadBuiltin(t))
	hidden := 0
	err := journal.ReadFile("../../testdata/boot-unclean-oom.jsonl.gz", func(en journal.Entry) error {
		if isHidden, _ := e.Feed(en); isHidden {
			hidden++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	events, hiddenCounts := e.Finish()

	byRule := map[string]*Event{}
	for _, ev := range events {
		if byRule[ev.Rule.ID] == nil {
			byRule[ev.Rule.ID] = ev
		}
	}
	oom := byRule["memory.oom-report"]
	if oom == nil {
		t.Fatal("OOM not recognised")
	}
	if oom.Count != 4 || oom.Title != "out of memory, 4 processes killed" {
		t.Errorf("OOM event: count %d, title %q", oom.Count, oom.Title)
	}
	if got := oom.First.Format("15:04:05"); got != "10:53:10" {
		t.Errorf("OOM first at %s", got)
	}
	if byRule["memory.oom-kill"] != nil {
		t.Error("kill lines counted twice (also outside the report)")
	}
	if ev := byRule["crash.coredump"]; ev == nil || ev.Title != "sunshine killed by SIGTRAP" {
		t.Errorf("crash event: %+v", ev)
	}
	if ev := byRule["known.rtw88-tx"]; ev == nil || ev.Count != 149 {
		t.Errorf("rtw88 event: %+v", ev)
	}
	if ev := byRule["power.suspend"]; ev == nil {
		t.Error("suspend not recognised")
	}
	if hidden < 437 || len(hiddenCounts) == 0 || hiddenCounts[0].Rule.ID != "hidden.firewall" || hiddenCounts[0].Count != 437 {
		t.Errorf("hidden: %d lines, counts %+v", hidden, hiddenCounts)
	}
	for _, ev := range events {
		if strings.Contains(ev.Title, "{") {
			t.Errorf("unfilled title %q", ev.Title)
		}
	}
}

// A rule whose example an earlier rule takes would never fire: Test says so. User rules come
// first, so a wide one can hide a built-in rule, and Test says that too.
func TestShadowedRule(t *testing.T) {
	s := loadUser(t, `
[[rule]]
id     = "mine.oom"
level  = "warn"
title  = "oom"
source = "kernel"
text   = "Out of memory"

[[rule]]
id     = "mine.oom-kill"
level  = "warn"
title  = "oom kill"
source = "kernel"
text   = "Out of memory: Killed"
match  = ["Out of memory: Killed process 1 (x)"]
`)
	got := strings.Join(s.Test(), "\n")
	for _, want := range []string{
		`mine.oom-kill (/home/u/.config/whyd/rules/mine.toml): "Out of memory: Killed process 1 (x)" is taken by mine.oom`,
		`memory.oom-kill (memory.toml): "Out of memory: Killed process 49257 (Isolated Web Co) total-vm:3527084kB, anon-rss:209888kB" is taken by mine.oom`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fails %q\nmissing %q", got, want)
		}
	}
}

// A mistake in a rule is reported with what to write instead, never silently ignored.
func TestRuleMistakes(t *testing.T) {
	base := "id = \"mine.x\"\nlevel = \"warn\"\ntitle = \"x\"\ntext = \"x\"\n"
	cases := map[string]string{ // added to base, or replacing the line of the same field
		"sorce = \"kernel\"":                        `unknown field "sorce", did you mean "source"?`,
		"priority = \"<=4\"":                        "priority must be one of emerg, alert, crit, err, warning",
		"priority = \"warning\"":                    "",
		"merge_window = \"10 minutes\"":             "merge_window must be a duration",
		"note = \"{proc}\"":                         "note uses {proc}, but the rule names no part proc",
		"merge_title = \"{count} times\"":           "",
		"max_lines = 10":                            "max_lines has no effect without until",
		"match = 3":                                 "match must be text in quotes",
		"level = \"loud\"":                          `level must be "err", "warn", "info", "context", "hidden" or "off", got "loud"`,
		"level = \"context\"":                       "title has no effect in a context rule",
		"text = \"{a}{b}\"":                         "{a} and {b} are next to each other",
		"text = \"{a:int}\"":                        "the type can only be num",
		"text = \"{a} {a}\"":                        "{a} appears twice",
		"text = \"\"":                               "text must be text in quotes",
		"text = [\"a {x}\", \"b {x}\"]":             "",
		"id = \"Mine.X\"":                           "id must be area.name, in lowercase letters, digits and dashes",
		"id = \"mine\"":                             "id must be area.name",
		"id = \"mine.a.b-c\"":                       "",
		"title = \"{n:num}\"\ntext = \"a {n:num}\"": "title uses {n:num}: write {n}, a type goes only in text",
		"regex = 'x'":                               `unknown field "regex"`,
		"title = \"{x}\"\ntext = \"a {x}\"\nsource = \"s\"\nuntil = \"z\"\nmax_lines = 5": "",
	}
	for extra, want := range cases {
		text := base
		for line := range strings.SplitSeq(extra, "\n") {
			field, _, _ := strings.Cut(line, " =")
			text = strings.Replace(text, field+" = ", "#"+field+" = ", 1) // the base line is replaced
		}
		text += extra
		s, err := Load(Source{Name: "u", FS: fstest.MapFS{"r.toml": {Data: []byte("[[rule]]\n" + text)}}})
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(s.Problems, "\n")
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("%s\n got %q\nwant %q", extra, got, want)
		}
	}
	for text, want := range map[string]string{
		"id = \"mine.m\"\ntitle = \"m\"\ntext = 'a'":                                          "missing level",
		"id = \"mine.m\"\nlevel = \"info\"\ntext = 'a'":                                       "missing title",
		"id = \"mine.m\"\nlevel = \"info\"\ntitle = \"m\"":                                    "missing text",
		"id = \"mine.m\"\nlevel = \"info\"\ntitle = \"m\"\nsource = \"s\"":                    "", // every line of s
		"id = \"mine.m\"\nlevel = \"info\"\ntitle = \"m\"\nsource = \"s\"\nnomatch = [\"x\"]": "nomatch has no effect without text",
		"id = \"mine.m\"\nlevel = \"info\"\ntitle = \"m\"\nsource = \"s\"\nuntil = 'b'":       "missing text",
		"id = \"mine.h\"\nlevel = \"hidden\"\ntext = 'a'":                                     "missing title: what the hidden lines are",
		"id = \"mine.h\"\nlevel = \"hidden\"\ntitle = \"h\"\ntext = 'a'\nexplain = \"e\"":     "explain has no effect in a hidden rule",
		"id = \"mine.c\"\nlevel = \"context\"\ntext = 'a'":                                    "",
		"id = \"mine.o\"\nlevel = \"off\"":                                                    `no rule has the id "mine.o"`,
		"level = \"info\"\ntitle = \"m\"\ntext = 'a'":                                         "missing id",
	} {
		s, err := Load(Source{Name: "u", FS: fstest.MapFS{"r.toml": {Data: []byte("[[rule]]\n" + text)}}})
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(s.Problems, "\n")
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("%s\n got %q\nwant %q", text, got, want)
		}
	}
}

// A rule turned off and on again is back in its place, changing the rule first defined.
func TestOffAndOn(t *testing.T) {
	user := fstest.MapFS{
		"a.toml": {Data: []byte("[[rule]]\nid = \"known.rtw88-tx\"\nlevel = \"off\"\n")},
		"b.toml": {Data: []byte("[[rule]]\nid = \"known.rtw88-tx\"\nlevel = \"warn\"\n")},
	}
	s, err := Load(Source{Name: "built-in", FS: builtin.Builtin, Strict: true}, Source{Name: "u", FS: user})
	if err != nil {
		t.Fatal(err)
	}
	r := s.Rule("known.rtw88-tx")
	if r == nil || r.Level != Warn || r.Changes != "built-in" || len(s.Off) != 0 || len(s.Problems) != 0 {
		t.Errorf("rule %+v, off %v, problems %q", r, s.Off, s.Problems)
	}
}

// The table of built-in rules in docs/RULES.md lists every rule as it is.
func TestGuideTable(t *testing.T) {
	data, err := os.ReadFile("../../docs/RULES.md")
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(data), "\n## Built-in rules\n")
	if !ok {
		t.Fatal("no Built-in rules section in docs/RULES.md")
	}
	rows := map[string]string{}
	for line := range strings.SplitSeq(table, "\n") {
		if rest, ok := strings.CutPrefix(line, "| `"); ok {
			id, _, _ := strings.Cut(rest, "`")
			rows[id] = line
		}
	}
	for _, r := range loadBuiltin(t).Rules {
		title := r.Title
		if r.Level == Context {
			title = "(context)"
		}
		want := fmt.Sprintf("| `%s` | %s | %s | %s |", r.ID, r.file, r.Level, title)
		if rows[r.ID] != want {
			t.Errorf("docs/RULES.md:\n got %q\nwant %q", rows[r.ID], want)
		}
		delete(rows, r.ID)
	}
	for id := range rows {
		t.Errorf("docs/RULES.md lists %s, which is not a built-in rule", id)
	}
}
