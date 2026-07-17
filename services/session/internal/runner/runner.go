// Package runner drives one interactive provider session for one MADS
// session (spec §15): it starts or resumes the provider session, serializes
// user turns onto it, fans out provider events to subscribers, and keeps the
// Data Service's session record (Activity, ProviderRef) in sync. It also
// hosts the synthesis engine (spec §4, services/session/internal/synth),
// feeding it every user turn and provider event.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/capability"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/githook"
	"github.com/drewkidwell/mads/pkg/vault"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/session/internal/synth"
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

// ErrSwitchBusy is returned by SwitchTransport when a turn is in flight —
// single-writer discipline: the caller must Interrupt before switching.
var ErrSwitchBusy = errors.New("runner: cannot switch transport mid-turn")

// ErrNotPTYMode is returned by TerminalWrite/TerminalResize when the
// current transport isn't a terminal session.
var ErrNotPTYMode = errors.New("runner: session is not in terminal mode")

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

	// FastModel backs the synthesis engine's structured drafting calls.
	// Callers set it (from main.go) before calling Start, same pattern as
	// Cwd/WorktreeRoot. Nil is fine when the session's SynthesisMode is off.
	FastModel *fastmodel.Runner

	// CredStore resolves capability credential env vars from the local
	// restricted-permission credential store (spec §13.1). Callers set it
	// (from main.go) before calling Start; nil disables all capability
	// wiring — same nil-safe convention as FastModel.
	CredStore *credstore.Store

	// MCPRegistry coordinates system-wide MCP server processes (spec §12.5).
	// Nil disables capability wiring the same way a nil CredStore does.
	MCPRegistry *capability.Registry

	// VaultResolver resolves capability credential env vars that reference an
	// external vault (spec §13.8), for names credstore doesn't have. Nil
	// disables vault resolution entirely — capabilities fall back to
	// credstore-only behavior exactly as before this field existed.
	VaultResolver *vault.Resolver

	wt *worktree.Manager // built lazily the first time isolation is needed

	synthEngine *synth.Engine // built in Start from sess.SynthesisMode; nil-safe no-op if off

	// Died is closed exactly once if the provider process ends mid-turn
	// without us having initiated the close (the Router's crash-supervision
	// signal, spec §15). The caller (main.go) selects on this and exits the
	// process immediately, without running the clean-shutdown path — that
	// path belongs to the Router's supervisor, which reacts to this OS
	// process exiting.
	Died chan struct{}
	died sync.Once

	hub *hub

	// opMu serializes SwitchTransport against Shutdown — both hold this for
	// their entire duration (closing the old provider process blocks on it
	// actually exiting), so a Shutdown can't tear down mid-switch and a
	// switch can't install a new session after Shutdown already marked this
	// one inactive. Distinct from mu, which only ever guards a single field
	// read/write at a time.
	opMu sync.Mutex

	mu           sync.Mutex
	sess         provider.Session
	providerName string // sess's provider name, set alongside sess so Transcript can look the provider back up
	busy         bool   // a turn is in flight (Send called, no EventTurnEnd yet)
	closing      bool   // Shutdown has started tearing this session down

	// lastRef is the provider-native ref of the most recently started
	// session, kept even while sess == nil mid-switch — SwitchTransport
	// resumes it, and Transcript falls back to it so the live transcript
	// keeps serving during a switch instead of going blank.
	lastRef string

	// prov and baseOpts are captured once at the end of Start and reused by
	// SwitchTransport, which only overrides Mode/Resume — switching
	// transport doesn't redo worktree creation, hook install, or capability
	// setup.
	prov     provider.Provider
	baseOpts provider.StartOptions

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
	// Isolated worktrees get the hook via Manager.Create; this covers the
	// non-isolated case where the session works directly on a real repo.
	r.installHook(ctx, cwd)

	capEnv := r.setupCapabilities(ctx, sess, cwd)

	opts := provider.StartOptions{
		Mode:   provider.ModeACP,
		Model:  sess.Model,
		Cwd:    cwd,
		Resume: sess.ProviderRef,
		Env:    capEnv,
		Trust:  provider.Trust(sess.Trust),
	}
	provSess, err := p.Start(ctx, opts)
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

	r.synthEngine = synth.New(sess.SynthesisMode, r.sessionID, dataclient.New(r.data.BaseURL(), "synthesizer"), r.FastModel)

	r.mu.Lock()
	r.sess = provSess
	r.providerName = sess.Provider
	r.prov = p
	r.baseOpts = opts
	r.lastRef = provSess.Ref()
	r.mu.Unlock()

	go r.pump(provSess)
	return nil
}

