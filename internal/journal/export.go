package journal

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ReadExport decodes journalctl's export format from r, calling fn for each entry. It is faster
// than JSON for journalctl to write and for whyd to read: entries are "FIELD=value" lines ending
// with an empty line; a value with a newline or that isn't text is written as "FIELD\n", its
// length as 64-bit little-endian, the bytes, "\n". A field that appears twice keeps the first.
func ReadExport(r io.Reader, fn func(Entry) error) error {
	br := bufio.NewReaderSize(r, 256*1024)
	var e exportEntry
	e.Priority = -1
	seen := 0 // bit set of the fields already read in this entry
	n := 0
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) { // a very long plain line
			long := append([]byte(nil), line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = br.ReadSlice('\n')
				long = append(long, line...)
			}
			line = long
		}
		if err == io.EOF && len(line) == 0 {
			if seen != 0 {
				return finish(&e, fn)
			}
			return nil
		}
		if err != nil && err != io.EOF {
			return err
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		if len(line) == 0 { // end of the entry
			n++
			if seen != 0 {
				if err := finish(&e, fn); err != nil {
					return err
				}
			}
			e, seen = exportEntry{Entry: Entry{Priority: -1}, lastHost: e.lastHost}, 0
			continue
		}
		rawName, value, plain := bytes.Cut(line, []byte{'='})
		bit, known := exportFields[string(rawName)] // before reading on: line points into the buffer
		if !plain {                                 // binary field: length, data, newline
			var size [8]byte
			if _, err := io.ReadFull(br, size[:]); err != nil {
				return fmt.Errorf("export entry %d: %w", n+1, err)
			}
			data := make([]byte, binary.LittleEndian.Uint64(size[:]))
			if _, err := io.ReadFull(br, data); err != nil {
				return fmt.Errorf("export entry %d: %w", n+1, err)
			}
			if _, err := br.ReadByte(); err != nil {
				return fmt.Errorf("export entry %d: %w", n+1, err)
			}
			value = data
		}
		if !known || seen&bit != 0 {
			continue
		}
		seen |= bit
		set(&e, bit, value)
	}
}

// the fields whyd reads, as bits of a set
var exportFields = map[string]int{
	"MESSAGE": 1 << 0, "PRIORITY": 1 << 1, "SYSLOG_IDENTIFIER": 1 << 2, "_SYSTEMD_UNIT": 1 << 3,
	"_TRANSPORT": 1 << 4, "_PID": 1 << 5, "_COMM": 1 << 6, "__REALTIME_TIMESTAMP": 1 << 7,
	"_HOSTNAME": 1 << 8,
}

type exportEntry struct {
	Entry
	comm     string
	lastHost string // kept from one entry to the next
}

func set(e *exportEntry, bit int, v []byte) {
	switch bit {
	case 1 << 0:
		e.Message = strings.ToValidUTF8(string(v), "�")
	case 1 << 1:
		if p, err := strconv.Atoi(string(v)); err == nil {
			e.Priority = p
		}
	case 1 << 2:
		e.Source = string(v)
	case 1 << 3:
		e.Unit = string(v)
	case 1 << 4:
		e.Kernel = string(v) == "kernel"
	case 1 << 5:
		if pid, err := strconv.Atoi(string(v)); err == nil {
			e.PID = pid
		}
	case 1 << 6:
		e.comm = string(v)
	case 1 << 7:
		if us, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			e.Time = time.UnixMicro(us)
		}
	case 1 << 8:
		if string(v) == e.lastHost { // the same value on every line: share one string
			e.Host = e.lastHost
		} else {
			e.Host = string(v)
			e.lastHost = e.Host
		}
	}
}

func finish(e *exportEntry, fn func(Entry) error) error {
	if e.Source == "" { // _COMM only when there is no SYSLOG_IDENTIFIER, as in JSON
		e.Source = e.comm
	}
	return fn(e.Entry)
}
