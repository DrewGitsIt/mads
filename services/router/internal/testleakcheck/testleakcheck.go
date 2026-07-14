// Package testleakcheck lets a router test package's TestMain prove, after
// m.Run() returns, that no testchild process it spawned is still alive —
// turning a silent leak into a hard test failure instead of a growing pile
// of orphaned processes.
package testleakcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Verify scans /proc for live processes whose executable is exactly
// binPath and returns an error listing their PIDs if any are found. Call
// after m.Run() and before removing binPath's containing directory — once
// the binary file is deleted, /proc/<pid>/exe would resolve to
// "binPath (deleted)" instead of binPath, defeating the comparison.
func Verify(binPath string) error {
	entries, err := os.ReadDir("/proc")
	if os.IsNotExist(err) {
		return nil // non-Linux: nothing to check
	}
	if err != nil {
		return fmt.Errorf("testleakcheck: read /proc: %w", err)
	}

	var leaked []string
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a PID directory
		}
		exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			continue // process exited mid-scan or unreadable — not our concern
		}
		if exe == binPath {
			leaked = append(leaked, fmt.Sprintf("pid %d", pid))
		}
	}
	if len(leaked) > 0 {
		return fmt.Errorf("testleakcheck: %d testchild process(es) still running %s after test run: %v",
			len(leaked), binPath, leaked)
	}
	return nil
}
