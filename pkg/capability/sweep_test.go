package capability

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// deadPID returns the PID of a process that has already exited and been
// reaped, so it reads as not alive (barring immediate PID reuse).
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func writeAged(t *testing.T, path, content string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestSweepStaleRemovesOnlyOldDeadPidFiles(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	dead := strconv.Itoa(deadPID(t))
	live := strconv.Itoa(os.Getpid())

	files := map[string]struct {
		content string
		age     time.Duration
		kept    bool
	}{
		"old-dead.pid":    {dead, time.Hour, false},
		"old-empty.pid":   {"", time.Hour, false}, // claim that never got a PID
		"old-garbage.pid": {"not-a-pid", time.Hour, false},
		"old-live.pid":    {live, time.Hour, true},
		"fresh-dead.pid":  {dead, 0, true},         // inside the minAge window
		"fresh-empty.pid": {"", 0, true},           // a claim mid-EnsureStarted
		"notes.txt":       {dead, time.Hour, true}, // not a pid file
	}
	for name, f := range files {
		writeAged(t, filepath.Join(dir, name), f.content, f.age)
	}

	n, err := reg.SweepStale(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("removed %d files, want 3", n)
	}
	for name, f := range files {
		_, err := os.Stat(filepath.Join(dir, name))
		if f.kept && err != nil {
			t.Errorf("%s should be kept: %v", name, err)
		}
		if !f.kept && !os.IsNotExist(err) {
			t.Errorf("%s should be removed, stat err = %v", name, err)
		}
	}
}

func TestSweepStaleMissingDirIsNotAnError(t *testing.T) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "never-created"))
	n, err := reg.SweepStale(time.Minute)
	if err != nil || n != 0 {
		t.Fatalf("SweepStale on a missing dir = (%d, %v), want (0, nil)", n, err)
	}
}
