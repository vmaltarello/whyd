package analyze

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Machine is the hardware the session ran on, as the kernel described it at boot: the
// session's own journal, never the machine whyd runs on, so a journal recorded elsewhere
// describes its own machine.
type Machine struct {
	Model    string   // "HUAWEI KLVL-WXX9", from the DMI line
	CPU      string   // "AMD Ryzen 5 4600H"
	RAMKB    int64    // physical memory the kernel counted
	Graphics []string // graphics drivers: "amdgpu", "i915", "nvidia-drm"
}

// RAM is the memory in whole gigabytes, as sold: the kernel counts a little less than installed.
func (m Machine) RAM() string {
	if m.RAMKB <= 0 {
		return ""
	}
	return strconv.Itoa(int(math.Ceil(float64(m.RAMKB)/1024/1024))) + " GB"
}

var (
	// "Memory: 15554792K/16119108K available (…)"
	memoryLine = regexp.MustCompile(`^Memory: \d+K/(\d+)K available`)
	// "[drm] Initialized amdgpu 3.64.0 for 0000:04:00.0 on minor 1"
	drmLine = regexp.MustCompile(`^\[drm\] Initialized (\S+) `)
	// what CPU names carry that says nothing: "(R)", "(TM)", "CPU @ 2.80GHz", "with Radeon Graphics"
	cpuNoise = regexp.MustCompile(`\((R|TM|r|tm)\)|\s+CPU\b|\s+@\s+\S+|\s+with\s+.*Graphics|\s+\d+-Core Processor|\s+Processor`)
)

// framebuffer drivers used before the real one loads: not the graphics card
var bootDisplay = map[string]bool{"simpledrm": true, "efidrm": true, "vesadrm": true, "ofdrm": true}

// feed reads a kernel line logged at boot.
func (m *Machine) feed(msg string) {
	switch {
	case strings.HasPrefix(msg, "DMI: ") && m.Model == "":
		v := strings.TrimPrefix(msg, "DMI: ")
		if strings.HasPrefix(v, "SMBIOS ") || strings.HasPrefix(v, "Memory slots") {
			return
		}
		v, _, _ = strings.Cut(v, ", BIOS ")
		v, _, _ = strings.Cut(v, "/") // vendor and product; the board name follows
		m.Model = strings.TrimSpace(v)
	case strings.HasPrefix(msg, "smpboot: CPU0: ") && m.CPU == "":
		v, _, _ := strings.Cut(strings.TrimPrefix(msg, "smpboot: CPU0: "), " (family")
		m.CPU = strings.Join(strings.Fields(cpuNoise.ReplaceAllString(v, "")), " ")
	case strings.HasPrefix(msg, "Memory: ") && m.RAMKB == 0:
		if s := memoryLine.FindStringSubmatch(msg); s != nil {
			m.RAMKB, _ = strconv.ParseInt(s[1], 10, 64)
		}
	case strings.HasPrefix(msg, "[drm] Initialized "):
		if s := drmLine.FindStringSubmatch(msg); s != nil && !bootDisplay[s[1]] && !slices.Contains(m.Graphics, s[1]) {
			m.Graphics = append(m.Graphics, s[1])
		}
	}
}

// Line is the machine on one line: "HUAWEI KLVL-WXX9, AMD Ryzen 5 4600H, 16 GB RAM, amdgpu".
func (m Machine) Line() string {
	var parts []string
	for _, p := range []string{m.Model, m.CPU} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if r := m.RAM(); r != "" {
		parts = append(parts, r+" RAM")
	}
	if len(m.Graphics) > 0 {
		parts = append(parts, strings.Join(m.Graphics, " + "))
	}
	return strings.Join(parts, ", ")
}
