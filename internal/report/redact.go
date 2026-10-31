// Package report prints an analysis as plain text (terminal, pipes) or as Markdown ready to
// paste on a forum, with personal data redacted.
package report

import (
	"cmp"
	"os"
	"os/user"
	"regexp"
	"slices"
	"strings"

	"github.com/vmaltarello/whyd/internal/analyze"
)

// Redactor hides what identifies a machine or a person: MAC and IP addresses, serial numbers,
// e-mail addresses, hostnames, user names, Wi-Fi and network connection names, Tailscale
// network names.
type Redactor struct {
	users          []string
	reHost, reUser *regexp.Regexp
}

// NewRedactor hides the hostname and user of this machine (under sudo, the user who ran sudo,
// not root) and those found in the session's journal, which may come from another machine.
// r may be nil.
func NewRedactor(r *analyze.Result) *Redactor {
	var hosts, users []string
	if h, err := os.Hostname(); err == nil {
		hosts = append(hosts, h)
	}
	if me := Me(); me != "" {
		users = append(users, me)
	}
	if r != nil {
		hosts = append(hosts, r.Host)
		users = append(users, r.Users...)
	}
	return newRedactor(hosts, users)
}

// Me is the user running whyd: under sudo, the user who ran sudo, not root. Tests replace it.
var Me = func() string {
	if name := os.Getenv("SUDO_USER"); name != "" {
		return name
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func newRedactor(hosts, users []string) *Redactor {
	r := &Redactor{}
	r.reUser, r.users = anyWord(users)
	r.reHost, _ = anyWord(hosts)
	return r
}

// anyWord returns a pattern matching any of the names as a whole word, and the names it kept:
// names shorter than 3 characters are left out, they would hit ordinary words.
func anyWord(names []string) (*regexp.Regexp, []string) {
	var keep []string
	for _, n := range names {
		if len(n) >= 3 && !slices.Contains(keep, n) {
			keep = append(keep, n)
		}
	}
	if len(keep) == 0 {
		return nil, nil
	}
	quoted := make([]string, len(keep))
	for i, n := range keep {
		quoted[i] = regexp.QuoteMeta(n)
	}
	// longest first, so "alexander" isn't cut to "[user]ander"
	slices.SortFunc(quoted, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	return regexp.MustCompile(`\b(?:` + strings.Join(quoted, "|") + `)\b`), keep
}

var (
	reMAC     = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{2}:){5,}[0-9a-f]{2}\b`)
	reIPv6    = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,4}:){3,7}[0-9a-f]{1,4}\b|\b[0-9a-f]{1,4}(?::[0-9a-f]{0,4})*::[0-9a-f:]*[0-9a-f]\b`)
	reIPv4    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	reSerial  = regexp.MustCompile(`(?i)(serial ?(?:number|no\.?)?\s*[=:]\s*)([^\s,;]+)`) // usb: "SerialNumber: 4C53…"
	reEmail   = regexp.MustCompile(`\b[\w.+-]+@[a-zA-Z][\w-]*(?:\.[\w-]+)*\.[a-zA-Z]{2,}\b`)
	reUnit    = regexp.MustCompile(`\.(?:service|socket|scope|slice|target|mount|timer|path|device|swap)$`)
	reSSID    = regexp.MustCompile(`(?i)(ssid[=: ]+['"]?)([^'"\s,)]+)`)
	reConn    = regexp.MustCompile(`((?:connection|network) ')([^']+)(')`) // NetworkManager, iwd: usually the Wi-Fi name
	reTailnet = regexp.MustCompile(`(?i)\b[a-z0-9-]+\.ts\.net\b`)
)

// Redact returns s with personal data replaced by [MAC], [IP], [serial], [email], [host],
// [user], [SSID] and [tailnet].
func (r *Redactor) Redact(s string) string {
	s = reMAC.ReplaceAllString(s, "[MAC]") // before IPv6: a MAC looks like hex groups too
	s = reIPv6.ReplaceAllString(s, "[IP]")
	s = reIPv4.ReplaceAllString(s, "[IP]")
	s = reSerial.ReplaceAllString(s, "${1}[serial]")
	s = reEmail.ReplaceAllStringFunc(s, func(m string) string {
		if reUnit.MatchString(m) { // "getty@tty1.service" is a unit, not an address
			return m
		}
		return "[email]"
	})
	s = reSSID.ReplaceAllString(s, "${1}[SSID]")
	s = reConn.ReplaceAllString(s, "${1}[SSID]${3}")
	s = reTailnet.ReplaceAllString(s, "[tailnet]")
	// placeholders first, so a user or host called "user" or "host" doesn't hit "[user]" itself
	if r.reUser != nil {
		for _, u := range r.users {
			s = strings.ReplaceAll(s, "/home/"+u, "/home/\x00u")
		}
		s = r.reUser.ReplaceAllString(s, "\x00u")
	}
	if r.reHost != nil {
		s = r.reHost.ReplaceAllString(s, "\x00h")
	}
	return strings.NewReplacer("\x00u", "[user]", "\x00h", "[host]").Replace(s)
}
