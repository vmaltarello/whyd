package analyze

import (
	"strings"
	"testing"
	"time"

	"github.com/vmaltarello/whyd/internal/journal"
	"github.com/vmaltarello/whyd/internal/rules"
)

// runEnd analyses "source: message" lines one second apart from 18:00, ending with a power-off.
func runEnd(t *testing.T, lines ...string) *Result {
	t.Helper()
	set, err := rules.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	lines = append(lines, "systemd: Reached target System Power Off.", "systemd-journald: Journal stopped")
	at := time.Date(2026, 10, 9, 18, 0, 0, 0, time.Local)
	res, err := Run(Input{Rules: set, Read: func(fn func(journal.Entry) error) error {
		for i, l := range lines {
			src, msg, _ := strings.Cut(l, ": ")
			if err := fn(journal.Entry{Time: at.Add(time.Duration(i) * time.Second), Priority: 6, Source: src, Message: msg}); err != nil {
				return err
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRequests(t *testing.T) {
	login := "systemd: pam_unix(sddm:session): session opened for user alex(uid=1000) by alex(uid=0)"
	cases := []struct {
		name  string
		lines []string
		words string // what follows the time, naming the user
	}{
		{"none", nil, ""},
		{
			"sudo",
			[]string{
				login,
				"sudo: alex : TTY=pts/0 ; PWD=/home/alex ; USER=root ; COMMAND=/usr/bin/shutdown now",
				"systemd-logind: poweroff requested from client PID 9 ('shutdown') (unit user@1000.service)...",
			},
			" by alex, with sudo shutdown now",
		},
		{
			"no sudo",
			[]string{
				login,
				"systemd-logind: poweroff requested from client PID 9 ('shutdown') (unit user@1000.service)...",
			},
			" by alex, with shutdown",
		},
		{
			"sudo long before",
			[]string{
				login,
				"sudo: alex : TTY=pts/0 ; PWD=/home/alex ; USER=root ; COMMAND=/usr/bin/shutdown now",
				"systemd: a", "systemd: b", "systemd: c", "systemd: d", "systemd: e",
				"systemd-logind: poweroff requested from client PID 9 ('shutdown') (unit user@1000.service)...",
			},
			" by alex, with shutdown",
		},
		{
			"systemctl in a session",
			[]string{
				"systemd-logind: New session '3' of user 'alex' with class 'user' and type 'wayland'.",
				"systemd-logind: poweroff requested from client PID 9 ('systemctl') (unit session-3.scope)...",
			},
			" by alex, with systemctl poweroff",
		},
		{
			"old session line",
			[]string{
				"systemd-logind: New session 3 of user alex.",
				"systemd-logind: poweroff requested from client PID 9 ('systemctl') (unit session-3.scope)...",
			},
			" by alex, with systemctl poweroff",
		},
		{
			"greeter session",
			[]string{
				"systemd-logind: New session '5' of user 'greeter' with class 'greeter' and type 'tty'.",
				"systemd-logind: poweroff requested from client PID 9 ('systemctl') (unit session-5.scope)...",
			},
			" from the login screen",
		},
		{
			"login screen",
			[]string{
				"systemd-logind: poweroff requested from client PID 9 ('sddm') (unit sddm.service)...",
			},
			" from the login screen (sddm)",
		},
		{
			"service",
			[]string{
				"systemd-logind: poweroff requested from client PID 9 ('unattended-upgr') (unit unattended-upgrades.service)...",
			},
			" by unattended-upgrades",
		},
		{
			"power key",
			[]string{
				"systemd-logind: Power key pressed short.", "systemd-logind: Powering off...",
			},
			" with the power key",
		},
		{
			"power key ignored",
			[]string{
				"systemd-logind: Power key pressed short.", "systemd: a", "systemd: b", "systemd: c",
				"systemd: d", "systemd: e", "systemd: f", "systemd-logind: Powering off...",
			},
			"",
		},
		{
			"scheduled",
			[]string{
				login,
				"sudo: alex : TTY=pts/0 ; PWD=/home/alex ; USER=root ; COMMAND=/usr/bin/shutdown",
				"systemd-logind: The system will power off at Fri 2026-10-09 18:01:01 CEST!",
			},
			", scheduled at 18:00 by alex, with sudo shutdown",
		},
		{
			"scheduled, cancelled",
			[]string{
				"systemd-logind: The system will power off at Fri 2026-10-09 18:01:01 CEST!",
				"systemd-logind: System shutdown has been cancelled",
			},
			"",
		},
	}
	for _, c := range cases {
		r := runEnd(t, c.lines...)
		if got := r.Verdict.By.Words(true); got != c.words {
			t.Errorf("%s: %q, want %q", c.name, got, c.words)
		}
		if want := "shut down at " + r.Boot.Last.Format("15:04:05") + r.Verdict.By.Words(false); r.Verdict.Summary != want {
			t.Errorf("%s: summary %q, want %q", c.name, r.Verdict.Summary, want)
		}
	}
}
