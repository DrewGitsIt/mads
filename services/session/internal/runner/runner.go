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
	"os/exec"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/githook"
	"github.com/drewkidwell/mads/providers/provider"
)

// ErrNotReady is returned by Send/Interrupt before Start has completed.
var ErrNotReady = errors.New("runner: session not ready")

// ErrTurnInFlight is returned by Send when a turn is already in progress —
// one turn at a time, matching the chat UX (composer disabled while
// streaming).
var ErrTurnInFlight = errors.New("runner: turn already in flight")

// Runner owns one provider.Session for the lifetime of this OS process.
type Runner struct {
	sessionID string
	data      *dataclient.Client
	providers *provider.Registry

	// Cwd is the provider session's working directory. Defaults to ".";
	// callers set it (from -cwd) before calling Start. There is no
	// working-directory field on contracts.Session yet — worktree path
	// resolution is later work — so this is a known gap, not solved here.
	Cwd string

	// DataURL is the Data Service base URL. It is stamped into the session
	// worktree's git config (mads.dataUrl) so the post-commit hook
	// installed by Start knows where to report commits (spec §5).
	DataURL string

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

	// touched tracks paths already reported this run, so recordFileChange
	// can tell an "added" first sighting from a later "modified" one.
	touched map[string]bool
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
		touched:   make(map[string]bool),
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

	r.installHook(ctx)

	provSess, err := p.Start(ctx, provider.StartOptions{
		Mode:   provider.ModeACP,
		Model:  sess.Model,
		Cwd:    r.Cwd,
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

// installHook installs the mads post-commit hook (spec §5) in the
// session's working tree and points it at the Data Service, so interactive
// commits report with session/task linkage the same way pipeline task
// commits do. Best-effort and silent on failure (logged, not returned):
// Cwd defaults to "." (no worktree resolution yet — see README's "Known
// gaps"), and installing against "." would mutate whatever repo this
// process happens to have been started from, so this only acts once Cwd
// has been pointed at an actual session-specific directory that is itself
// a git repo.
func (r *Runner) installHook(ctx context.Context) {
	if r.Cwd == "" || r.Cwd == "." {
		return
	}
	if err := exec.CommandContext(ctx, "git", "-C", r.Cwd, "rev-parse", "--git-dir").Run(); err != nil {
		return
	}
	if err := githook.EnsureInstalled(ctx, r.Cwd); err != nil {
		log.Printf("runner: install commit hook: %v", err)
		return
	}
	if err := githook.SetWorktreeConfig(ctx, r.Cwd, githook.Config{
		SessionID: r.sessionID,
		DataURL:   r.DataURL,
	}); err != nil {
		log.Printf("runner: configure commit hook: %v", err)
	}
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

// Files returns every file this session has touched, as recorded with the
// Data Service (spec §5).
func (r *Runner) Files(ctx context.Context) ([]contracts.FileChange, error) {
	return r.data.FileChanges(ctx, r.sessionID)
}

// Commits returns every commit this session has produced, with task
// linkage resolved by the Data Service (spec §5).
func (r *Runner) Commits(ctx context.Context) ([]contracts.Commit, error) {
	return r.data.Commits(ctx, r.sessionID)
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
		if ev.Kind == provider.EventToolUse && ev.Tool != nil && ev.Tool.Path != "" {
			r.recordFileChange(ev.Tool.Path)
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

// recordFileChange reports one path the agent's tools touched to the Data
// Service (spec §5). "added" on first sighting this run, "modified" after —
// best effort, since a dropped observation shouldn't interrupt the turn.
func (r *Runner) recordFileChange(path string) {
	r.mu.Lock()
	change := "modified"
	if !r.touched[path] {
		change = "added"
		r.touched[path] = true
	}
	r.mu.Unlock()

	fc := &contracts.FileChange{
		SessionID: r.sessionID,
		Path:      path,
		Change:    change,
		Origin:    contracts.OriginToolUse,
		Actor:     r.sessionID,
		Timestamp: time.Now(),
	}
	if err := r.data.RecordFileChange(context.Background(), fc); err != nil {
		log.Printf("runner: record file change %s: %v", path, err)
	}
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err) // v is always a static map[string]string literal
	}
	return raw
}
