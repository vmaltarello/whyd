package analyze

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vmaltarello/whyd/internal/journal"
)

// Request is who or what asked for a shutdown or a sleep, as logind and sudo log it.
type Request struct {
	How       string    // "with sudo shutdown now", "from the login screen (sddm)", "lid closed"…; may be ""
	User      string    // the person who asked, when the journal says it: outputs decide whether to name them
	Scheduled time.Time // when a shutdown for later was asked for, zero otherwise
}

// Words is what follows the time in the end line: " with sudo reboot", ", scheduled at 18:12 with
// sudo shutdown", ", lid closed", " by alex, with sudo reboot" when the user is to be named.
func (q *Request) Words(name bool) string {
	if q == nil {
		return ""
	}
	s := ""
	if !q.Scheduled.IsZero() {
		s = ", scheduled at " + q.Scheduled.Format("15:04")
	}
	if name && q.User != "" {
		s += " by " + q.User
		if q.How != "" {
			s += ","
		}
	}
	switch {
	case q.How == "":
	case strings.HasPrefix(q.How, "with ") || strings.HasPrefix(q.How, "from ") || strings.HasPrefix(q.How, "by "):
		s += " " + q.How
	case s == "":
		s = ", " + q.How
	default:
		s += ", " + q.How // ", scheduled at 18:12, battery critical"
	}
	return s
}

var (
	// logind: "poweroff requested from client PID 4471 ('shutdown') (unit user@1000.service)..."
	reRequested = regexp.MustCompile(`^(\S+) requested from client PID \d+ \('([^']*)'\)(?: \(unit ([^)]+)\))?`)
	// logind: "The system will power off at Fri 2026-09-25 18:22:33 CEST!"
	reScheduled = regexp.MustCompile(`^The system will (?:power off|reboot|halt|kexec) at `)
	// logind: "New session '2' of user 'alex' with class 'user'…", before systemd 258 "New session 2 of user alex."
	reNewSession = regexp.MustCompile(`^New session '?([^' ]+)'? of user '?([^' .]+)'?(?: with class '([^']+)')?`)
	// sudo: "alex : TTY=pts/0 ; PWD=/home/alex ; USER=root ; COMMAND=/usr/bin/shutdown now"
	reSudo     = regexp.MustCompile(`^(\S+) : .*; COMMAND=(.+)$`)
	reUserUnit = regexp.MustCompile(`^user@(\d+)\.service$`)
	reSession  = regexp.MustCompile(`^session-([^.]+)\.scope$`)
)

const (
	requestLag = 5 * time.Second // within this time of a logind request, a sudo command or a key press is what led to it
	sleepLag   = time.Minute     // and the system goes to sleep within this time of the request
)

// logind: what a key or the lid asks for, as the end line says it
var keys = map[string]string{
	"Power key pressed short.": "with the power key",
	"Power key pressed long.":  "with the power key",
	"Power key pressed.":       "with the power key", // before systemd 254
	"Suspend key pressed.":     "with the suspend key",
	"Hibernate key pressed.":   "with the hibernate key",
	"Lid closed.":              "lid closed",
}

// login screens that can shut the machine down
var greeters = map[string]bool{"sddm": true, "gdm": true, "gdm3": true, "lightdm": true, "greetd": true, "ly": true}

// requests follows the lines that say who asked for a shutdown or a sleep.
type requests struct {
	uids     map[int]string     // from pam: who has this uid
	sessions map[string]session // from logind
	sudoAt   time.Time
	sudoUser string
	sudoCmd  []string // the command, its first word a base name: ["shutdown", "now"]
	keyAt    time.Time
	key      string   // what the last key did: "with the power key", "lid closed"
	shutdown *Request // the last shutdown asked for
	sleep    *Request // the last sleep asked for, until the system goes to sleep
	sleepAt  time.Time
}

// slept returns who or what asked for the sleep that starts at this time, nil if the journal
// doesn't say.
func (q *requests) slept(at time.Time) *Request {
	r := q.sleep
	q.sleep = nil
	if r == nil || at.Sub(q.sleepAt) > sleepLag {
		return nil
	}
	return r
}

