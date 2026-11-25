// Package runlogs enforces Config.LogRetentionDays ("Keep logs for (days)"
// in Settings) on the pipeline stage output logs under $MADS_HOME/runs.
//
// Layout (services/pipeline/engine/outputlog.go):
//
//	runs/<session_id>/<run_seq>/<task_id>/<stage>-attempt-<n>.log
//
// The unit of retention is one run directory: it is removed once nothing in
// it has been written for longer than the retention period, so a run is
// always kept or dropped whole and a run still writing (its newest log
// touched seconds ago) is never a candidate. A session directory left empty
// is removed too. Nothing else under runs/ is touched.
package runlogs

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Sweep removes run directories under runsDir whose newest entry is older
// than maxAge, then any session directory that sweep left empty. It returns
// how many run directories it removed. A missing runsDir is not an error.
// Per-entry failures are skipped rather than aborting the sweep: one
// unreadable directory must not keep every other expired run on disk.
func Sweep(runsDir string, maxAge time.Duration, now time.Time) (int, error) {
	sessions, err := os.ReadDir(runsDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cutoff := now.Add(-maxAge)
	removed := 0
	for _, s := range sessions {
		if !s.IsDir() {
			continue
		}
		sessDir := filepath.Join(runsDir, s.Name())
		runs, err := os.ReadDir(sessDir)
		if err != nil {
			continue
		}
		for _, r := range runs {
			if !r.IsDir() {
				continue
			}
			runDir := filepath.Join(sessDir, r.Name())
			newest, ok := newestModTime(runDir)
			if !ok || newest.After(cutoff) {
				continue
			}
			if os.RemoveAll(runDir) == nil {
				removed++
			}
		}
		// Only succeeds if empty, which is exactly the intent.
		_ = os.Remove(sessDir)
	}
	return removed, nil
}

// newestModTime is the latest modification time of dir or anything beneath
// it. ok is false if the walk hit an error, so a partially unreadable run is
// kept rather than judged by the part that could be read.
func newestModTime(dir string) (newest time.Time, ok bool) {
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest, err == nil
}
