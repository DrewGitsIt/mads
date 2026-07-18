package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// pollInterval is how often handlePipelineTaskOutput re-stats the task's
// captured log file and re-checks task status — no inotify, plain poll-tail
// (Pipeline tab HANDOFF.md §3).
const pollInterval = 500 * time.Millisecond

// handlePipelineTaskOutput streams a task's captured agent output — live if
// the task is still running, or the full captured log if it already
// finished — from $MADS_HOME/runs/<session_id>/<run_seq>/<task_id>/
// <stage>-attempt-<n>.log (services/pipeline/engine/outputlog.go writes
// these). There is no Data Service event type for output chunks by design
// (acceptance criteria) — this reads derived, on-disk data only, the same
// philosophy as commits.go.
//
// This assumes the process that ran engine.Run (today, `mads run`) and this
// Router process resolve the same $MADS_HOME — true for local dev and the
// e2e harness (both set MADS_HOME from the same env), not guaranteed across
// machines.
func (s *server) handlePipelineTaskOutput(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	taskID := r.PathValue("taskId")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Nothing captured (no run yet, or no log file for this task yet): the
	// honest empty state is a connection that closes having delivered zero
	// chunks — no special sentinel, see §4's client-side handling.
	runSeq, ok := latestRunSeq(s.runsDir, sessionID)
	if !ok {
		return
	}
	taskDir := filepath.Join(s.runsDir, sessionID, strconv.FormatInt(runSeq, 10), taskID)
	curPath, ok := latestLogFile(taskDir)
	if !ok {
		return
	}
	curFile, err := os.Open(curPath)
	if err != nil {
		return
	}
	defer func() { _ = curFile.Close() }()
	var offset int64

	ctx := r.Context()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		// Re-glob every tick: a newer *.log file means the task moved to its
		// next stage while this connection was open — switch to tailing it,
		// continuing the same output stream.
		if next, found := latestLogFile(taskDir); found && next != curPath {
			_ = curFile.Close()
			f, err := os.Open(next)
			if err != nil {
				return
			}
			curFile, curPath, offset = f, next, 0
		}

		grew := false
		if info, err := curFile.Stat(); err == nil && info.Size() > offset {
			buf := make([]byte, info.Size()-offset)
			if _, err := curFile.ReadAt(buf, offset); err != nil {
				return
			}
			offset = info.Size()
			grew = true
			data, err := json.Marshal(string(buf))
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}

		if !grew {
			task, err := s.data.Task(ctx, taskID)
			if err == nil && isTerminalTaskStatus(task.Status) {
				if _, err := fmt.Fprint(w, "event: end\ndata:\n\n"); err != nil {
					return
				}
				flusher.Flush()
				return
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// latestRunSeq finds the highest run_seq directory under runsDir/sessionID —
// the session's most recent pipeline run. false if the session has no runs
// captured at all.
func latestRunSeq(runsDir, sessionID string) (int64, bool) {
	entries, err := os.ReadDir(filepath.Join(runsDir, sessionID))
	if err != nil {
		return 0, false
	}
	var max int64
	found := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil {
			continue
		}
		if !found || n > max {
			max = n
			found = true
		}
	}
	return max, found
}

// latestLogFile returns the most-recently-modified *.log file in taskDir —
// the latest attempt's captured log, which also naturally tracks the
// currently-running stage while its file is being actively appended to.
func latestLogFile(taskDir string) (string, bool) {
	matches, err := filepath.Glob(filepath.Join(taskDir, "*.log"))
	if err != nil || len(matches) == 0 {
		return "", false
	}
	var best string
	var bestMod time.Time
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestMod) {
			best, bestMod = m, info.ModTime()
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

func isTerminalTaskStatus(s contracts.TaskStatus) bool {
	switch s {
	case contracts.StatusDone, contracts.StatusBlocked, contracts.StatusNeedsReview, contracts.StatusWontDo:
		return true
	default:
		return false
	}
}
