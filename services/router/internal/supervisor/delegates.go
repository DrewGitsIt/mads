package supervisor

import (
	"context"
	"log"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/delegates"
)

// concludeStrandedDelegates ends every delegate still recorded running for
// sessionID once it has no live child. Claude Code's subagents run inside
// the harness process, so they died with it; the runner that records that
// (Tracker.ProcessExit) may not have got to — killed in a shutdown, crashed
// — and the correction a starting runner makes (Tracker.Seed) waits until
// the session is next resumed, which may be never. Until then the rail
// would show a subagent running for days.
//
// It makes that same Seed correction now, so the records read exactly as a
// resume would leave them: orphaned, reason runner_restart, and a parent's
// WaitingOn on them cleared. Best-effort: logs and gives up on errors.
//
// A session with a tracked child is skipped — its runner owns the records.
// The check is made before reading, so a child spawned in the moment between
// can still have a fresh record overwritten; a resume is a user action
// after the exit, so that window is not a practical one.
func (s *Supervisor) concludeStrandedDelegates(ctx context.Context, sessionID string) {
	s.mu.Lock()
	_, tracked := s.sessions[sessionID]
	s.mu.Unlock()
	if tracked {
		return
	}
	stored, err := s.data.SessionDelegates(ctx, sessionID)
	if err != nil {
		log.Printf("supervisor: load delegates for %s: %v", sessionID, err)
		return
	}
	running := false
	for _, d := range stored {
		if d.State == contracts.DelegateRunning {
			running = true
			break
		}
	}
	if !running {
		return
	}
	for _, d := range delegates.New(sessionID, 0).Seed(stored, time.Now()) {
		if _, err := s.data.PutDelegate(ctx, d); err != nil {
			log.Printf("supervisor: conclude delegate %s of %s: %v", d.HarnessID, sessionID, err)
		}
	}
}

// concludeAllStrandedDelegates is the boot-time sweep of the above: every
// session with a delegate recorded running, active or not, gets
// concludeStrandedDelegates. A session already inactive before this Router
// started never passes through the active-session sweep, and its stranded
// delegates would otherwise stay running until it is next resumed.
func (s *Supervisor) concludeAllStrandedDelegates(ctx context.Context) {
	running, err := s.data.Delegates(ctx, contracts.DelegateRunning)
	if err != nil {
		log.Printf("supervisor: boot sweep: list running delegates: %v", err)
		return
	}
	seen := map[string]bool{}
	for _, d := range running {
		if seen[d.SessionID] {
			continue
		}
		seen[d.SessionID] = true
		s.concludeStrandedDelegates(ctx, d.SessionID)
	}
}
