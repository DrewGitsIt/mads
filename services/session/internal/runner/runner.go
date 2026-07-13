// Package runner drives one interactive provider session for one MADS
// session (spec §15): it starts or resumes the provider session, serializes
// user turns onto it, fans out provider events to subscribers, and keeps the
// Data Service's session record (Activity, ProviderRef) in sync.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/session/internal/worktree"
)

// ErrNotReady is returned by Send/Interrupt before Start has completed.
var ErrNotReady = errors.New("runner: session not ready")

// ErrTurnInFlight is returned by Send when a turn is already in progress —
// one turn at a time, matching the chat UX (composer disabled while
// streaming).
var ErrTurnInFlight = errors.New("runner: turn already in flight")

// ErrNoWorktree is returned by Merge when the session has no isolated
// worktree to merge back.
var ErrNoWorktree = errors.New("runner: session has no isolated worktree to merge")

// Runner owns one provider.Session for the lifetime of this OS process.
type Runner struct {
	sessionID string
	data      *dataclient.Client
	providers *provider.Registry

	// Cwd is the provider session's working directory. Defaults to ".";
	// callers set it (from -cwd) before calling Start. When WorktreeRoot is
	// set and Cwd is a git repo, Start isolates the session into a worktree
	// under WorktreeRoot instead of running directly on Cwd (spec §17).
	Cwd string

	// WorktreeRoot is where session worktrees are created (outside Cwd).
	// Empty (the default) disables isolation entirely, keeping every
	// existing caller — which never sets this — on today's behavior.
	WorktreeRoot string

	wt *worktree.Manager // built lazily the first time isolation is needed

	// Died is closed exactly once if the provider process ends mid-turn
	// without us having initiated the close (the Router's crash-supervision
	// signal, spec §15). The caller (main.go) selects on this and exits the
	// process immediately, without running the clean-shutdown path — that
	// path belongs to the Router's supervisor, which reacts to this OS
	// process exiting.
	Died chan struct{}
	died sync.Once

	hub *hub

	mu      sync.Mutex
	sess    provider.Session
	busy    bool // a turn is in flight (Send called, no EventTurnEnd yet)
	closing bool // Shutdown has started tearing this session down
}

// New builds a Runner for sessionID. data's actor must be sessionID (spec
// §15's per-service actor convention) so events and lease claims attribute
// correctly.
func New(sessionID string, data *dataclient.Client, providers *provider.Registry) *Runner {
	return &Runner{
		sessionID: sessionID,
		data:      data,
		providers: providers,
		Cwd:       ".",
		Died:      make(chan struct{}),
		hub:       newHub(),
	}
}

// Start loads the session record, starts (or resumes, via ProviderRef) the
// provider session, persists the resume handle, and begins relaying events.
func (r *Runner) Start(ctx context.Context) error {
	sess, err := r.data.Session(ctx, r.sessionID)
	if err != nil {
		return fmt.Errorf("runner: load session %s: %w", r.sessionID, err)
	}

	p, err := r.providers.Get(sess.Provider)
	if err != nil {
		return fmt.Errorf("runner: provider %q: %w", sess.Provider, err)
	}

	cwd := r.Cwd
	if r.WorktreeRoot != "" && sess.Isolation != contracts.IsolationCurrentDir {
		switch {
		case sess.WorktreeDir != "":
			cwd = sess.WorktreeDir // resuming (bounce): reuse, don't recreate
		case worktree.IsGitRepo(ctx, r.Cwd):
			dir, branch, err := r.manager().Create(ctx, r.sessionID, sess.Name)
			if err != nil {
				return fmt.Errorf("runner: create worktree: %w", err)
			}
			sess.WorktreeDir, sess.WorktreeBranch = dir, branch
			cwd = dir
		}
	}

	provSess, err := p.Start(ctx, provider.StartOptions{
		Mode:   provider.ModeACP,
		Model:  sess.Model,
		Cwd:    cwd,
		Resume: sess.ProviderRef,
	})
	if err != nil {
		return fmt.Errorf("runner: start provider session: %w", err)
	}

	// Persist the resume handle before returning success, so a crash right
	// after this point still leaves a resumable ref recorded.
	sess.ProviderRef = provSess.Ref()
	sess.Activity = contracts.SessionActive
	if err := r.data.UpdateSession(ctx, &sess); err != nil {
		return fmt.Errorf("runner: persist provider ref: %w", err)
	}

	if err := r.data.Append(ctx, &contracts.Envelope{
		Type:      contracts.SessionProvisioning,
		SessionID: r.sessionID,
		Payload:   mustJSON(map[string]string{"step": "provider_ready", "provider": sess.Provider}),
	}); err != nil {
		log.Printf("runner: append session.provisioning: %v", err)
	}
	if err := r.data.Append(ctx, &contracts.Envelope{
		Type:      contracts.SessionActivityChanged,
		SessionID: r.sessionID,
		Payload:   mustJSON(map[string]string{"activity": string(contracts.SessionActive)}),
	}); err != nil {
		log.Printf("runner: append session.activity_changed: %v", err)
	}

	r.mu.Lock()
	r.sess = provSess
	r.mu.Unlock()

	go r.pump(provSess)
	return nil
}