// installHook installs the mads post-commit hook (spec §5) in the session's
// working tree and points it at the Data Service, so interactive commits
// report with session/task linkage the same way pipeline task commits do.
// Best-effort and silent on failure (logged, not returned): dir defaults to
// "." for un-isolated sessions with no explicit -cwd, and installing
// against "." would mutate whatever repo this process happens to have been
// started from, so this only acts on an explicit directory that is itself a
// git repo.
func (r *Runner) installHook(ctx context.Context, dir string) {
	if dir == "" || dir == "." {
		return
	}
	if err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-dir").Run(); err != nil {
		return
	}
	if err := githook.EnsureInstalled(ctx, dir); err != nil {
		log.Printf("runner: install commit hook: %v", err)
		return
	}
	if err := githook.SetWorktreeConfig(ctx, dir, githook.Config{
		SessionID: r.sessionID,
		DataURL:   r.data.BaseURL(),
	}); err != nil {
		log.Printf("runner: configure commit hook: %v", err)
	}
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

	r.synthEngine.OnUserSend(text)

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

// SwitchTransport tears down the current transport's provider process —
// waiting for it to fully exit, not merely signaling it — and starts a new
// one in the requested mode, resuming the same provider-native ref (spec:
// one conversation, two transports, single-writer handoff). Claude-style
// harnesses refuse a second attachment to a still-live ref, so the old
// process must be dead before the new one starts; provider.Session.Close is
// documented to block on exactly that for every real transport.
//
// Refused with ErrSwitchBusy while a turn is in flight — the caller (UI)
// offers interrupt-then-switch as the escape hatch. Idempotent: asking to
// switch to the mode already active is a no-op that returns the current
// ref without touching the provider process.
func (r *Runner) SwitchTransport(ctx context.Context, mode provider.Mode) (string, error) {
	r.opMu.Lock()
	defer r.opMu.Unlock()

	r.mu.Lock()
	if r.prov == nil {
		r.mu.Unlock()
		return "", ErrNotReady
	}
	if r.busy {
		r.mu.Unlock()
		return "", ErrSwitchBusy
	}
	if r.sess != nil && r.sess.Mode() == mode {
		ref := r.sess.Ref()
		r.mu.Unlock()
		return ref, nil
	}
	old := r.sess
	resumeRef := r.lastRef
	prov := r.prov
	opts := r.baseOpts
	// Nil sess before releasing the lock: Send/Interrupt/TerminalWrite see
	// "no transport" (ErrNotReady) for the whole switch, and pumpPTY's
	// crash check (below) relies on this happening-before old.Close().
	r.sess = nil
	r.mu.Unlock()

	if old != nil {
		if err := old.Close(); err != nil {
			log.Printf("runner: close previous transport for %s: %v", r.sessionID, err)
		}
	}

	opts.Mode = mode
	opts.Resume = resumeRef

	newSess, err := prov.Start(ctx, opts)
	if err != nil {
		// Leave r.sess == nil: the session has no live transport until the
		// caller retries the switch or reopens the mode it just left. Never
		// fall back silently — a wrong-mode transport is worse than none.
		return "", fmt.Errorf("runner: switch transport to %s: %w", mode, err)
	}

	r.mu.Lock()
	r.sess = newSess
	r.lastRef = newSess.Ref()
	r.mu.Unlock()

	// Dispatch on the requested mode, not a type assertion on newSess: a
	// concrete session type may structurally satisfy PTYSession (Terminal/
	// Resize methods present) even when it was started in ACP mode and has
	// no live terminal behind those methods (providers/fake does this — one
	// struct backs both modes).
	if pty, ok := newSess.(provider.PTYSession); ok && mode == provider.ModePTY {
		go r.pumpPTY(pty)
	} else {
		go r.pump(newSess)
	}

	return newSess.Ref(), nil
}

// TerminalWrite writes raw bytes to the current transport's pty, bypassing
// Send entirely — claudecode's PTY-mode Send wraps text with a submit
// convention that's wrong for interactive keystrokes. ErrNotPTYMode if the
// current transport isn't a terminal session.
func (r *Runner) TerminalWrite(p []byte) error {
	r.mu.Lock()
	sess := r.sess
	r.mu.Unlock()
	if sess == nil {
		return ErrNotReady
	}
	// Gate on Mode, not just a type assertion: a concrete session type may
	// structurally satisfy PTYSession even when it was started in ACP mode
	// (providers/fake does this — one struct backs both modes).
	if sess.Mode() != provider.ModePTY {
		return ErrNotPTYMode
	}
	pty, ok := sess.(provider.PTYSession)
	if !ok {
		return ErrNotPTYMode
	}
	_, err := pty.Terminal().Write(p)
	return err
}

