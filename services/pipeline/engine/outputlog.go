package engine

import (
	"fmt"
	"os"
	"path/filepath"
)

// outputLogPath is $MadsHome/runs/<session_id>/<run_seq>/<task_id>/
// <stage>-attempt-<n>.log — the layout the Router's drill-in endpoint reads
// back (services/router/internal/api/pipeline_output.go).
func outputLogPath(madsHome, sessionID string, runSeq int64, taskID, stage string, attempt int) string {
	return filepath.Join(madsHome, "runs", sessionID, fmt.Sprint(runSeq), taskID,
		fmt.Sprintf("%s-attempt-%d.log", stage, attempt))
}

// outputLog is one stage attempt's captured log file: chunks appended as
// they arrive, best-effort throughout — a capture failure never fails the
// stage (same philosophy as recordUsage).
type outputLog struct {
	f       *os.File
	written int
}

// openOutputLog opens (create/append) the log file for one stage attempt.
// Returns a nil *outputLog (not an error) on any failure to open — callers
// treat capture as best-effort and never let it block a stage.
func openOutputLog(madsHome, sessionID string, runSeq int64, taskID, stage string, attempt int) *outputLog {
	if madsHome == "" {
		return nil
	}
	path := outputLogPath(madsHome, sessionID, runSeq, taskID, stage, attempt)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	return &outputLog{f: f}
}

// write appends chunk, best-effort.
func (l *outputLog) write(chunk string) {
	if l == nil || chunk == "" {
		return
	}
	n, err := l.f.WriteString(chunk)
	if err == nil {
		l.written += n
	}
}

// close closes the file, first writing fallback if nothing streamed —
// a provider with no incremental output mechanism still leaves something
// captured for the attempt.
func (l *outputLog) close(fallback string) {
	if l == nil {
		return
	}
	if l.written == 0 {
		l.write(fallback)
	}
	_ = l.f.Close()
}
