package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// handlePipelineTaskTurns streams a task's agent transcript as normalized
// turns (contracts.TranscriptMessage) — the transparency-first drill-in feed
// sourced from the provider's own transcript rather than the tee'd prose log
// (pipeline_output.go). Wire format is provider-owned and never reaches the
// UI: normalizeTurns is the one seam.
//
// Like handlePipelineTaskOutput, this is honest-empty by design: no run
// captured yet, no recorded stage.attempt_ref for the task's current
// stage/attempt, or a provider with no TranscriptReader/no transcript for
// that ref all degrade to a connection that opens and closes having
// delivered zero frames — the UI's signal to fall back to the raw prose log
// (pipeline_output.go), not an error.
func (s *server) handlePipelineTaskTurns(w http.ResponseWriter, r *http.Request) {
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

	ctx := r.Context()
	sess, err := s.data.Session(ctx, sessionID)
	if err != nil {
		return
	}

	runSeq, ok := latestRunSeq(s.runsDir, sessionID)
	if !ok {
		return
	}
	taskDir := filepath.Join(s.runsDir, sessionID, strconv.FormatInt(runSeq, 10), taskID)
	curPath, ok := latestLogFile(taskDir)
	if !ok {
		return
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var curRef string
	haveRef := false
	lastLen := -1

	for {
		// A newer *.log file means the task moved to its next stage —
		// re-resolve the ref for the new attempt (pipeline_output.go's same
		// re-glob-on-poll).
		if next, found := latestLogFile(taskDir); found && next != curPath {
			curPath, haveRef, lastLen = next, false, -1
		}

		if !haveRef {
			if stage, attempt, ok := parseStageAttempt(curPath); ok {
				if ref, found := s.findAttemptRef(ctx, sessionID, taskID, stage, attempt); found {
					curRef, haveRef = ref, true
				}
			}
		}

		if haveRef {
			msgs := s.turnsFor(ctx, sess, curRef)
			if len(msgs) != lastLen {
				lastLen = len(msgs)
				raw, err := json.Marshal(msgs)
				if err != nil {
					return
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
					return
				}
				flusher.Flush()
			}
		}

		task, err := s.data.Task(ctx, taskID)
		if err == nil && isTerminalTaskStatus(task.Status) {
			if _, err := fmt.Fprint(w, "event: end\ndata:\n\n"); err != nil {
				return
			}
			flusher.Flush()
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// turnsFor reads and normalizes the provider transcript at ref, degrading to
// an empty slice (never an error) on any failure — no bound provider, no
// TranscriptReader support, or the read itself failing.
func (s *server) turnsFor(ctx context.Context, sess contracts.Session, ref string) []contracts.TranscriptMessage {
	out := []contracts.TranscriptMessage{}
	p, err := s.providers.Get(sess.Provider)
	if err != nil {
		return out
	}
	reader, ok := p.(provider.TranscriptReader)
	if !ok {
		return out
	}
	msgs, err := reader.Transcript(ctx, ref)
	if err != nil {
		return out
	}
	return normalizeTurns(msgs, s.inboundSources(ctx, sess))
}

// findAttemptRef looks up the provider-native session ref the engine
// recorded for taskID's given stage/attempt (engine.go's recordRef), the
// exact attribution this endpoint depends on — no path guessing. false if
// no such stage.attempt_ref event has landed yet (the one-shot hasn't
// announced a ref, or never will).
func (s *server) findAttemptRef(ctx context.Context, sessionID, taskID, stage string, attempt int) (string, bool) {
	evs, err := s.data.Events(ctx, 0, sessionID, []string{string(contracts.StageAttemptRef)})
	if err != nil {
		return "", false
	}
	type refPayload struct {
		Stage   string `json:"stage"`
		Attempt int    `json:"attempt"`
		Ref     string `json:"ref"`
	}
	var ref string
	for _, ev := range evs {
		if ev.TaskID != taskID {
			continue
		}
		var p refPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.Stage == stage && p.Attempt == attempt && p.Ref != "" {
			ref = p.Ref
		}
	}
	return ref, ref != ""
}

// parseStageAttempt recovers the stage name and attempt number from an
// output log's filename (outputlog.go's "<stage>-attempt-<n>.log"), the
// same layout handlePipelineTaskOutput already tails.
func parseStageAttempt(path string) (stage string, attempt int, ok bool) {
	base := strings.TrimSuffix(filepath.Base(path), ".log")
	const sep = "-attempt-"
	i := strings.LastIndex(base, sep)
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(base[i+len(sep):])
	if err != nil {
		return "", 0, false
	}
	return base[:i], n, true
}
