package analyze

import (
	"fmt"
	"maps"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vmaltarello/whyd/internal/journal"
	"github.com/vmaltarello/whyd/internal/rules"
	builtin "github.com/vmaltarello/whyd/rules"
)

func run(t *testing.T, name string) *Result {
	t.Helper()
	set, err := rules.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(Input{
		Rules: set,
		Read: func(fn func(journal.Entry) error) error {
			return journal.ReadFile("../../testdata/boot-"+name+".jsonl.gz", fn)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestVerdicts(t *testing.T) {
	cases := map[string]EndKind{
		"unclean-oom":          EndUnclean,
		"unclean-quiet":        EndUnclean,
		"clean-poweroff":       EndPowerOff,
		"clean-reboot-suspend": EndReboot,
	}
	for name, want := range cases {
		if got := run(t, name).Verdict.Kind; got != want {
			t.Errorf("%s: verdict %v, want %v", name, got, want)
		}
	}
}

// The real freeze of 9 Oct 2026: OOM at 10:53, sunshine crash, quiet end at 12:11:56.
func TestFreeze(t *testing.T) {
	r := run(t, "unclean-oom")

	if r.Kernel != "7.2.9-arch1-1" {
		t.Errorf("kernel %q", r.Kernel)
	}
	if got := r.Machine.Line(); got != "HUAWEI KLVL-WXX9, AMD Ryzen 5 4600H, 16 GB RAM, amdgpu" {
		t.Errorf("machine %q", got)
	}
	if len(r.Users) != 1 || r.Users[0] != "user" { // greeter and root are system users
		t.Errorf("users %q", r.Users)
	}
	if r.Suspends != 1 || r.Asleep.Round(time.Minute) != 10*time.Hour+38*time.Minute ||
		len(r.Sleeps) != 1 || r.Sleeps[0].From.Format("15:04") != "23:27" {
		t.Errorf("suspends %d, asleep %v", r.Suspends, r.Asleep)
	}
	if w := r.Worst(); w == nil || w.Rule.ID != "memory.oom-report" {
		t.Fatalf("worst finding: %+v", w)
	}
	if r.Quiet.Round(time.Minute) != 14*time.Minute {
		t.Errorf("quiet %v, want ~14 min", r.Quiet)
	}
	if r.HiddenLines != 437+149+1 { // firewall, rtw88 push errors, one wireless-extensions warning
		t.Errorf("hidden lines %d", r.HiddenLines)
	}
	if len(r.Tail) == 0 || r.Tail[0].Time.Before(r.Boot.Last.Add(-TailKeep)) || len(r.TailSince(TailWindow)) >= len(r.Tail) {
		t.Errorf("tail: %d lines", len(r.Tail))
	}

	if r.UnknownErr+r.UnknownW == 0 || len(r.Unknown) == 0 {
		t.Error("unrecognised warnings not collected")
	}
	for i := 1; i < len(r.Unknown); i++ {
		if r.Unknown[i].Priority < r.Unknown[i-1].Priority {
			t.Fatal("unrecognised groups not sorted by priority")
		}
	}

	o := r.OOM
	if o == nil {
		t.Fatal("no OOM report")
	}
	if len(o.Killed) != 4 || o.Killed[0].Name != "Web Content" {
		t.Errorf("killed %+v", o.Killed)
	}
	if o.GPUKB < 7_000_000 || o.ShmemKB < 4_000_000 || o.AnonKB < 5_000_000 {
		t.Errorf("memory breakdown %+v", o)
	}
	if len(o.Top) == 0 || o.Top[0].Name != "Isolated Web Co" {
		t.Errorf("top processes %+v", o.Top)
	}
}

func TestQuietCrashes(t *testing.T) {
	r := run(t, "unclean-quiet")
	if r.Quiet != 0 {
		t.Errorf("4 minutes of silence should not be a finding, got %v", r.Quiet)
	}
	crashes := 0
	for _, ev := range r.Findings {
		if ev.Rule.ID == "crash.coredump" {
			crashes++
		}
	}
	if crashes != 2 {
		t.Errorf("%d crashes, want 2 (awww-daemon, qemu)", crashes)
	}
}

func TestDuration(t *testing.T) {
	cases := map[time.Duration]string{
		63 * time.Second:                  "63s",
		14*time.Minute + 6*time.Second:    "14m",
		17*time.Hour + 22*time.Minute + 9: "17h22m",
	}
	for d, want := range cases {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestCollapse(t *testing.T) {
	at := time.Date(2026, 10, 9, 11, 57, 37, 0, time.Local)
	line := func(sec int, src, msg string) TailLine {
		return TailLine{Entry: journal.Entry{Time: at.Add(time.Duration(sec) * time.Second), Source: src, Message: msg}}
	}
	in := []TailLine{
		line(0, "sunshine", "[11:57:37.485] Resolution: 2160x1440"),
		line(0, "sunshine", "[11:57:37.486] Offset: 0x0"),
		line(0, "sunshine", "[11:57:37.486] Logical size: 1440x960"),
		line(0, "sunshine", "[11:57:37.487] Offset: 0x0"),
		line(0, "sunshine", "[11:57:37.487] Logical size: 1440x960"),
		line(13, "sudo", "a password is required"),
		line(40, "sudo", "a password is required"), // back to back: merged however far apart
		line(41, "systemd", "Started Locale Service."),
		line(60, "sudo", "a password is required"), // not back to back and not within a second
	}
	got := Collapse(in)
	want := []int{1, 2, 2, 2, 1, 1}
	if len(got) != len(want) {
		t.Fatalf("got %d groups, want %d", len(got), len(want))
	}
	for i, g := range got {
		if g.Count != want[i] {
			t.Errorf("group %d (%q): count %d, want %d", i, g.Message, g.Count, want[i])
		}
	}
}

func TestOOMTaskFormats(t *testing.T) {
	ev := &rules.Event{Entries: []journal.Entry{
		{Message: "firefox invoked oom-killer: gfp_mask=0x140cca(GFP_HIGHUSER_MOVABLE|__GFP_COMP), order=0"},
		// kernel 6.1 and later
		{Message: "[   4321]  1000  4321  1234567    51200    40000    11000      200   900000        0           100 firefox"},
		// older kernels: no rss_anon, rss_file, rss_shmem
		{Message: "[   1234]  1000  1234   654321    25600   300000        0             0 Isolated Web Co"},
	}}
	got := parseOOM(ev).Top
	want := []Process{{"firefox", 51200 * pageKB}, {"Isolated Web Co", 25600 * pageKB}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Top = %v, want %v", got, want)
	}
}

func TestGroupInsideGroup(t *testing.T) {
	set, err := rules.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	// a second OOM report starting before the first one ended must not swallow the first
	at := time.Date(2026, 10, 9, 10, 53, 0, 0, time.Local)
	lines := []string{
		"firefox invoked oom-killer: gfp_mask=0x140cca(GFP_HIGHUSER_MOVABLE|__GFP_COMP), order=0, oom_score_adj=0",
		"zen invoked oom-killer: gfp_mask=0x140cca(GFP_HIGHUSER_MOVABLE|__GFP_COMP), order=0, oom_score_adj=0",
	}
	res, err := Run(Input{Rules: set, Read: func(fn func(journal.Entry) error) error {
		for i, l := range lines {
			if err := fn(journal.Entry{Time: at.Add(time.Duration(i) * time.Second), Priority: 4, Source: "kernel", Kernel: true, Message: l}); err != nil {
				return err
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range res.Events {
		if ev.Rule.ID == "memory.oom-report" {
			n += ev.Count
		}
	}
	if n != 2 {
		t.Errorf("%d OOM reports, want 2", n)
	}
}

// runLines analyses kernel lines one millisecond apart, as the kernel writes a report.
func runLines(t *testing.T, lines []string) *Result {
	t.Helper()
	set, err := rules.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local)
	res, err := Run(Input{Rules: set, Read: func(fn func(journal.Entry) error) error {
		for i, l := range lines {
			if err := fn(journal.Entry{Time: at.Add(time.Duration(i) * time.Millisecond), Priority: 6, Source: "kernel", Kernel: true, Message: l}); err != nil {
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

func TestHibernation(t *testing.T) {
	r := runLines(t, []string{"Linux version 7.2.9", "PM: hibernation: hibernation entry", "PM: hibernation: hibernation exit", "PM: hibernation: hibernation entry"})
	if r.Verdict.Kind != EndAsleep || !strings.Contains(r.Verdict.Summary, "hibernated") {
		t.Errorf("verdict %+v", r.Verdict)
	}
	if r.Hibernations != 2 || r.Suspends != 0 || len(r.Sleeps) != 2 || !r.Sleeps[0].Hibernate || r.Asleep != time.Millisecond {
		t.Errorf("hibernations %d, suspends %d, sleeps %+v, asleep %v", r.Hibernations, r.Suspends, r.Sleeps, r.Asleep)
	}
}

// An OOM report lists one line per process: on a busy machine it is longer than a few hundred
// lines, and must still be one event with the killed process.
func TestLongOOMReport(t *testing.T) {
	lines := []string{"firefox invoked oom-killer: gfp_mask=0x140cca(GFP_HIGHUSER_MOVABLE|__GFP_COMP), order=0, oom_score_adj=0"}
	for i := range 900 {
		lines = append(lines, fmt.Sprintf("[%7d]  1000 %5d   654321    25600   300000        0             0 proc%d", i, i, i))
	}
	lines = append(lines, "Out of memory: Killed process 4321 (firefox) total-vm:3527084kB, anon-rss:209888kB")
	r := runLines(t, lines)
	var oom []string
	for _, ev := range r.Findings {
		if ev.Rule.Category == "memory" {
			oom = append(oom, ev.Rule.ID+": "+ev.Title)
		}
	}
	if len(oom) != 1 || oom[0] != "memory.oom-report: out of memory, firefox killed" {
		t.Errorf("memory findings %q", oom)
	}
	if r.OOM == nil || len(r.OOM.Killed) != 1 || r.OOM.Killed[0].Name != "firefox" {
		t.Errorf("OOM %+v", r.OOM)
	}
}

// Each out-of-memory problem shows the memory of its own report, whatever rule took the lines;
// a kill without a report shows none.
func TestOOMPerEvent(t *testing.T) {
	user := fstest.MapFS{"mine.toml": {Data: []byte(`
[[rule]]
id    = "memory.oom-report"
level = "off"

[[rule]]
id     = "mine.oom"
level  = "err"
source = "kernel"
text   = "{trigger} invoked oom-killer"
until  = "Out of memory: Killed process {pid:num} ({process})"
title  = "oom, {process} killed"
`)}}
	set, err := rules.Load(rules.Source{Name: "built-in", FS: builtin.Builtin, Strict: true}, rules.Source{Name: "u", FS: user})
	if err != nil || len(set.Problems) > 0 {
		t.Fatal(err, set.Problems)
	}
	report := func(kb int, name string) []string {
		return []string{
			name + " invoked oom-killer: gfp_mask=0x140cca(GFP_HIGHUSER_MOVABLE|__GFP_COMP), order=0",
			fmt.Sprintf("Node 0 active_anon:%dkB inactive_anon:0kB shmem:0kB", kb),
			fmt.Sprintf("Out of memory: Killed process 1 (%s) total-vm:1kB", name),
		}
	}
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local)
	var entries []journal.Entry
	for i, block := range [][]string{report(1000, "first"), report(2000, "second"), {"Out of memory: Killed process 2 (lone) total-vm:1kB"}} {
		for j, l := range block {
			entries = append(entries, journal.Entry{Time: at.Add(time.Duration(i)*time.Hour + time.Duration(j)*time.Millisecond), Priority: 6, Source: "kernel", Kernel: true, Message: l})
		}
	}
	r, err := Run(Input{Rules: set, Read: func(fn func(journal.Entry) error) error {
		for _, e := range entries {
			if err := fn(e); err != nil {
				return err
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, ev := range r.Findings {
		if o := r.OOMOf(ev); o != nil {
			got[ev.Title] = o.AnonKB
		} else {
			got[ev.Title] = -1
		}
	}
	want := map[string]int64{"oom, first killed": 1000, "oom, second killed": 2000, "out of memory, lone killed": -1}
	if !maps.Equal(got, want) {
		t.Errorf("memory per problem %v, want %v", got, want)
	}
	if r.OOM == nil || r.OOM.AnonKB != 1000 {
		t.Errorf("first report %+v", r.OOM)
	}
}

// CPU and board names lose what says nothing; boot framebuffers are not the graphics card.
func TestMachineNames(t *testing.T) {
	var m Machine
	for _, l := range []string{
		"DMI: SMBIOS 3.3 present.",
		"DMI: LENOVO 20XW0055GE/20XW0055GE, BIOS N32ET86W (1.62 ) 08/15/2023",
		"smpboot: CPU0: 11th Gen Intel(R) Core(TM) i7-1165G7 @ 2.80GHz (family: 0x6, model: 0x8c, stepping: 0x1)",
		"Memory: 7841212K/8203520K available (18432K kernel code, 2816K rwdata)",
		"[drm] Initialized simpledrm 1.0.0 for simple-framebuffer.0 on minor 0",
		"[drm] Initialized i915 1.6.0 for 0000:00:02.0 on minor 1",
		"[drm] Initialized nvidia-drm 0.0.0 for 0000:01:00.0 on minor 2",
	} {
		m.feed(l)
	}
	if got := m.Line(); got != "LENOVO 20XW0055GE, 11th Gen Intel Core i7-1165G7, 8 GB RAM, i915 + nvidia-drm" {
		t.Errorf("got %q", got)
	}
}
