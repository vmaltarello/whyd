package analyze

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vmaltarello/whyd/internal/journal"
	"github.com/vmaltarello/whyd/internal/rules"
)

// runEnd analyses "source: message" lines one second apart from 18:00, ending with a power-off.
func runEnd(t *testing.T, lines ...string) *Result {
	t.Helper()
	return runLog(t, append(lines, "systemd: Reached target System Power Off.", "systemd-journald: Journal stopped")...)
}

// runLog analyses "source: message" lines one second apart from 18:00.
func runLog(t *testing.T, lines ...string) *Result {
	t.Helper()
	set, err := rules.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 9, 18, 0, 0, 0, time.Local)
	res, err := Run(Input{Rules: set, Read: func(fn func(journal.Entry) error) error {
		for i, l := range lines {
			src, msg, _ := strings.Cut(l, ": ")
			if err := fn(journal.Entry{Time: at.Add(time.Duration(i) * time.Second), Priority: 6, Source: src, Kernel: src == "kernel", Message: msg}); err != nil {
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

func TestSleepRequests(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		words string
	}{
		{"none", nil, ""},
		{"lid", []string{"systemd-logind: Lid closed.", "systemd-logind: Suspending..."}, ", lid closed"},
		{
			"battery",
			[]string{
				"systemd-logind: suspend requested from client PID 9 ('upowerd') (unit upower.service)...",
				"systemd-logind: The system will sleep now!",
			},
			", battery critical",
		},
		{
			"systemctl",
			[]string{
				"systemd-logind: New session '3' of user 'alex' with class 'user' and type 'wayland'.",
				"systemd-logind: suspend requested from client PID 9 ('systemctl') (unit session-3.scope)...",
			},
			" by alex, with systemctl suspend",
		},
		{
			"lid while docked",
			[]string{
				"systemd-logind: Lid closed.", "systemd: a", "systemd: b", "systemd: c", "systemd: d",
				"systemd: e", "systemd: f", "systemd-logind: Suspending...",
			},
			"",
		},
		{
			"asked long before", append([]string{"systemd-logind: Lid closed.", "systemd-logind: Suspending..."},
				slices.Repeat([]string{"systemd: a"}, 70)...),
			"",
		},
	}
	for _, c := range cases {
		r := runLog(t, append(c.lines, "kernel: PM: suspend entry (s2idle)")...)
		if len(r.Sleeps) != 1 {
			t.Fatalf("%s: %d sleeps", c.name, len(r.Sleeps))
		}
		if got := r.Sleeps[0].By.Words(true); got != c.words {
			t.Errorf("%s: %q, want %q", c.name, got, c.words)
		}
	}
	r := runLog(t, "systemd-logind: suspend requested from client PID 9 ('upowerd') (unit upower.service)...",
		"kernel: PM: suspend entry (s2idle)")
	if want := "never woke up: suspended at 18:00, battery critical, and never resumed"; r.Verdict.Summary != want {
		t.Errorf("verdict %q, want %q", r.Verdict.Summary, want)
	}
	// a sleep asked for and woken from doesn't stay for the next one
	r = runLog(t, "systemd-logind: Lid closed.", "systemd-logind: Suspending...", "kernel: PM: suspend entry (s2idle)",
		"kernel: PM: suspend exit", "kernel: PM: suspend entry (s2idle)")
	if len(r.Sleeps) != 2 || r.Sleeps[0].By == nil || r.Sleeps[1].By != nil {
		t.Errorf("sleeps %+v", r.Sleeps)
	}
}
