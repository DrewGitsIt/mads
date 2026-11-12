package runlogs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkLog creates runsDir/<rel> as a log file and backdates it, and every
// directory on the way to it, by age.
func mkLog(t *testing.T, runsDir, rel string, age time.Duration) {
	t.Helper()
	path := filepath.Join(runsDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	for p := path; p != runsDir; p = filepath.Dir(p) {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSweepRemovesExpiredRunsWhole(t *testing.T) {
	runs := t.TempDir()
	day := 24 * time.Hour
	// ses_old: one run, entirely expired -> run and session dir both go.
	mkLog(t, runs, "ses_old/1/tsk_a/executor-attempt-1.log", 40*day)
	// ses_mixed: run 1 expired, run 2 recent -> only run 1 goes.
	mkLog(t, runs, "ses_mixed/1/tsk_a/executor-attempt-1.log", 40*day)
	mkLog(t, runs, "ses_mixed/2/tsk_a/executor-attempt-1.log", 2*day)
	// ses_partial: one old log and one fresh log in the same run -> the run
	// is still live, nothing goes.
	mkLog(t, runs, "ses_partial/1/tsk_a/architect-attempt-1.log", 40*day)
	mkLog(t, runs, "ses_partial/1/tsk_b/executor-attempt-1.log", time.Minute)
	// A stray file at the top level is never touched.
	mkLog(t, runs, "README", 400*day)

	n, err := Sweep(runs, 30*day, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("removed %d runs, want 2", n)
	}
	for path, want := range map[string]bool{
		"ses_old":     false,
		"ses_mixed/1": false,
		"ses_mixed/2/tsk_a/executor-attempt-1.log":    true,
		"ses_partial/1/tsk_a/architect-attempt-1.log": true,
		"ses_partial/1/tsk_b/executor-attempt-1.log":  true,
		"README": true,
	} {
		if got := exists(filepath.Join(runs, path)); got != want {
			t.Errorf("%s exists = %v, want %v", path, got, want)
		}
	}
}

func TestSweepMissingRunsDirIsNotAnError(t *testing.T) {
	n, err := Sweep(filepath.Join(t.TempDir(), "runs"), time.Hour, time.Now())
	if err != nil || n != 0 {
		t.Fatalf("Sweep on a missing dir = (%d, %v), want (0, nil)", n, err)
	}
}
