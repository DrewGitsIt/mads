package procid

import (
	"fmt"
	"regexp"
)

// The darwin backend's pure helpers live in this untagged file so they are
// unit-tested on Linux too; only the syscall/exec glue is darwin-only.

var ioregPlatformUUID = regexp.MustCompile(`(?m)^\s*"IOPlatformUUID"\s*=\s*"([^"]+)"`)

// parseIOPlatformUUID extracts IOPlatformUUID from `ioreg -rd1 -c
// IOPlatformExpertDevice` output. A missing or empty value is an error,
// never "", so Current can't record a blank machine ID.
func parseIOPlatformUUID(out []byte) (string, error) {
	m := ioregPlatformUUID.FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("procid: IOPlatformUUID not found in ioreg output")
	}
	return string(m[1]), nil
}

// timevalMicros converts a kinfo_proc p_starttime timeval to microseconds
// since the epoch — darwin's PIDStartTicks value.
func timevalMicros(sec, usec int64) int64 {
	return sec*1_000_000 + usec
}
