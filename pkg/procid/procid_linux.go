//go:build linux

package procid

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Default is the real Linux backend.
var Default Reader = linuxReader{}

type linuxReader struct{}

func (linuxReader) MachineID() (string, error) {
	b, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (linuxReader) BootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// PIDStartTicks reads field 22 (starttime) of /proc/<pid>/stat. The comm
// field (field 2) is parenthesized and may itself contain spaces or `)`,
// so this splits on the LAST `)` in the line before field-splitting the
// remainder (see man proc, "stat" — comm field caveat). Field 3 (state)
// is then index 0 of that remainder, so field 22 is index 22-3=19.
func (linuxReader) PIDStartTicks(pid int) (int64, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	line := string(b)
	i := strings.LastIndex(line, ")")
	if i < 0 {
		return 0, fmt.Errorf("procid: malformed /proc/%d/stat: no comm terminator", pid)
	}
	fields := strings.Fields(line[i+1:])
	const startTimeIndex = 22 - 3 // field 22, 0-based from field 3
	if len(fields) <= startTimeIndex {
		return 0, fmt.Errorf("procid: malformed /proc/%d/stat: too few fields", pid)
	}
	ticks, err := strconv.ParseInt(fields[startTimeIndex], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("procid: parsing /proc/%d/stat starttime: %w", pid, err)
	}
	return ticks, nil
}
