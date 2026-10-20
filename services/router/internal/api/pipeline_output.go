package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
//
// Optional ?run=&stage=&attempt= pick an earlier run and/or one exact stage
// attempt instead of following the newest log (pipelineLogQuery).
func (s *server) handlePipelineTaskOutput(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	taskID := r.PathValue("taskId")

	sel, err := parsePipelineLogQuery(r.URL.Query())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

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
	// chunks — no special sentinel, see §4's client-side handling. A request
	// that named a run or attempt also gets "end" (sel.emptyEnd).
	runSeq, taskDir, curPath, ok := s.resolvePipelineLog(sessionID, taskID, sel)
	if !ok {
		sel.emptyEnd(w, flusher)
		return
	}
	curFile, err := os.Open(curPath)
	if err != nil {
		sel.emptyEnd(w, flusher)
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
		// continuing the same output stream. A pinned attempt never switches.
		if next, found := latestLogFile(taskDir); !sel.pinned && found && next != curPath {
			_ = curFile.Close()
			f, err := os.Open(next)
			if err != nil {
				return
			}
			curFile, curPath, offset = f, next, 0
		}

		// Decided before the read below, so bytes written up to the moment
		// the file stopped being live are still delivered before "end".
		finished := s.pipelineLogFinished(ctx, sessionID, taskID, runSeq, taskDir, curPath, sel)

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

		if finished {
			writeEnd(w, flusher)
			return
		}
		if !grew {
			task, err := s.data.Task(ctx, taskID)
			if err == nil && isTerminalTaskStatus(task.Status) {
				writeEnd(w, flusher)
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

// pipelineLogQuery is the optional selector both drill-in endpoints accept
// (docs/TASKS-PIPELINE-DESIGN.md contract 3). The zero value is the default:
// the latest run, following its newest log.
type pipelineLogQuery struct {
	run     int64
	hasRun  bool   // ?run= given: that run's task dir instead of the latest
	stage   string // set with attempt only when pinned
	attempt int
	pinned  bool // ?stage=&attempt= given: exactly that file, never follow
}

// parsePipelineLogQuery validates ?run=, ?stage= and ?attempt=. stage and
// attempt come as a pair; stage becomes a filename component, so anything
// that could step outside the task dir is rejected outright.
func parsePipelineLogQuery(q url.Values) (pipelineLogQuery, error) {
	var sel pipelineLogQuery
	if q.Has("run") {
		n, err := strconv.ParseInt(q.Get("run"), 10, 64)
		if err != nil || n < 0 {
			return sel, errors.New("run must be a non-negative integer")
		}
		sel.run, sel.hasRun = n, true
	}
	if q.Has("stage") != q.Has("attempt") {
		return sel, errors.New("stage and attempt must be given together")
	}
	if !q.Has("stage") {
		return sel, nil
	}
	stage := q.Get("stage")
	if stage == "" || strings.ContainsAny(stage, `/\`) || strings.Contains(stage, "..") || filepath.Base(stage) != stage {
		return sel, errors.New("invalid stage")
	}
	n, err := strconv.Atoi(q.Get("attempt"))
	if err != nil || n < 1 {
		return sel, errors.New("attempt must be a positive integer")
	}
	sel.stage, sel.attempt, sel.pinned = stage, n, true
	return sel, nil
}

// emptyEnd closes out an honest-empty stream: the default request keeps the
// bare close, but one that named a run or attempt gets an explicit "end" —
// that file is never coming, so there is nothing to retry for.
func (sel pipelineLogQuery) emptyEnd(w http.ResponseWriter, flusher http.Flusher) {
	if sel.hasRun || sel.pinned {
		writeEnd(w, flusher)
	}
}

func writeEnd(w http.ResponseWriter, flusher http.Flusher) {
	if _, err := fmt.Fprint(w, "event: end\ndata:\n\n"); err != nil {
		return
	}
	flusher.Flush()
}

// resolvePipelineLog picks the run and log file sel names: the latest run
// unless sel.hasRun, then the exact pinned attempt file or else the newest
// *.log. false if that run, task dir or file isn't captured.
func (s *server) resolvePipelineLog(sessionID, taskID string, sel pipelineLogQuery) (runSeq int64, taskDir, path string, ok bool) {
	if sel.hasRun {
		runSeq = sel.run
	} else if runSeq, ok = latestRunSeq(s.runsDir, sessionID); !ok {
		return 0, "", "", false
	}
	taskDir = filepath.Join(s.runsDir, sessionID, strconv.FormatInt(runSeq, 10), taskID)
	if !sel.pinned {
		path, ok = latestLogFile(taskDir)
		return runSeq, taskDir, path, ok
	}
	path = filepath.Join(taskDir, fmt.Sprintf("%s-attempt-%d.log", sel.stage, sel.attempt))
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return 0, "", "", false
	}
	return runSeq, taskDir, path, true
}

// pipelineLogFinished reports whether nothing more will be written to the
// stream sel selected, on top of the terminal-status check both handlers
// already make. A named run that is no longer the latest is over; a pinned
// attempt is live only while it is the newest log of the latest run and the
// task hasn't reached a terminal status. Always false for the default query.
func (s *server) pipelineLogFinished(ctx context.Context, sessionID, taskID string, runSeq int64, taskDir, path string, sel pipelineLogQuery) bool {
	if !sel.hasRun && !sel.pinned {
		return false
	}
	if latest, ok := latestRunSeq(s.runsDir, sessionID); !ok || latest != runSeq {
		return true
	}
	if !sel.pinned {
		return false
	}
	if newest, ok := latestLogFile(taskDir); !ok || newest != path {
		return true
	}
	task, err := s.data.Task(ctx, taskID)
	return err == nil && isTerminalTaskStatus(task.Status)
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
