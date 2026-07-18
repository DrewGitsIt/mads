//go:build !linux

package procid

import "fmt"

// Default is the non-Linux backend: every method errors, collapsing
// "definitely not Linux" and "can't read /proc" into the same
// not-provable outcome (rule 4's non-Linux clause) for free.
var Default Reader = otherReader{}

type otherReader struct{}

func (otherReader) MachineID() (string, error) {
	return "", fmt.Errorf("procid: not supported on this platform")
}

func (otherReader) BootID() (string, error) {
	return "", fmt.Errorf("procid: not supported on this platform")
}

func (otherReader) PIDStartTicks(pid int) (int64, error) {
	return 0, fmt.Errorf("procid: not supported on this platform")
}
