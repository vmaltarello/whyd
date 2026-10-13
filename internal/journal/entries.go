package journal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Entry is one journal line, normalised: the only shape later stages see.
type Entry struct {
	Time     time.Time
	Priority int    // 0 emerg … 3 err, 4 warning … 7 debug; -1 when missing
	Source   string // SYSLOG_IDENTIFIER, else _COMM: "kernel", "systemd", "sunshine"…
	Unit     string // _SYSTEMD_UNIT, may be empty
	PID      int    // 0 when missing
	Kernel   bool   // logged by the kernel (_TRANSPORT=kernel)
	Host     string // _HOSTNAME, may be empty in recorded journals
	Message  string
}

// fields asked from journalctl; the timestamps, cursor and boot ID always come along
var outputFields = "MESSAGE,PRIORITY,SYSLOG_IDENTIFIER,_SYSTEMD_UNIT,_TRANSPORT,_PID,_COMM,_HOSTNAME"

// Selection is the part of a boot an analysis needs. Most lines of a long boot are routine
// messages of a chatty program, and journalctl's time is per line: reading only the useful ones
// is several times faster.
type Selection struct {
	Matches []string      // "FIELD=value": lines matching any of them, besides warnings and errors
	Recent  time.Duration // and every line in this time before the end of the boot
}

// ReadBoot streams the entries of a boot, oldest first, calling fn for each one: the whole boot,
// or with a selection the warnings and errors, the lines of sel.Matches and everything in
// sel.Recent before the end. It asks for the export format: faster than JSON to write and to
// read, and never shortens long fields (JSON replaces fields over 4096 bytes with null unless
// --all is given, losing crash stack traces). The boot is selected by its ID, so the index
// shifting after a reboot can't mix them up.
func ReadBoot(ctx context.Context, b Boot, sel *Selection, fn func(Entry) error) error {
	fields := "--output-fields=" + outputFields
	if sel == nil || b.Last.IsZero() {
		return export(ctx, fn, "-b", b.ID, fields)
	}
	cut := b.Last.Add(-sel.Recent)
	// before the cut: the selected lines (same field: or; "+" between fields: or), then all after
	args := []string{
		"-b", b.ID, fields, "--until", stamp(cut.Add(-time.Microsecond)),
		"PRIORITY=0", "PRIORITY=1", "PRIORITY=2", "PRIORITY=3", "PRIORITY=4",
	}
	byField := map[string][]string{}
	var order []string
	for _, m := range sel.Matches {
		f, _, _ := strings.Cut(m, "=")
		if byField[f] == nil {
			order = append(order, f)
		}
		byField[f] = append(byField[f], m)
	}
	for _, f := range order {
		args = append(append(args, "+"), byField[f]...)
	}
	if err := export(ctx, fn, args...); err != nil {
		return err
	}
	return export(ctx, fn, "-b", b.ID, fields, "--since", stamp(cut))
}

// stamp writes a time as journalctl reads it to the microsecond: "@1791578926.671952".
func stamp(t time.Time) string {
	return fmt.Sprintf("@%d.%06d", t.Unix(), t.Nanosecond()/1000)
}

// export runs journalctl with the export format and these arguments, calling fn for each entry.
// When fn returns an error, journalctl is stopped and the error returned.
func export(ctx context.Context, fn func(Entry) error, args ...string) error {
	args = append([]string{"-o", "export", "--no-pager"}, args...)
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("journalctl: %w", err)
	}
	readErr := ReadExport(out, fn)
	if readErr != nil {
		// stop journalctl early, its exit error is not interesting then
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return readErr
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("journalctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Read decodes journalctl JSON output (one object per line) from r, calling fn for each entry:
// recorded journals, for tests and --file. The entries must all come from one boot: a whole
// journal exported without -b would read as one endless session.
func Read(r io.Reader, fn func(Entry) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // messages with stack traces can be long
	line := 0
	boot, host := "", ""
	for sc.Scan() {
		line++
		e, id, err := decodeEntry(sc.Bytes())
		if err != nil {
			return fmt.Errorf("journal line %d: %w", line, err)
		}
		if boot == "" {
			boot = id
		} else if id != "" && id != boot {
			return fmt.Errorf("the file holds more than one boot (from line %d on, another boot ID): "+
				"record a single one, for example with journalctl -b -1 -o json --all", line)
		}
		if e.Host == host { // the same few values on every line: share one string
			e.Host = host
		} else {
			host = e.Host
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return sc.Err()
}

// rawEntry holds the fields whyd reads, still encoded: decoding into a struct instead of a map
// and reading plain strings without a second pass makes this about twice as fast.
type rawEntry struct {
	Message   json.RawMessage `json:"MESSAGE"`
	Priority  json.RawMessage `json:"PRIORITY"`
	Ident     json.RawMessage `json:"SYSLOG_IDENTIFIER"`
	Unit      json.RawMessage `json:"_SYSTEMD_UNIT"`
	Transport json.RawMessage `json:"_TRANSPORT"`
	PID       json.RawMessage `json:"_PID"`
	Comm      json.RawMessage `json:"_COMM"`
	Host      json.RawMessage `json:"_HOSTNAME"`
	BootID    json.RawMessage `json:"_BOOT_ID"`
	Realtime  json.RawMessage `json:"__REALTIME_TIMESTAMP"`
}

// decodeEntry decodes one JSON line into an entry and the ID of its boot ("" if missing).
func decodeEntry(data []byte) (Entry, string, error) {
	var raw rawEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return Entry{}, "", err
	}
	e := Entry{
		Priority: -1,
		Source:   fieldValue(raw.Ident),
		Unit:     fieldValue(raw.Unit),
		Kernel:   fieldValue(raw.Transport) == "kernel",
		Host:     fieldValue(raw.Host),
		Message:  fieldValue(raw.Message),
	}
	if e.Source == "" {
		e.Source = fieldValue(raw.Comm)
	}
	if us, err := strconv.ParseInt(fieldValue(raw.Realtime), 10, 64); err == nil {
		e.Time = time.UnixMicro(us)
	}
	if p, err := strconv.Atoi(fieldValue(raw.Priority)); err == nil {
		e.Priority = p
	}
	if pid, err := strconv.Atoi(fieldValue(raw.PID)); err == nil {
		e.PID = pid
	}
	return e, fieldValue(raw.BootID), nil
}

// fieldValue returns a journal field as text. journalctl writes a value as a string, as an array of
// bytes when it isn't valid UTF-8, and as an array of values when the field appears more than once
// (then the first one is used).
func fieldValue(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	// most values are plain strings without escapes: no need to decode them
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' && bytes.IndexByte(v, '\\') < 0 {
		return string(v[1 : len(v)-1])
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	var b []byte
	var nums []int
	if json.Unmarshal(v, &nums) == nil {
		b = make([]byte, len(nums))
		for i, n := range nums {
			b[i] = byte(n)
		}
		return strings.ToValidUTF8(string(b), "�")
	}
	var many []json.RawMessage
	if json.Unmarshal(v, &many) == nil && len(many) > 0 {
		return fieldValue(many[0])
	}
	return ""
}