// manager lazily builds the worktree.Manager the first time isolation is
// needed, so a Runner that never isolates (WorktreeRoot == "") never touches
// git at all.
func (r *Runner) manager() *worktree.Manager {
	if r.wt == nil {
		r.wt = &worktree.Manager{
			Primary:   r.Cwd,
			Root:      r.WorktreeRoot,
			DataURL:   r.data.BaseURL(),
			SessionID: r.sessionID,
		}
	}
	return r.wt
}

// Merge attempts to merge the session's isolated worktree branch back into
// targetBranch (the primary's current branch if targetBranch is empty). A
// clean merge lands the work on the primary and cleans up the worktree and
// branch; a conflicted merge aborts, keeps the branch, and returns
// structured conflict data for the UI's resolution flow (spec §6, §17).
//
// Known gap, not solved here: the cleanup below removes the worktree
// directory that the live provider process may still have as its cwd. If
// the interactive agent is mid-turn when merge-back runs, later
// relative-path operations in that process could fail. Restarting the
// provider session pointed at the primary tree post-merge (reusing
// ProviderRef the way Start already resumes it) would fix this properly,
// but it's out of scope for W4.5's acceptance criteria.
func (r *Runner) Merge(ctx context.Context, targetBranch string) (contracts.MergeResult, error) {
	sess, err := r.data.Session(ctx, r.sessionID)
	if err != nil {
		return contracts.MergeResult{}, fmt.Errorf("runner: load session: %w", err)
	}
	if sess.WorktreeBranch == "" {
		return contracts.MergeResult{}, ErrNoWorktree
	}

	m := r.manager()
	if targetBranch != "" {
		if _, err := m.Git(ctx, r.Cwd, "checkout", targetBranch); err != nil {
			return contracts.MergeResult{}, fmt.Errorf("runner: checkout %s: %w", targetBranch, err)
		}
	}
	actualTarget, _ := m.Git(ctx, r.Cwd, "rev-parse", "--abbrev-ref", "HEAD")
	actualTarget = strings.TrimSpace(actualTarget)

	if err := r.data.Append(ctx, &contracts.Envelope{
		Type: contracts.MergeAttempted, SessionID: r.sessionID,
		Payload: mustJSON(map[string]string{"branch": sess.WorktreeBranch, "target_branch": actualTarget}),
	}); err != nil {
		log.Printf("runner: append merge.attempted: %v", err)
	}

	if err := m.CommitStray(ctx, sess.WorktreeDir, r.sessionID, sess.Name); err != nil {
		return contracts.MergeResult{}, fmt.Errorf("runner: commit stray changes: %w", err)
	}
	result, err := m.Merge(ctx, "git merge --no-ff", r.sessionID, sess.Name, sess.WorktreeBranch)
	if err != nil {
		return contracts.MergeResult{}, err
	}

	if result.Conflicted {
		if err := r.data.Append(ctx, &contracts.Envelope{
			Type: contracts.MergeConflicted, SessionID: r.sessionID,
			Payload: mustJSON(map[string]any{"branch": result.Branch, "target_branch": actualTarget, "files": result.Files}),
		}); err != nil {
			log.Printf("runner: append merge.conflicted: %v", err)
		}
		return result, nil // branch/worktree untouched — session stays flagged unmerged
	}

	m.Remove(context.WithoutCancel(ctx), sess.WorktreeDir, sess.WorktreeBranch)
	sess.WorktreeDir, sess.WorktreeBranch = "", ""
	if err := r.data.UpdateSession(ctx, &sess); err != nil {
		return contracts.MergeResult{}, fmt.Errorf("runner: clear worktree fields: %w", err)
	}
	if err := r.data.Append(ctx, &contracts.Envelope{
		Type: contracts.MergeCompleted, SessionID: r.sessionID,
		Payload: mustJSON(map[string]string{"branch": result.Branch, "target_branch": actualTarget}),
	}); err != nil {
		log.Printf("runner: append merge.completed: %v", err)
	}
	return result, nil
}

