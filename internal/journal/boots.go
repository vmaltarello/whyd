// Package journal reads the systemd journal by running journalctl and decoding its output.
package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Boot is one boot recorded in the journal.
type Boot struct {
	Index int       // same numbering as journalctl -b: 0 = current, -1 = previous, …
	ID    string    // boot ID (_BOOT_ID in journal entries)
	First time.Time // first entry of the boot
	Last  time.Time // last entry of the boot
}

// Duration is the time between the first and the last entry of the boot.
func (b Boot) Duration() time.Duration {
	return b.Last.Sub(b.First)
}

// rawBoot mirrors one object of `journalctl --list-boots -o json`.
// Timestamps are microseconds since the Unix epoch.
type rawBoot struct {
	Index      int    `json:"index"`
	BootID     string `json:"boot_id"`
	FirstEntry int64  `json:"first_entry"`
	LastEntry  int64  `json:"last_entry"`
}

// ListBoots returns every boot in the journal, oldest first.
func ListBoots(ctx context.Context) ([]Boot, error) {
	out, err := exec.CommandContext(ctx, "journalctl", "--list-boots", "-o", "json", "--no-pager").Output()
	if err != nil {
		// on a non-zero exit, journalctl explains why on stderr: keep it in the error
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("journalctl --list-boots: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("journalctl --list-boots: %w", err)
	}
	if t := bytes.TrimSpace(out); len(t) > 0 && t[0] != '[' {
		// systemd before 251 ignores -o json here and prints its table
		return bootsFromTable(ctx, out)
	}
	return parseBoots(out)
}

// a row of the --list-boots table: " -1 2449e69564ba4aecbc8f807e0971b698 Thu 2026-10-08 …"
var bootRow = regexp.MustCompile(`^\s*(-?\d+)\s+([0-9a-f]{32})\s`)

// parseBootTable reads the index and ID of each boot from the --list-boots table.
func parseBootTable(out []byte) []Boot {
	var boots []Boot
	for line := range strings.SplitSeq(string(out), "\n") {
		if m := bootRow.FindStringSubmatch(line); m != nil {
			idx, _ := strconv.Atoi(m[1])
			boots = append(boots, Boot{Index: idx, ID: m[2]})
		}
	}
	return boots
}

// bootsFromTable completes the boots of the table with their first and last entry, asking
// journalctl for each: the dates in the table depend on the locale and the time zone.
func bootsFromTable(ctx context.Context, out []byte) ([]Boot, error) {
	boots := parseBootTable(out)
	if len(boots) == 0 {
		return nil, fmt.Errorf("journalctl --list-boots: no boots in its output")
	}
	errStop := errors.New("stop")
	for i := range boots {
		b := &boots[i]
		first := func(e Entry) error { b.First = e.Time; return errStop }
		if err := export(ctx, first, "-b", b.ID, "--output-fields=PRIORITY"); err != nil && !errors.Is(err, errStop) {
			return nil, err
		}
		last := func(e Entry) error { b.Last = e.Time; return nil }
		if err := export(ctx, last, "-b", b.ID, "-n", "1", "--output-fields=PRIORITY"); err != nil {
			return nil, err
		}
	}
	return boots, nil
}

// parseBoots decodes the output of `journalctl --list-boots -o json`.
// It is separate from ListBoots so tests can feed it a file instead of running journalctl.
func parseBoots(data []byte) ([]Boot, error) {
	var raw []rawBoot
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decoding boot list: %w", err)
	}
	boots := make([]Boot, 0, len(raw))
	for _, r := range raw {
		boots = append(boots, Boot{
			Index: r.Index,
			ID:    r.BootID,
			First: time.UnixMicro(r.FirstEntry),
			Last:  time.UnixMicro(r.LastEntry),
		})
	}
	return boots, nil
}
