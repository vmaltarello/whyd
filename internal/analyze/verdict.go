package analyze

import (
	"fmt"
	"strings"
	"time"

	"github.com/vmaltarello/whyd/internal/journal"
)

// EndKind is how a boot ended.
type EndKind int

const (
	EndRunning  EndKind = iota // the current boot
	EndPowerOff                // clean power-off
	EndReboot                  // clean reboot
	EndShutdown                // clean, other target (halt, kexec)
	EndAsleep                  // went to sleep and never woke up
	EndPanic                   // kernel panic recorded
	EndUnclean                 // the journal just stops
)

// the names say what happened in words anyone uses, not in systemd's: "abrupt end", not "no
// shutdown target reached"
var endNames = [...]string{"running", "shut down", "restarted", "shut down", "never woke up", "kernel panic", "abrupt end"}

func (k EndKind) String() string { return endNames[k] }

// Clean tells whether the system shut down properly.
func (k EndKind) Clean() bool { return k == EndPowerOff || k == EndReboot || k == EndShutdown }

// Interval is a span of time asleep: suspended, or hibernated when Hibernate is set.
type Interval struct {
	From, To  time.Time
	Hibernate bool
}

// Verdict is how the boot ended, with a one-line explanation.
type Verdict struct {
	Kind    EndKind
	Summary string   // never names the user: outputs add By.User where it can be shown
	By      *Request // who or what asked for a clean end, nil if the journal doesn't say
}

// "Journal stopped" counts as the end only within this many lines of the last entry, and within
// TailKeep of it: the lines before are a selection (journal.Selection), they can't be counted
const stopWindow = 60

// endState follows the markers that tell how a boot ended.
type endState struct {
	stoppedAt    int // line number of the last "Journal stopped", 0 if none
	stoppedTime  time.Time
	target       string // last shutdown target reached: "poweroff", "reboot", "shutdown"
	sleeping     bool   // a suspend or hibernation entry without its exit yet
	hibernate    bool   // the open sleep is a hibernation
	sleepStart   time.Time
	suspends     int
	hibernations int
	asleep       time.Duration
	sleeps       []Interval
	panic        string
	lines        int
	req          requests
}

func (s *endState) feed(e journal.Entry, line int) {
	s.lines = line
	s.req.feed(e)
	switch {
	case e.Source == "systemd-journald" && e.Message == "Journal stopped":
		s.stoppedAt, s.stoppedTime = line, e.Time
	case e.Source == "systemd" && strings.HasPrefix(e.Message, "Reached target "):
		switch strings.TrimSuffix(strings.TrimPrefix(e.Message, "Reached target "), ".") {
		case "System Power Off":
			s.target = "poweroff"
		case "System Reboot":
			s.target = "reboot"
		case "System Halt", "Kexec":
			s.target = "shutdown"
		}
	case e.Kernel && strings.HasPrefix(e.Message, "PM: suspend entry"):
		s.sleeping, s.hibernate, s.sleepStart = true, false, e.Time
		s.suspends++
	case e.Kernel && strings.HasPrefix(e.Message, "PM: hibernation: hibernation entry"):
		s.sleeping, s.hibernate, s.sleepStart = true, true, e.Time
		s.hibernations++
	case e.Kernel && (strings.HasPrefix(e.Message, "PM: suspend exit") ||
		strings.HasPrefix(e.Message, "PM: hibernation: hibernation exit")):
		if s.sleeping {
			s.asleep += e.Time.Sub(s.sleepStart)
			s.sleeps = append(s.sleeps, Interval{s.sleepStart, e.Time, s.hibernate})
		}
		s.sleeping = false
	case e.Kernel && strings.HasPrefix(e.Message, "Kernel panic - not syncing: "):
		s.panic = strings.TrimPrefix(e.Message, "Kernel panic - not syncing: ")
	}
}

// verdict says how the boot ended. Summary starts with the kind ("abrupt end, journal stops at …")
// so outputs can colour that first word and print the rest as is.
func (s *endState) verdict(r *Result) Verdict {
	end := r.Boot.Last.Format("15:04:05")
	switch {
	case r.Current:
		return Verdict{EndRunning, "running", nil}
	case s.stoppedAt > 0 && s.lines-s.stoppedAt < stopWindow && r.Boot.Last.Sub(s.stoppedTime) <= TailKeep:
		by := s.req.shutdown
		switch s.target {
		case "poweroff":
			return Verdict{EndPowerOff, "shut down at " + end + by.Words(false), by}
		case "reboot":
			return Verdict{EndReboot, "restarted at " + end + by.Words(false), by}
		}
		return Verdict{EndShutdown, "shut down at " + end + by.Words(false), by}
	case s.panic != "": // before "asleep": a panic while suspending or resuming is still a panic
		return Verdict{EndPanic, "kernel panic: " + s.panic, nil}
	case s.sleeping && s.hibernate:
		return Verdict{EndAsleep, fmt.Sprintf("never woke up: hibernated at %s and never resumed",
			s.sleepStart.Format("15:04")), nil}
	case s.sleeping:
		return Verdict{EndAsleep, fmt.Sprintf("never woke up: suspended at %s and never resumed",
			s.sleepStart.Format("15:04")), nil}
	}
	sum := "abrupt end, journal stops at " + end
	if g := r.Gap(); g > 0 {
		sum += ", next boot after " + Duration(g)
	}
	return Verdict{EndUnclean, sum, nil}
}
