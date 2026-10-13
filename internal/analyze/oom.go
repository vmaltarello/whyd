package analyze

import (
	"cmp"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/vmaltarello/whyd/internal/rules"
)

// OOMReport is what the kernel's out-of-memory report says about where the memory went.
type OOMReport struct {
	AnonKB     int64 // active + inactive anonymous memory (what processes allocated)
	ShmemKB    int64 // shared memory and tmpfs
	GPUKB      int64 // GPU memory in system RAM (gpu_active), -1 when the kernel didn't report it
	SwapFreeKB int64
	SwapKB     int64
	Killed     []Killed  // every process killed in the event
	Top        []Process // biggest processes at the first report, by resident memory
}

type Killed struct{ Name, PID string }

type Process struct {
	Name  string
	RSSKB int64
}

var (
	reActiveAnon   = regexp.MustCompile(`\bactive_anon:(\d+)kB`)
	reInactiveAnon = regexp.MustCompile(`\binactive_anon:(\d+)kB`)
	reShmem        = regexp.MustCompile(`\bshmem:(\d+)kB`)
	reGPU          = regexp.MustCompile(`\bgpu_active:(\d+)kB`)
	reSwapFree     = regexp.MustCompile(`^Free swap\s*=\s*(\d+)kB`)
	reSwapTotal    = regexp.MustCompile(`^Total swap\s*=\s*(\d+)kB`)
	reKill         = regexp.MustCompile(`Out of memory: Killed process`)
	// [  pid  ]   uid  tgid total_vm      rss rss_anon rss_file rss_shmem pgtables_bytes swapents oom_score_adj name
	// (kernels before 6.1 have no rss_anon, rss_file and rss_shmem: three numbers fewer)
	reTask = regexp.MustCompile(`^\[\s*\d+\]\s+\d+\s+\d+\s+\d+\s+(\d+)(?:\s+-?\d+){6}\s+(.+)$|^\[\s*\d+\]\s+\d+\s+\d+\s+\d+\s+(\d+)(?:\s+-?\d+){3}\s+(\D.*)$`)
)

// the task list counts memory in pages; the size of this machine's is the best guess for a file
var pageKB = int64(os.Getpagesize() / 1024)

// parseOOM reads the first out-of-memory report of an event (its lines up to the first kill),
// whatever rule took them: nil when the event doesn't hold one.
func parseOOM(ev *rules.Event) *OOMReport {
	if len(ev.Entries) == 0 || !strings.Contains(ev.Entries[0].Message, " invoked oom-killer") {
		return nil
	}
	r := &OOMReport{GPUKB: -1}
	num := func(re *regexp.Regexp, s string) (int64, bool) {
		m := re.FindStringSubmatch(s)
		if m == nil {
			return 0, false
		}
		n, err := strconv.ParseInt(m[1], 10, 64)
		return n, err == nil
	}
	biggest := map[string]int64{} // program name -> largest resident memory among its processes
	for _, e := range ev.Entries {
		msg := e.Message
		if reKill.MatchString(msg) {
			break // the first report ends at its kill line
		}
		if n, ok := num(reActiveAnon, msg); ok && r.AnonKB == 0 {
			// "Node 0 active_anon:…kB inactive_anon:…kB … shmem:…kB … gpu_active:…kB"
			in, _ := num(reInactiveAnon, msg)
			r.AnonKB = n + in
			r.ShmemKB, _ = num(reShmem, msg)
			if g, ok := num(reGPU, msg); ok {
				r.GPUKB = g
			}
		}
		if n, ok := num(reSwapFree, msg); ok {
			r.SwapFreeKB = n
		}
		if n, ok := num(reSwapTotal, msg); ok {
			r.SwapKB = n
		}
		if m := reTask.FindStringSubmatch(msg); m != nil {
			rss, name := m[1], m[2]
			if rss == "" {
				rss, name = m[3], m[4]
			}
			pages, _ := strconv.ParseInt(rss, 10, 64)
			biggest[name] = max(biggest[name], pages*pageKB)
		}
	}
	for name, kb := range biggest {
		r.Top = append(r.Top, Process{name, kb})
	}
	for _, s := range ev.Samples {
		r.Killed = append(r.Killed, Killed{s["process"], s["pid"]})
	}
	slices.SortFunc(r.Top, func(a, b Process) int {
		return cmp.Or(cmp.Compare(b.RSSKB, a.RSSKB), cmp.Compare(a.Name, b.Name))
	})
	if len(r.Top) > 5 {
		r.Top = r.Top[:5]
	}
	if r.AnonKB == 0 && len(r.Top) == 0 { // the first line alone: no report to read
		return nil
	}
	return r
}