// Send relays one user turn. Returns ErrTurnInFlight (surfaced as HTTP 409)
// if a turn is already in flight.
func (r *Runner) Send(ctx context.Context, text string) error {
	r.mu.Lock()
	if r.sess == nil {
		r.mu.Unlock()
		return ErrNotReady
	}
	if r.busy {
		r.mu.Unlock()
		return ErrTurnInFlight
	}
	r.busy = true
	sess := r.sess
	r.mu.Unlock()

	if err := sess.Send(ctx, text); err != nil {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
		return err
	}
	return nil
}

// Interrupt stops the in-flight response, if any. The session remains
// usable — the pump clears the in-flight flag when the resulting
// EventTurnEnd (Interrupted: true) arrives.
func (r *Runner) Interrupt(ctx context.Context) error {
	r.mu.Lock()
	if r.sess == nil {
		r.mu.Unlock()
		return ErrNotReady
	}
	sess := r.sess
	r.mu.Unlock()
	return sess.Interrupt(ctx)
}

// Subscribe attaches a new listener to the provider event stream. Call
// unsubscribe when done to release the channel.
func (r *Runner) Subscribe() (ch <-chan provider.Event, unsubscribe func()) {
	return r.hub.subscribe()
}

// Shutdown closes the provider session, marks the session record inactive,
// releases any leases held by this session, and stops accepting new events.
// Safe to call once; subsequent calls are no-ops.
func (r *Runner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return nil
	}
	r.closing = true
	sess := r.sess
	r.mu.Unlock()

	var closeErr error
	if sess != nil {
		closeErr = sess.Close()
	}

	if cur, err := r.data.Session(ctx, r.sessionID); err != nil {
		log.Printf("runner: load session for shutdown: %v", err)
	} else {
		cur.Activity = contracts.SessionInactive
		if err := r.data.UpdateSession(ctx, &cur); err != nil {
			log.Printf("runner: mark session inactive: %v", err)
		}
	}
	if err := r.data.Append(ctx, &contracts.Envelope{
		Type:      contracts.SessionActivityChanged,
		SessionID: r.sessionID,
		Payload:   mustJSON(map[string]string{"activity": string(contracts.SessionInactive)}),
	}); err != nil {
		log.Printf("runner: append session.activity_changed: %v", err)
	}
	if err := r.data.ReleaseAll(ctx, r.sessionID, "shutdown"); err != nil {
		log.Printf("runner: release leases: %v", err)
	}

	r.hub.close()
	return closeErr
}

// pump reads the provider's event stream until it closes, broadcasting
// every event to subscribers. On close, if the last turn never reached
// EventTurnEnd and we did not initiate the close ourselves (Shutdown), the
// provider process died mid-turn: broadcast a synthetic error and signal
// Died so main.go can exit — without duplicating the Router's own crash
// bookkeeping (lease release, session.crashed), which fires independently
// when this OS process exits.
func (r *Runner) pump(sess provider.Session) {
	for ev := range sess.Events() {
		if ev.Kind == provider.EventTurnEnd {
			r.mu.Lock()
			r.busy = false
			r.mu.Unlock()
		}
		r.hub.broadcast(ev)
	}

	r.mu.Lock()
	crashed := !r.closing && r.busy
	r.mu.Unlock()
	if !crashed {
		return
	}

	log.Printf("runner: provider session for %s ended unexpectedly mid-turn", r.sessionID)
	r.hub.broadcast(provider.Event{Kind: provider.EventError, Text: "provider process exited unexpectedly"})
	r.died.Do(func() { close(r.Died) })
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err) // v is always a static map[string]string literal
	}
	return raw
}
