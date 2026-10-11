package runner

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/session/internal/shellwatch"
)

// startShellWatch follows the session's shell calls for the files they
// write (services/session/internal/shellwatch) when it works in place in a
// git checkout. File tools report their own paths; without this, `sed -i`,
// a formatter or a script's writes never reach the ledger, so Commit leaves
// them out. An isolated session needs none of it: everything in its
// worktree is already its work (§5), and its header counts those files as
// Outside.
//
// Called once in Start, before the pump runs, so the pump reads r.shell
// without a lock.
func (r *Runner) startShellWatch() {
	r.mu.Lock()
	wt := r.wt
	r.mu.Unlock()
	if wt.dir == "" || wt.branch != "" {
		return
	}
	r.shell = shellwatch.New(wt.dir, r.recordShellChanges)
}

// observeShell is the pump's hook: a shell call that might write opens a
// window, its result closes it, and a turn end closes what the main agent
// left open. at is when the pump took the event — stamped before anything
// else the pump does with it, so the window starts as early as it can.
// Only a lock and a map write here; the looking happens elsewhere.
func (r *Runner) observeShell(ev provider.Event, at time.Time) {
	if r.shell == nil {
		return
	}
	switch ev.Kind {
	case provider.EventToolUse:
		if ev.Tool != nil && ev.Tool.Kind == provider.ToolKindExecute && !shellwatch.ReadOnly(ev.Tool.Input) {
			r.shell.Started(ev.Tool.ID, at, ev.ParentToolUseID != "")
		}
	case provider.EventToolResult:
		if ev.Tool != nil {
			r.shell.Finished(ev.Tool.ID, at)
		}
	case provider.EventTurnEnd:
		r.shell.TurnEnded(at)
	}
}

// recordShellChanges is the tracker's Recorder: one look's findings, sent
// to the Data Service as one batch. A file the session's own file tool
// recorded inside the same window is left out — that edit is already in the
// ledger. A new header count follows when anything was recorded.
func (r *Runner) recordShellChanges(ctx context.Context, top string, changes []shellwatch.Change) {
	r.mu.Lock()
	base := r.touchBaseLocked()
	batch := make([]contracts.ShellFileChange, 0, len(changes))
	for _, c := range changes {
		abs := filepath.Join(top, filepath.FromSlash(c.Path))
		if at, ok := r.toolTouchedAt[abs]; ok && !at.Before(c.Since) && !at.After(c.Until) {
			continue
		}
		// Same convention as recordFileChange: only a path's first touch
		// (or its first since a recorded deletion) reads as created.
		change := c.Kind
		prev, seen := r.touched[abs]
		if change == "deleted" && prev == "deleted" {
			continue // the turn-end sweep (sweepDeletedFiles) got there first
		}
		if change == "added" && seen && prev != "deleted" {
			change = "modified"
		}
		batch = append(batch, contracts.ShellFileChange{
			Repo: base, Path: abs, Change: change, Timestamp: c.At, Since: c.Since, Until: c.Until,
		})
	}
	r.mu.Unlock()
	if len(batch) == 0 {
		return
	}

	res, err := r.data.RecordShellFileChanges(ctx, r.sessionID, batch)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("runner: record shell file changes: %v", err)
		}
		return
	}
	r.mu.Lock()
	for _, fc := range res.Recorded {
		r.touched[fc.Path] = fc.Change
	}
	r.mu.Unlock()
	if len(res.Recorded) > 0 {
		r.refreshCountsAsync()
	}
}
