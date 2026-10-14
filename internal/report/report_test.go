package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/vmaltarello/whyd/internal/analyze"
	"github.com/vmaltarello/whyd/internal/journal"
	"github.com/vmaltarello/whyd/internal/rules"
)

func TestRedact(t *testing.T) {
	r := newRedactor([]string{"archlinux"}, []string{"user"})
	cases := map[string]string{
		"MAC=02:1a:2b:3c:4d:5e:02:6f:70:81:92:a3:08:00 SRC=192.168.50.254":    "MAC=[MAC] SRC=[IP]",
		"now using [2001:db8:6474:3296:adbb:72a5:aa3:9e18]:50631":             "now using [[IP]]:50631",
		"node fe80::1c2b:3aff:fe4d:5e6f up":                                   "node [IP] up",
		"PWD=/home/user/code/whyd":                                            "PWD=/home/[user]/code/whyd",
		"user : a password is required":                                       "[user] : a password is required",
		"linux@archlinux kernel":                                              "linux@[host] kernel",
		"wlan0: Connected to SSID 'HomeNet-5G'":                               "wlan0: Connected to SSID '[SSID]'",
		"device (wlan0): Activation: starting connection 'HomeNet-5G' (uuid)": "device (wlan0): Activation: starting connection '[SSID]' (uuid)",
		"SearchDomains:[tail0a1b2c.ts.net.]":                                  "SearchDomains:[[tailnet].]",
		"at 12:11:56 nothing":                                                 "at 12:11:56 nothing", // times are not addresses
		"usb 1-2: SerialNumber: 4C530001190527115213":                         "usb 1-2: SerialNumber: [serial]",
		"mail from alex.rossi@example.com rejected":                           "mail from [email] rejected",
		"Started getty@tty1.service":                                          "Started getty@tty1.service",
		"wlan0: Connected to network 'HomeNet-5G'":                            "wlan0: Connected to network '[SSID]'",
	}
	for in, want := range cases {
		if got := r.Redact(in); got != want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func freeze(t *testing.T) *analyze.Result { return load(t, "unclean-oom") }

func load(t *testing.T, name string) *analyze.Result {
	t.Helper()
	set, err := rules.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	res, err := analyze.Run(analyze.Input{Rules: set, Read: func(fn func(journal.Entry) error) error {
		return journal.ReadFile("../../testdata/boot-"+name+".jsonl.gz", fn)
	}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMarkdown(t *testing.T) {
	var b bytes.Buffer
	if err := Markdown(&b, freeze(t), newRedactor([]string{"archlinux"}, []string{"user"})); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"- **end:** abrupt end, journal stops at 12:11:56; nothing abnormal logged after 11:57",
		"- **before:** 10:53 out of memory, 4 processes killed; sunshine killed by SIGTRAP",
		"| error | out of memory, 4 processes killed (procs 6.0G, shmem 4.4G, gpu 7.5G, swap ",
		"| warning | sunshine killed by SIGTRAP",
		"quiet for 14m before the end",
		"Largest processes",
		"### Last lines",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q\n%s", want, out)
		}
	}
	for _, leak := range []string{"192.168.", "/home/user", "02:6f:70", "—"} {
		if strings.Contains(out, leak) {
			t.Errorf("report contains %q", leak)
		}
	}
}

func TestText(t *testing.T) {
	var b bytes.Buffer
	if err := Text(&b, freeze(t)); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"end     abrupt end, journal stops at 12:11:56",
		"other   253 warnings no rule recognises, in 44 different messages",
		"        nothing abnormal logged after 11:57",
		"before  10:53 out of memory, 4 processes killed; sunshine killed by SIGTRAP",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text misses %q\n%s", want, out)
		}
	}
}

func TestWhen(t *testing.T) {
	Now = func() time.Time { return time.Date(2026, 10, 9, 18, 30, 0, 0, time.Local) }
	defer func() { Now = time.Now }()
	cases := map[time.Time]string{
		time.Date(2026, 10, 9, 10, 53, 0, 0, time.Local): "today 10:53",
		time.Date(2026, 10, 8, 18, 49, 0, 0, time.Local): "yesterday 18:49",
		time.Date(2026, 10, 6, 12, 21, 0, 0, time.Local): "Tue 12:21",
		time.Date(2026, 9, 25, 16, 58, 0, 0, time.Local): "25 Sep 16:58",
	}
	for at, want := range cases {
		if got := When(at); got != want {
			t.Errorf("When(%v) = %q, want %q", at, got, want)
		}
	}
}

// "before" names possible causes: errors, and warnings close to the end; nothing after a clean end.
func TestBefore(t *testing.T) {
	cases := map[string]string{
		"unclean-oom":    "10:53 out of memory, 4 processes killed; sunshine killed by SIGTRAP",
		"unclean-quiet":  "21:42 qemu-system-x86 killed by SIGSEGV", // the wireplumber warning at boot is left out
		"clean-poweroff": "",
	}
	for name, want := range cases {
		if got := Before(load(t, name)); got != want {
			t.Errorf("%s: before = %q, want %q", name, got, want)
		}
	}
}

func TestRedactNamedLikePlaceholders(t *testing.T) {
	r := newRedactor([]string{"host"}, []string{"user"})
	if got := r.Redact("user@host: /home/user"); got != "[user]@[host]: /home/[user]" {
		t.Errorf("got %q", got)
	}
}

// A journal recorded on another machine: its hostname and users are hidden too.
func TestRedactRecorded(t *testing.T) {
	r := NewRedactor(&analyze.Result{Host: "otherbox", Users: []string{"alex", "alexander"}})
	in := "alexander@otherbox: /home/alex/x, alex"
	if got := r.Redact(in); got != "[user]@[host]: /home/[user]/x, [user]" {
		t.Errorf("got %q", got)
	}
}

// logger tags lines with the user name by default: sources are redacted like messages.
func TestMarkdownRedactsSources(t *testing.T) {
	r := freeze(t)
	r.Unknown = append([]analyze.UnknownGroup{{Source: "user", Message: "a | b ``` c", Priority: 3, Count: 1}}, r.Unknown...)
	r.Tail = append(r.Tail, analyze.TailLine{Entry: journal.Entry{Time: r.Boot.Last, Source: "user", Message: "done ```"}})
	var b bytes.Buffer
	if err := Markdown(&b, r, newRedactor([]string{"archlinux"}, []string{"user"})); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "| user |") || strings.Contains(out, " user: done") {
		t.Errorf("source not redacted:\n%s", out)
	}
	if !strings.Contains(out, "| error | 1 | [user] | a \\| b ``` c |") || !strings.Contains(out, "\n````\n") {
		t.Errorf("table cell or code fence wrong:\n%s", out)
	}
}

func TestRedactSudoUser(t *testing.T) {
	t.Setenv("SUDO_USER", "bob")
	if r := NewRedactor(nil); r.Redact("PWD=/home/bob/src") != "PWD=/home/[user]/src" {
		t.Errorf("under sudo the user who ran sudo is redacted, got %q", r.Redact("PWD=/home/bob/src"))
	}
}

// The day after the clocks change is 23 or 25 hours from the one before.
func TestWhenAcrossDST(t *testing.T) {
	rome, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Skip(err)
	}
	defer func(f func() time.Time) { Now = f }(Now)
	Now = func() time.Time { return time.Date(2027, 3, 29, 10, 0, 0, 0, rome) } // spring forward on 28 Mar
	if got := When(time.Date(2027, 3, 28, 9, 0, 0, 0, rome)); got != "yesterday 09:00" {
		t.Errorf("got %q", got)
	}
	Now = func() time.Time { return time.Date(2026, 10, 26, 10, 0, 0, 0, rome) } // fall back on 25 Oct
	if got := When(time.Date(2026, 10, 24, 9, 0, 0, 0, rome)); got != "Sat 09:00" {
		t.Errorf("got %q", got)
	}
}
