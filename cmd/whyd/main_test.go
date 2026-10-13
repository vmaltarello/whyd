package main

import (
	"slices"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	for _, c := range []struct {
		args string
		want options
		err  string
	}{
		{"", options{boot: -1}, ""},
		{"-b -3", options{boot: -3}, ""},
		{"report -b -2", options{cmd: "report", boot: -2}, ""},
		{"-b -2 report", options{cmd: "report", boot: -2}, ""},
		{"--file f.json.gz", options{boot: -1, file: "f.json.gz"}, ""},
		{"rules a.toml dir", options{cmd: "rules", boot: -1, paths: []string{"a.toml", "dir"}}, ""},
		{"--help", options{boot: -1, help: true}, ""},
		{"-h", options{boot: -1, help: true}, ""},
		{"--version", options{boot: -1, version: true}, ""},
		{"list", options{}, `unknown command "list": the commands are report and rules`},
		{"--text", options{}, "unknown option --text"},
		{"-u", options{}, "unknown option -u"},
		{"-b", options{}, "-b needs a value"},
		{"-b x", options{}, "-b needs a number"},
		{"report extra", options{}, `unexpected "extra"`},
		{"rules -b 1", options{}, "-b is for a session, not for rules"},
		{"rules --file f", options{}, "--file is for a session, not for rules"},
	} {
		got, err := parseArgs(strings.Fields(c.args))
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%q: error %v, want %q", c.args, err, c.err)
		case c.err == "" && err != nil:
			t.Errorf("%q: %v", c.args, err)
		case c.err == "" && (got.cmd != c.want.cmd || got.boot != c.want.boot || got.file != c.want.file ||
			got.help != c.want.help || got.version != c.want.version || !slices.Equal(got.paths, c.want.paths)):
			t.Errorf("%q: got %+v, want %+v", c.args, got, c.want)
		}
	}
}
