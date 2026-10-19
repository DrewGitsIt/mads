package engine

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/pipelineprogress"
)

// Picking up an Interrupted task (docs/PIPELINE-LEASES-DESIGN.md B2): the
// progress record and the pickup plan are pkg/pipelineprogress's pure
// functions, shared with the Router (which answers the Plan view's "where
// would this task pick up?"). This file is the engine's side: reading the
// events, and what a picked-up attempt is told.

// refreshInProgress pages the session's task.status_changed events since
// the last refresh into l. Best-effort: a failed read keeps what l knew.
func (e *engine) refreshInProgress(ctx context.Context, l *pipelineprogress.InProgressLog) {
	for {
		batch, err := e.cfg.Data.Events(ctx, l.Since(), e.cfg.SessionID, []string{string(contracts.TaskStatusChanged)})
		if err != nil || len(batch) == 0 {
			return
		}
		l.Fold(batch)
	}
}

// loadProgress pages through the session's events and rebuilds taskID's
// progress record. Best-effort: a read failure is no record (restart).
func (e *engine) loadProgress(ctx context.Context, taskID string) *pipelineprogress.Progress {
	var all []contracts.Envelope
	var since int64
	for {
		batch, err := e.cfg.Data.Events(ctx, since, e.cfg.SessionID, pipelineprogress.EventTypes)
		if err != nil {
			return nil
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		since = batch[len(batch)-1].Seq
	}
	return pipelineprogress.Of(all, taskID)
}

// interruptedTail is the end of an interrupted attempt's captured output, or
// "" when none was captured.
func (e *engine) interruptedTail(taskID string, rec *pipelineprogress.Progress, stage string, attempt int) string {
	if e.cfg.MadsHome == "" || rec == nil {
		return ""
	}
	raw, err := os.ReadFile(outputLogPath(e.cfg.MadsHome, e.cfg.SessionID, rec.RunSeq, taskID, stage, attempt))
	if err != nil {
		return ""
	}
	return trim(string(raw), 3000)
}

// resumingNote is the brief a fresh attempt gets when it takes over an
// interrupted one: say so, and pass on what the interrupted attempt said.
func resumingNote(attempt int, tail string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are resuming interrupted work: attempt %d at this stage was interrupted before it finished, and its changes are still in the working tree. Inspect them and continue from there rather than starting over.\n", attempt)
	if tail != "" {
		fmt.Fprintf(&b, "\nThe interrupted attempt's output ended with:\n%s\n", tail)
	}
	return b.String()
}

// priorWorkNote is the brief the first agent stage of a restarted task gets.
const priorWorkNote = "An earlier pipeline run worked on this task and was interrupted; its work is still in this working tree (committed on the task branch). Inspect it before starting, and build on it where it fits.\n"

// resumePrompt continues an agent session that was interrupted.
const resumePrompt = "You were interrupted; continue where you left off."