// TerminalResize propagates a terminal resize to the current transport's
// pty. ErrNotPTYMode if the current transport isn't a terminal session.
func (r *Runner) TerminalResize(cols, rows int) error {
	r.mu.Lock()
	sess := r.sess
	r.mu.Unlock()
	if sess == nil {
		return ErrNotReady
	}
	if sess.Mode() != provider.ModePTY {
		return ErrNotPTYMode
	}
	pty, ok := sess.(provider.PTYSession)
	if !ok {
		return ErrNotPTYMode
	}
	return pty.Resize(cols, rows)
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
	r.opMu.Lock()
	defer r.opMu.Unlock()

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
// EventTurnEnd and we did not initiate the close ourselves (Shutdown or a
// SwitchTransport replacing this session), the provider process died
// mid-turn: broadcast a synthetic error and signal Died so main.go can exit
// — without duplicating the Router's own crash bookkeeping (lease release,
// session.crashed), which fires independently when this OS process exits.
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
		r.synthEngine.OnEvent(ev)
		r.hub.broadcast(ev)
	}

	r.mu.Lock()
	// The identity check matters, not just busy: SwitchTransport nils
	// r.sess and starts a new session concurrently with this goroutine
	// winding down from an intentional close. Without it, the new
	// session's Send could flip busy back to true and race this check into
	// a false "crashed" report for an old session we closed on purpose.
	crashed := !r.closing && r.busy && r.sess == sess
	r.mu.Unlock()
	if !crashed {
		return
	}

	log.Printf("runner: provider session for %s ended unexpectedly mid-turn", r.sessionID)
	r.hub.broadcast(provider.Event{Kind: provider.EventError, Text: "provider process exited unexpectedly"})
	r.died.Do(func() { close(r.Died) })
}

// pumpPTY is pump's PTY-mode counterpart: it relays raw terminal bytes as
// EventText chunks on the same hub pump uses for ACP's structured events, so
// GET /output serves both transports uniformly.
//
// Interactive terminal input goes through TerminalWrite, never Send, so PTY
// mode's own traffic never sets busy. But Send is still a valid call in PTY
// mode per the Session interface (and providers/fake's tests exercise it,
// as the only way to get a fake turn recorded into shared transcript
// history while "in terminal mode") — so if Events() is non-nil, drain it
// purely for busy-clearing/tool-tracking/synth bookkeeping, without
// rebroadcasting: providers/fake mirrors every chunk onto both Events and
// Terminal, and rebroadcasting here would double the text a subscriber
// sees. A real PTY provider's Events() is nil (per the interface's own
// doc), so this loop simply never runs there.
//
// The crash check can't use busy (PTY mode's own traffic never sets it);
// it compares sess against the runner's current session by identity
// instead. SwitchTransport nils r.sess before intentionally closing the
// old session (and Shutdown never clears r.sess at all, so closing already
// covers that case), so a mismatch here only happens when nothing
// intentional caused the close.
func (r *Runner) pumpPTY(sess provider.PTYSession) {
	var wg sync.WaitGroup
	if events := sess.Events(); events != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ev := range events {
				if ev.Kind == provider.EventTurnEnd {
					r.mu.Lock()
					r.busy = false
					r.mu.Unlock()
				}
				if ev.Kind == provider.EventToolUse && ev.Tool != nil && ev.Tool.Path != "" {
					r.recordFileChange(ev.Tool.Path)
				}
				r.synthEngine.OnEvent(ev)
			}
		}()
	}

	term := sess.Terminal()
	buf := make([]byte, 4096)
	for {
		n, err := term.Read(buf)
		if n > 0 {
			r.hub.broadcast(provider.Event{Kind: provider.EventText, Text: string(buf[:n])})
		}
		if err != nil {
			break
		}
	}
	wg.Wait()

	r.mu.Lock()
	crashed := !r.closing && r.sess == provider.Session(sess)
	r.mu.Unlock()
	if !crashed {
		return
	}

	log.Printf("runner: provider session for %s ended unexpectedly (terminal)", r.sessionID)
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