func (q *requests) feed(e journal.Entry) {
	switch e.Source {
	case "sudo":
		if m := reSudo.FindStringSubmatch(e.Message); m != nil {
			q.sudoAt, q.sudoUser, q.sudoCmd = e.Time, m[1], strings.Fields(m[2])
			if len(q.sudoCmd) > 0 {
				q.sudoCmd[0] = path.Base(q.sudoCmd[0])
			}
		}
	case "systemd-logind":
		q.logind(e)
	}
	if m := sessionUser.FindStringSubmatch(e.Message); m != nil {
		if uid, err := strconv.Atoi(m[2]); err == nil {
			if q.uids == nil {
				q.uids = map[int]string{}
			}
			q.uids[uid] = m[1]
		}
	}
}

func (q *requests) logind(e journal.Entry) {
	msg := e.Message
	switch {
	case reNewSession.MatchString(msg):
		m := reNewSession.FindStringSubmatch(msg)
		if q.sessions == nil {
			q.sessions = map[string]session{}
		}
		q.sessions[m[1]] = session{m[2], m[3]}
	case keys[msg] != "":
		q.key, q.keyAt = keys[msg], e.Time
	case msg == "Powering off..." || msg == "Rebooting..." || msg == "Halting...":
		if !q.keyAt.IsZero() && e.Time.Sub(q.keyAt) <= requestLag {
			q.shutdown = &Request{How: q.key}
		}
	case msg == "Suspending..." || msg == "Hibernating..." || msg == "Performing hybrid sleep..." ||
		msg == "Suspending, then hibernating...":
		if !q.keyAt.IsZero() && e.Time.Sub(q.keyAt) <= requestLag {
			q.sleep, q.sleepAt = &Request{How: q.key}, e.Time
		}
	case reScheduled.MatchString(msg):
		r := &Request{Scheduled: e.Time}
		q.withSudo(r, e.Time, "shutdown")
		q.shutdown = r
	case msg == "System shutdown has been cancelled":
		if q.shutdown != nil && !q.shutdown.Scheduled.IsZero() {
			q.shutdown = nil
		}
	default:
		m := reRequested.FindStringSubmatch(msg)
		if m == nil {
			return
		}
		switch m[1] {
		case "poweroff", "reboot", "halt", "kexec":
			q.shutdown = q.client(e.Time, m[1], m[2], m[3])
		case "suspend", "hibernate", "hybrid-sleep", "suspend-then-hibernate":
			q.sleep, q.sleepAt = q.client(e.Time, m[1], m[2], m[3]), e.Time
		}
	}
}

// session is a login session, as logind logs it.
type session struct {
	user  string
	class string // "user", "greeter"…; "" before systemd 258
}

// client says who asked logind for this action, from the program, its unit and a sudo just before.
func (q *requests) client(at time.Time, action, comm, unit string) *Request {
	r := &Request{}
	switch {
	case greeters[comm]:
		r.How = "from the login screen (" + comm + ")"
		return r
	case comm == "upowerd": // the critical battery action
		r.How = "battery critical"
		return r
	case comm == "systemctl" || comm == "loginctl":
		r.How = "with " + comm + " " + action
	case comm == "shutdown" || comm == "reboot" || comm == "poweroff" || comm == "halt":
		r.How = "with " + comm
	case strings.HasSuffix(unit, ".service") && !reUserUnit.MatchString(unit) && !strings.HasPrefix(unit, "run-"):
		r.How = "by " + strings.TrimSuffix(unit, ".service") // the program's name is cut to 15 characters
		return r
	case comm != "":
		r.How = "with " + comm
	}
	if m := reUserUnit.FindStringSubmatch(unit); m != nil {
		uid, _ := strconv.Atoi(m[1])
		r.User = q.uids[uid]
	} else if m := reSession.FindStringSubmatch(unit); m != nil {
		s := q.sessions[m[1]]
		if s.class == "greeter" {
			return &Request{How: "from the login screen"}
		}
		r.User = s.user
	}
	q.withSudo(r, at, comm)
	return r
}

// withSudo puts the sudo command just before in the request, when it ran this program.
func (q *requests) withSudo(r *Request, at time.Time, comm string) {
	if len(q.sudoCmd) == 0 || q.sudoCmd[0] != comm || at.Sub(q.sudoAt) > requestLag || at.Before(q.sudoAt) {
		return
	}
	r.How, r.User = "with sudo "+strings.Join(q.sudoCmd, " "), q.sudoUser
}
