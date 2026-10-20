package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// outputLogPath is $MadsHome/runs/<session_id>/<run_seq>/<task_id>/
// <stage>-attempt-<n>.log — the layout the Router's drill-in endpoint reads
// back (services/router/internal/api/pipeline_output.go).
func outputLogPath(madsHome, sessionID string, runSeq int64, taskID, stage string, attempt int) string {
	return filepath.Join(madsHome, "runs", sessionID, fmt.Sprint(runSeq), taskID,
		fmt.Sprintf("%s-attempt-%d.log", stage, attempt))
}

// outputLog is one stage attempt's captured log file: events (text deltas,
// tool use) appended as they arrive, best-effort throughout — a capture
// failure never fails the stage (same philosophy as recordUsage). Every
// event is separated by a newline boundary so the file reads as distinct
// entries rather than one run-on wall of text; a header/footer pair frames
// the attempt.
type outputLog struct {
	f         *os.File
	events    int // count of write()/tool() calls — NOT header/footer
	lastNL    bool
	stage     string
	attempt   int
	startedAt time.Time
}

// openOutputLog opens (create/append) the log file for one stage attempt and
// writes its header line immediately, so a live SSE follow sees structure
// even before the first token. Returns a nil *outputLog (not an error) on
// any failure to open — callers treat capture as best-effort and never let
// it block a stage.
func openOutputLog(madsHome, sessionID string, runSeq int64, taskID, stage string, attempt int, model string) *outputLog {
	return openOutputLogNote(madsHome, sessionID, runSeq, taskID, stage, attempt, model, "")
}

// openOutputLogNote is openOutputLog with a preface line written before the
// header (none when empty) — a resumed attempt's note of the run and attempt
// it continues.
func openOutputLogNote(madsHome, sessionID string, runSeq int64, taskID, stage string, attempt int, model, preface string) *outputLog {
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
	l := &outputLog{f: f, stage: stage, attempt: attempt, startedAt: time.Now(), lastNL: true}
	if preface != "" {
		_, _ = fmt.Fprintln(f, preface)
	}
	_, _ = fmt.Fprintf(f, "=== stage=%s attempt=%d task=%s model=%s started=%s ===\n",
		stage, attempt, taskID, model, l.startedAt.Format(time.RFC3339))
	return l
}

// emit appends chunk, best-effort, inserting a newline boundary before it
// when the previous write didn't already end in one — a boundary between
// distinct events, never munged into the middle of one.
func (l *outputLog) emit(chunk string) {
	if l == nil || chunk == "" {
		return
	}
	if !l.lastNL && l.events > 0 {
		_, _ = l.f.WriteString("\n")
	}
	if _, err := l.f.WriteString(chunk); err == nil {
		l.lastNL = strings.HasSuffix(chunk, "\n")
		l.events++
	}
}

// write appends a text delta — the thin wrapper the engine calls for
// EventText.
func (l *outputLog) write(chunk string) {
	l.emit(chunk)
}

// tool records a tool-use marker line, e.g. "⏺ Read path/to/file".
func (l *outputLog) tool(t *provider.ToolUse) {
	if l == nil || t == nil {
		return
	}
	label := t.Name
	if t.Path != "" {
		label += " " + t.Path
	}
	l.emit("⏺ " + label + "\n")
}

// close writes the fallback text if nothing streamed, then always writes the
// footer line and closes the file.
func (l *outputLog) close(outcome string, fallback string) {
	if l == nil {
		return
	}
	if l.events == 0 {
		l.emit(fallback)
	}
	if !l.lastNL {
		_, _ = l.f.WriteString("\n")
	}
	duration := time.Since(l.startedAt).Round(time.Millisecond)
	_, _ = fmt.Fprintf(l.f, "=== stage=%s attempt=%d outcome=%s duration=%s ===\n",
		l.stage, l.attempt, outcome, duration)
	_ = l.f.Close()
}
