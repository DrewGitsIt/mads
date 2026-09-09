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
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// ErrNoResumeRef is returned by swapTransport when there is no resumable
// provider-native ref yet (mid-first-connection, before the wire
// announcement or first turn ever established one) — cycling the process
// here would silently start a brand new, unrelated provider session instead
// of resuming, discarding conversation history. RefreshOrchestrate treats
// this as "leave pending_restart and retry later" rather than a hard error.
var ErrNoResumeRef = errors.New("runner: cannot cycle transport before a resumable ref exists")

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

	// RouterURL is the Router's own base URL, passed as MADS_ROUTER_URL to
	// the orchestrate capability's MCP server process (docs/
	// LINKED-SESSIONS-DESIGN.md, setupOrchestrate) — the one thing the
	// Runner needs to reach the Router directly rather than through r.data
	// (the Data Service client). Callers set it (from -router-url) before
	// calling Start, same pattern as Cwd/WorktreeRoot.
	RouterURL string

	// SelfURL is this session-service process's own bound HTTP address
	// (e.g. "http://127.0.0.1:PORT"), set by main.go once the listener is
	// bound and before calling Start (same pattern as Cwd/WorktreeRoot).
	// Empty disables the permission-prompt hook entirely (e.g. in tests that
	// never serve HTTP) — Start only sets opts.PermissionPromptURL when this
	// is non-empty.
	SelfURL string

	// PermissionTimeout bounds how long an unanswered permission request sits
	// pending before RequestPermission (permissions.go) resolves it to a
	// deny on its own. Zero defaults to defaultPermissionTimeout.
	PermissionTimeout time.Duration

	// permMu guards permPending and permSeq (permissions.go) — the in-memory
	// registry of pending/resolved permission requests for this session.
	// Never persisted: a process restart loses in-flight requests
	// (acceptable — the acceptance criteria only require surviving a
	// transcript refetch, not a process bounce).
	permMu      sync.Mutex
	permPending map[string]*pendingPermission
	permSeq     int

	wt *worktree.Manager // built lazily the first time isolation is needed

	synthEngine *synth.Engine // built in Start from sess.SynthesisMode; nil-safe no-op if off

	// digest accumulates the in-flight turn's activity for the per-turn
	// digest push (docs/LINKED-SESSIONS-DESIGN.md decision 4). Always
	// active — unlike synthEngine it isn't mode-gated, since whether anyone
	// reads the digest is the Router digest watcher's call, not this
	// session's.
	digest digestAccumulator

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

	// mode is the transport mode last successfully established — set
	// alongside sess in Start and SwitchTransport, and read back by Mode().
	// Kept as its own field rather than derived from sess.Mode() so a caller
	// still gets a meaningful answer during the brief window mid-switch where
	// sess is nil (old transport closed, new one not yet started).
	mode provider.Mode

	// pending holds injected messages that arrived while busy (decision 1,
	// docs/LINKED-SESSIONS-DESIGN.md): inject never interrupts an in-flight
	// turn, it queues behind it. onTurnEnd pops and delivers the head of
	// this slice, FIFO, once the current turn ends.
	pending []pendingInjection

	// orchestrateRefreshPending is set by RefreshOrchestrate when a live
	// materialize/teardown cycle was requested while a turn was in flight —
	// swapTransport refuses to run mid-turn (ErrSwitchBusy), so the request
	// is deferred and drained at the next turn boundary, the same
	// deferred-at-turn-boundary pattern `pending` (queued injections) above
	// already uses. Guarded by mu.
	orchestrateRefreshPending bool

	// sentTurns counts every turn sent so far (Send and Inject alike),
	// 1-indexed — the ordinal Transcript correlates against turnAttributions
	// to attach the right Attribution to the right user-role message.
	sentTurns int

	// turnAttributions records, by sentTurns ordinal, the Attribution of
	// turns that were injected (absent for ordinary user turns). In-memory
	// only: lost on session bounce/restart, since the provider's own
	// transcript has no metadata channel to persist into.
	turnAttributions map[int]contracts.Attribution

	// lastRef is the last-observed provider-native ref, kept in sync by
	// watchRefAnnounce as soon as the provider announces it (see providers/
	// provider.RefAnnouncer) and by syncProviderRef as a turn-end fallback,
	// and kept even while sess == nil mid-switch. It's the SwitchTransport
	// fallback when there's no outgoing session to read old.Ref() from
	// directly, and Transcript falls back to it so the live transcript
	// keeps serving during a switch instead of going blank.
	lastRef string

	// recordMu serializes every load-modify-write of the session RECORD
	// (UpdateSession round trips). watchRefAnnounce made the runner's ref
	// persist a background write for the first time — unserialized, it can
	// interleave with Start's or Shutdown's own read-modify-write and
	// silently clobber fields either direction (a lost update, seen as
	// nondeterministic runner-test failures under load). persistedRef is
	// guarded by recordMu: it holds the last ref actually written, so
	// dedupe happens on persisted success, never on the in-memory lastRef
	// — marking lastRef before the write lands made the turn-end fallback
	// skip while the announce write was still in flight (or had failed).
	recordMu     sync.Mutex
	persistedRef string

	// prov and baseOpts are captured once at the end of Start and reused by
	// SwitchTransport, which only overrides Mode/Resume — switching
	// transport doesn't redo worktree creation, hook install, or capability
	// setup.
	prov     provider.Provider
	baseOpts provider.StartOptions

	// spawnedAt and providerCwd are captured once in Start, right before
	// p.Start(ctx, opts) and from the same resolved cwd that went into
	// opts.Cwd (post-worktree-isolation) — the exact (cwd, spawnedAfter)
	// pair locateProviderRef needs to hand a provider.SessionLocator.
	spawnedAt   time.Time
	providerCwd string
	// locateArmed guards the SessionLocator backstop to fire at most once
	// per Runner: it's armed on the very first Send, when the first message
	// text becomes known. SwitchTransport does not re-arm it — by the time a
	// session has switched transports mid-conversation, either the wire
	// announcement already fired or PTY discovery already had a chance to
	// succeed; the acceptance criteria only cover a fresh session's first
	// message, so this is a known, accepted gap rather than something built
	// for.
	locateArmed bool

	// shutdownCh is closed exactly once, alongside r.closing being set in
	// Shutdown, and cancels locateProviderRef's best-effort-forever PTY
	// window — the Runner-level lifetime signal mirroring ptySession's old
	// internal "done" channel one layer up.
	shutdownCh chan struct{}

	// touched maps each path reported this run to the last verdict recorded
	// for it ("added"/"modified"/"deleted"). The first touch of a path is
	// classified by stat in recordFileChange; the verdict is remembered here
	// rather than re-derived because the UI's collapseChanges reads a first
	// touch of "added" as created-in-session, so every later touch must be
	// "modified" — except after a recorded "deleted", which resets
	// classification so a recreated path reads as created again.
	touched map[string]string
}

// pendingInjection is one queued Inject call, waiting for the in-flight
// turn to end.
type pendingInjection struct {
	text        string
	attribution contracts.Attribution
}

// New builds a Runner for sessionID. data's actor must be sessionID (spec
// §15's per-service actor convention) so events and lease claims attribute
// correctly.
func New(sessionID string, data *dataclient.Client, providers *provider.Registry) *Runner {
	return &Runner{
		sessionID:        sessionID,
		data:             data,
		providers:        providers,
		Cwd:              ".",
		Died:             make(chan struct{}),
		hub:              newHub(),
		touched:          make(map[string]string),
		turnAttributions: make(map[int]contracts.Attribution),
		shutdownCh:       make(chan struct{}),
		permPending:      make(map[string]*pendingPermission),
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

	agent, agentOK := r.loadAgent(ctx, sess)
	capEnv := r.setupCapabilities(ctx, cwd, agent, agentOK)
	orchestrateMCP := r.setupOrchestrate(ctx, sess)

	opts := provider.StartOptions{
		Mode:         provider.ModeACP,
		Model:        sess.Model,
		Cwd:          cwd,
		SystemPrompt: agent.Prompt, // "" when agentOK is false — no mads preamble, ever
		Resume:       sess.ProviderRef,
		Env:          capEnv,
		Trust:        provider.Trust(sess.Trust),
	}
	if orchestrateMCP != nil {
		opts.MCPServers = []provider.MCPServerRef{*orchestrateMCP}
	}
	if r.SelfURL != "" {
		opts.PermissionPromptURL = r.SelfURL + "/permission-hook"
	}
	spawnedAt := time.Now()
	provSess, err := p.Start(ctx, opts)
	if err != nil {
		return fmt.Errorf("runner: start provider session: %w", err)
	}

	// Persist the resume handle before returning success, so a crash right
	// after this point still leaves a resumable ref recorded.
	sess.ProviderRef = provSess.Ref()
	sess.Activity = contracts.SessionActive
	sess.Cwd = cwd // the resolved working dir the agent runs in — surfaced for the docked shell
	r.recordMu.Lock()
	if err := r.data.UpdateSession(ctx, &sess); err != nil {
		r.recordMu.Unlock()
		return fmt.Errorf("runner: persist provider ref: %w", err)
	}
	r.persistedRef = sess.ProviderRef
	r.recordMu.Unlock()

	if orchestrateMCP != nil {
		// A fresh spawn always launches with the current desired MCP set
		// already in opts.MCPServers (set above) — there is no "pending"
		// case at Start, only whether this provider actually wires launch-
		// time MCP config in at all (Matrix's BehaviorMCPServers).
		status := contracts.OrchestrateUnsupported
		if p.Matrix()[provider.BehaviorMCPServers].Works() {
			status = contracts.OrchestrateActive
		}
		r.markOrchestrateLinks(ctx, status)
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
	r.mode = opts.Mode
	r.lastRef = provSess.Ref()
	r.spawnedAt = spawnedAt
	r.providerCwd = cwd
	r.mu.Unlock()

	r.watchRefAnnounce(ctx, provSess)

	go r.pump(provSess)
	r.maybeRequestCompact(ctx, provSess, sess.LastUsage, sess.Model)
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
	r.recordMu.Lock()
	// sess was loaded before this lock; don't let this write revert a ref
	// that persistProviderRef landed in the meantime.
	if r.persistedRef != "" {
		sess.ProviderRef = r.persistedRef
	}
	err = r.data.UpdateSession(ctx, &sess)
	r.recordMu.Unlock()
	if err != nil {
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

// deliverLocked marks a turn as in-flight and records its ordinal — and,
// for an injected turn, the Attribution Transcript will later attach to the
// resulting user-role message. Must be called with r.mu held; the caller
// sends on r.sess itself, after unlocking.
func (r *Runner) deliverLocked(attribution *contracts.Attribution) {
	r.busy = true
	r.sentTurns++
	if attribution != nil {
		r.turnAttributions[r.sentTurns] = *attribution
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
	var armed bool
	var prov provider.Provider
	var mode provider.Mode
	var cwd string
	var spawnedAt time.Time
	if !r.locateArmed && r.lastRef == "" && r.sentTurns == 0 {
		r.locateArmed = true
		armed = true
		prov = r.prov
		mode = r.baseOpts.Mode
		cwd = r.providerCwd
		spawnedAt = r.spawnedAt
	}
	r.deliverLocked(nil)
	sess := r.sess
	r.mu.Unlock()

	if armed {
		go r.locateProviderRef(prov, mode, cwd, spawnedAt, text)
	}

	r.synthEngine.OnUserSend(text)
	r.digest.recordUser(text, false)

	if err := sess.Send(ctx, text); err != nil {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
		return err
	}
	return nil
}

// Inject delivers a message from a linked session into this session's
// conversation (docs/LINKED-SESSIONS-DESIGN.md decisions 1-2). It never
// interrupts an in-flight turn: if the runner is busy, the message queues
// and onTurnEnd delivers it once the current turn ends, FIFO with any other
// queued injections. Trust capping (decision 3) needs no extra plumbing
// here — sess.Send is the same call Send makes, so an injected turn is
// bound by the subject session's own trust level exactly as a user turn is.
func (r *Runner) Inject(ctx context.Context, text string, attribution contracts.Attribution) error {
	r.mu.Lock()
	if r.sess == nil {
		r.mu.Unlock()
		return ErrNotReady
	}
	if r.busy {
		r.pending = append(r.pending, pendingInjection{text: text, attribution: attribution})
		r.mu.Unlock()
		r.recordInjectionAudit(ctx, attribution, text)
		return nil
	}
	r.deliverLocked(&attribution)
	sess := r.sess
	r.mu.Unlock()

	r.synthEngine.OnUserSend(text)
	r.digest.recordUser(text, attribution.DigestAttributed)
	r.recordInjectionAudit(ctx, attribution, text)

	if err := sess.Send(ctx, text); err != nil {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
		return err
	}
	return nil
}

// recordInjectionAudit appends a LinkMessageInjected event scoped to the
// link's own actor identity, not r.data — r.data is bound to this session's
// own actor (spec §15's per-service convention), and an injected message
// must attribute to the link, not the subject. Best-effort: a failure here
// must not block delivery, since the message has already been queued or
// sent by the time this runs.
func (r *Runner) recordInjectionAudit(ctx context.Context, attribution contracts.Attribution, text string) {
	client := dataclient.New(r.data.BaseURL(), "link-"+attribution.LinkID)
	env := contracts.Envelope{
		Type:      contracts.LinkMessageInjected,
		SessionID: r.sessionID,
		Payload: mustJSON(map[string]string{
			"link_id":     attribution.LinkID,
			"source_name": attribution.SourceName,
			"text":        text,
		}),
	}
	if err := client.Append(ctx, &env); err != nil {
		log.Printf("runner: append link.message_injected: %v", err)
	}
}

// onTurnEnd clears the in-flight flag and, if any injections queued while
// busy, delivers the next one — all under one continuous lock hold, so a
// concurrent Send or Inject can't steal the slot ahead of a message that
// was already queued (FIFO ordering, decision 1). A run of queued digest
// pushes (job.GenerateDigest, decision 4) from the same link at the front
// of the queue is coalesced into a single forced turn instead of one per
// queued digest — see drainNextLocked. Any other queued injection (a real
// user/leader message, or a digest from a different link) stops the
// coalescing there and keeps its own individual, in-order turn.
//
// It also drains the digest accumulator and pushes the turn's delta
// (docs/LINKED-SESSIONS-DESIGN.md decision 4) before clearing busy, so a
// concurrent Send/Inject for the next turn can never record into this
// turn's buffer.
func (r *Runner) onTurnEnd() {
	r.pushTurnEnded()

	r.mu.Lock()
	r.busy = false
	if len(r.pending) > 0 {
		text, attribution := r.drainNextLocked()
		r.deliverLocked(&attribution)
		sess := r.sess
		r.mu.Unlock()
		if sess == nil {
			return
		}

		r.synthEngine.OnUserSend(text)
		r.digest.recordUser(text, attribution.DigestAttributed)

		if err := sess.Send(context.Background(), text); err != nil {
			log.Printf("runner: deliver queued injection: %v", err)
			r.mu.Lock()
			r.busy = false
			r.mu.Unlock()
		}
		// orchestrateRefreshPending, if set, is left untouched here — it's
		// only drained once the queue is truly empty (below), so a refresh
		// requested mid-queue-drain retries at the next real turn boundary
		// instead of racing this queued injection's own in-flight turn.
		return
	}
	refreshPending := r.orchestrateRefreshPending
	r.orchestrateRefreshPending = false
	closing := r.closing
	r.mu.Unlock()

	if refreshPending && !closing {
		go func() {
			if err := r.RefreshOrchestrate(context.Background()); err != nil {
				log.Printf("runner: deferred orchestrate refresh: %v", err)
			}
		}()
	}
}

// drainNextLocked pops the next queued injection(s) to deliver as one
// forced turn. If the head of r.pending is a digest push
// (Attribution.DigestAttributed), every contiguous queued digest push from
// the same link is coalesced into a single combined injection — a burst of
// per_turn digests that piled up while busy becomes one forced turn, not
// one per queued digest. Any other queued injection (a real user/leader
// message, or a digest from a different link) stops the coalescing and is
// left at the front of the queue for its own turn, so non-digest ordering
// is untouched. Must be called with r.mu held; mutates r.pending in place.
func (r *Runner) drainNextLocked() (text string, attribution contracts.Attribution) {
	head := r.pending[0]
	if !head.attribution.DigestAttributed {
		r.pending = r.pending[1:]
		return head.text, head.attribution
	}

	texts := []string{head.text}
	i := 1
	for i < len(r.pending) {
		item := r.pending[i]
		if !item.attribution.DigestAttributed || item.attribution.LinkID != head.attribution.LinkID {
			break
		}
		texts = append(texts, item.text)
		i++
	}
	r.pending = r.pending[i:]
	return strings.Join(texts, "\n\n"), head.attribution
}

// pushTurnEnded renders and resets the digest accumulator, appending a
// TurnEnded envelope carrying the raw delta if the turn produced any
// activity. The Router's digest watcher does the actual compression and
// per-link fan-out (job.GenerateDigest) — this is just the trigger.
// digest_only marks a turn whose entries were solely digest-attributed
// injections (a monitor's own forced turn): digestwatch skips spawning
// another digest for it, so monitor chains can't self-amplify.
// Best-effort: a failure here logs and is skipped, never surfaced to the
// caller, so a digest problem can never perturb the watched session's own
// turn (docs/LINKED-SESSIONS-DESIGN.md decision 4).
func (r *Runner) pushTurnEnded() {
	delta, digestOnly := r.digest.renderAndReset()
	if delta == "" {
		return
	}
	env := contracts.Envelope{
		Type:      contracts.TurnEnded,
		SessionID: r.sessionID,
		Payload:   mustJSON(map[string]any{"delta": delta, "digest_only": digestOnly}),
	}
	if err := r.data.Append(context.Background(), &env); err != nil {
		log.Printf("runner: append session.turn_ended: %v", err)
	}
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
// ref without touching the provider process. A thin wrapper over
// swapTransport, carrying forward the MCP server set already in effect so a
// plain transport switch never perturbs it.
func (r *Runner) SwitchTransport(ctx context.Context, mode provider.Mode) (string, error) {
	return r.swapTransport(ctx, mode, r.currentMCPServers(), false)
}

// currentMCPServers reads the MCP server set the currently-running provider
// process was last started (or cycled) with.
func (r *Runner) currentMCPServers() []provider.MCPServerRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.baseOpts.MCPServers
}

// mcpServersEqual reports whether a and b describe the same MCP server set,
// in the same order — swapTransport's idempotence check needs this so a
// plain SwitchTransport call (which always passes the set already in
// effect) never cycles the process on account of MCP servers alone, and so
// RefreshOrchestrate's repeat calls with an unchanged desired set stay
// no-ops.
func mcpServersEqual(a, b []provider.MCPServerRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Command != b[i].Command || len(a[i].Env) != len(b[i].Env) {
			return false
		}
		for j := range a[i].Env {
			if a[i].Env[j] != b[i].Env[j] {
				return false
			}
		}
	}
	return true
}

// swapTransport is the shared "close old process, resume with the same ref,
// start a new one" cycle behind both SwitchTransport (mode changes) and
// RefreshOrchestrate (materializing/tearing down the orchestrate MCP server
// on a running session without a user-visible restart, capability.go) — same
// primitive, parameterized by mode and the MCP server set the new process
// should advertise.
//
// Refused with ErrSwitchBusy while a turn is in flight. When
// requireResumable is set (RefreshOrchestrate only — SwitchTransport passes
// false, preserving its pre-existing "start fresh, let locate-mode discover
// the ref" fallback for the mid-first-connection case, see locateArmed),
// also refused with ErrNoResumeRef when there is no resumable ref yet:
// cycling here would silently start an unrelated fresh provider session
// instead of resuming, discarding conversation history; RefreshOrchestrate
// treats that as "leave pending_restart, retry later" rather than a hard
// failure. Idempotent: mode and mcp both already matching what's live is a
// no-op that returns the current ref without touching the provider process.
func (r *Runner) swapTransport(ctx context.Context, mode provider.Mode, mcp []provider.MCPServerRef, requireResumable bool) (string, error) {
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
	if r.sess != nil && r.sess.Mode() == mode && mcpServersEqual(r.baseOpts.MCPServers, mcp) {
		ref := r.sess.Ref()
		r.mu.Unlock()
		return ref, nil
	}
	old := r.sess
	resumeRef := r.lastRef
	if old != nil {
		resumeRef = old.Ref()
	}
	if resumeRef == "" && requireResumable {
		r.mu.Unlock()
		return "", ErrNoResumeRef
	}
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
	opts.MCPServers = mcp

	newSess, err := prov.Start(ctx, opts)
	if err != nil {
		// Leave r.sess == nil: the session has no live transport until the
		// caller retries the switch or reopens the mode it just left. Never
		// fall back silently — a wrong-mode transport is worse than none.
		return "", fmt.Errorf("runner: switch transport to %s: %w", mode, err)
	}

	r.mu.Lock()
	r.sess = newSess
	r.mode = mode
	r.lastRef = newSess.Ref()
	r.baseOpts.MCPServers = mcp
	r.mu.Unlock()

	r.watchRefAnnounce(ctx, newSess)

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

// Mode reports the transport mode currently in effect (or, mid-switch,
// the mode a completed switch last established) — read by GET /transport
// so the UI can initialize its display-mode toggle from the session's real
// state on mount instead of assuming chat.
func (r *Runner) Mode() provider.Mode {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mode
}

// persistProviderRef updates r.lastRef and writes the ref to the Data
// Service unless that exact ref already landed. Shared by the turn-end sync
// path (syncProviderRef) and the eager announcement path (watchRefAnnounce)
// so both funnel through one dedupe and one round trip. Dedupe is on
// persistedRef — the last ref that actually made it to the Data Service —
// under recordMu, so the turn-end fallback blocks behind an in-flight
// announce write instead of skipping on its optimism, and a failed write
// gets retried at the next turn end rather than swallowed.
//
// It also writes ref back into the live session itself, if r.sess implements
// provider.RefSetter: without this, a ref discovered only through the
// SessionLocator backstop (locateProviderRef) never reaches the session
// object's own Ref() — which Transcript and SwitchTransport's resume handle
// both read directly — leaving them permanently blank/empty for that
// session's whole live process even though the ref is correctly persisted
// here.
func (r *Runner) persistProviderRef(ctx context.Context, ref string) {
	if ref == "" {
		return
	}
	r.mu.Lock()
	r.lastRef = ref
	if setter, ok := r.sess.(provider.RefSetter); ok {
		setter.SetRef(ref)
	}
	r.mu.Unlock()

	r.recordMu.Lock()
	defer r.recordMu.Unlock()
	if ref == r.persistedRef {
		return
	}
	cur, err := r.data.Session(ctx, r.sessionID)
	if err != nil {
		log.Printf("runner: load session to persist provider ref: %v", err)
		return
	}
	cur.ProviderRef = ref
	if err := r.data.UpdateSession(ctx, &cur); err != nil {
		log.Printf("runner: persist provider ref: %v", err)
		return
	}
	r.persistedRef = ref
}

// syncProviderRef re-checks sess's provider-native ref and persists it if it
// has changed since this runner last observed it. Called once per turn end,
// not per event — the fallback path for providers/transports that never
// announce eagerly (watchRefAnnounce below), and a no-op in the common case
// where the ref was already captured at connect.
func (r *Runner) syncProviderRef(ctx context.Context, sess provider.Session) {
	r.persistProviderRef(ctx, sess.Ref())
}

// watchRefAnnounce persists sess's ref the moment it becomes known, without
// polling. If Ref() is already non-empty (kiro-cli ACP, any resumed
// session), persist synchronously right here. Otherwise, if the session
// implements provider.RefAnnouncer, wait on its channel in a background
// goroutine — a closed-empty receive (no announcement coming, e.g. PTY
// discovery giving up or a transport with no mechanism at all) is a no-op,
// not an error; syncProviderRef at turn end remains the fallback.
func (r *Runner) watchRefAnnounce(ctx context.Context, sess provider.Session) {
	if ref := sess.Ref(); ref != "" {
		r.persistProviderRef(ctx, ref)
		return
	}
	announcer, ok := sess.(provider.RefAnnouncer)
	if !ok {
		return
	}
	go func() {
		ref, ok := <-announcer.RefAnnounced()
		if !ok || ref == "" {
			return
		}
		r.persistProviderRef(context.Background(), ref)
	}()
}

// persistUsage writes usage as the session record's LastUsage — the context
// meter's data source at wake time (ui/src/conversation/Header/
// ContextMeter.tsx, before this process has seen a turn_end of its own) and
// the resume-compaction trigger's input (compact.go's shouldCompactOnResume).
// Called on every EventTurnEnd that carries usage, unlike persistProviderRef's
// dedup-on-unchanged-value: usage legitimately changes turn to turn, so there
// is nothing to skip. Best-effort: a failure here only means the meter/trigger
// are stale for the next resume, never surfaced to the caller.
func (r *Runner) persistUsage(ctx context.Context, usage *provider.Usage) {
	if usage == nil {
		return
	}
	r.recordMu.Lock()
	defer r.recordMu.Unlock()
	cur, err := r.data.Session(ctx, r.sessionID)
	if err != nil {
		log.Printf("runner: load session to persist usage: %v", err)
		return
	}
	cur.LastUsage = &contracts.Usage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
		Model:                    usage.Model,
		ContextTokens:            usage.ContextTokens,
		ContextWindow:            usage.ContextWindow,
	}
	if err := r.data.UpdateSession(ctx, &cur); err != nil {
		log.Printf("runner: persist usage: %v", err)
	}
}

// refLocateACPWindow bounds the SessionLocator backstop on ACP: the wire
// announcement is the primary, near-instant path there (session.go's
// system/init line fires at connect, before Send is ever called — the
// common case never even reaches this goroutine, since r.lastRef is already
// non-empty by then). This only matters on the rare miss, so it doesn't run
// forever. PTY has no bound: it's the ONLY mechanism on that transport
// (mirrors the "best-effort forever" ptySession poll it replaces), so it
// runs for the rest of the session's life (shutdownCh).
const refLocateACPWindow = 2 * time.Minute

// locateProviderRef runs the SessionLocator backstop, armed once by Send on
// the first turn. A no-op if prov doesn't implement provider.SessionLocator
// at all (most providers: the wire announcement or turn-end sync already
// covers them). Not re-armed by SwitchTransport — a known, accepted gap; see
// the locateArmed field doc.
func (r *Runner) locateProviderRef(prov provider.Provider, mode provider.Mode, cwd string, spawnedAt time.Time, firstMessage string) {
	locator, ok := prov.(provider.SessionLocator)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if mode != provider.ModePTY {
		var boundCancel context.CancelFunc
		ctx, boundCancel = context.WithTimeout(ctx, refLocateACPWindow)
		defer boundCancel()
	}
	go func() {
		select {
		case <-r.shutdownCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	ref, ok := locator.LocateSession(ctx, cwd, spawnedAt, firstMessage)
	if !ok {
		return
	}
	r.mu.Lock()
	closing := r.closing
	r.mu.Unlock()
	if closing {
		return
	}
	r.persistProviderRef(context.Background(), ref)
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

// SubscribeTerminal is Subscribe plus a replay of the hub's bounded PTY
// ring buffer, captured atomically with the subscription (see
// hub.subscribeWithPTYReplay). GET /output uses this so a newly attached
// terminal stream can restore prior scrollback instead of only seeing bytes
// that flow after attach; chat-mode turns never write EventPTYOutput, so
// replay is empty for a chat-only session.
func (r *Runner) SubscribeTerminal() (replay []byte, ch <-chan provider.Event, unsubscribe func()) {
	return r.hub.subscribeWithPTYReplay()
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
	close(r.shutdownCh)
	sess := r.sess
	r.mu.Unlock()

	var closeErr error
	if sess != nil {
		closeErr = sess.Close()
	}

	r.recordMu.Lock()
	if cur, err := r.data.Session(ctx, r.sessionID); err != nil {
		log.Printf("runner: load session for shutdown: %v", err)
	} else {
		cur.Activity = contracts.SessionInactive
		if err := r.data.UpdateSession(ctx, &cur); err != nil {
			log.Printf("runner: mark session inactive: %v", err)
		}
	}
	r.recordMu.Unlock()
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
			// Before onTurnEnd: it can start a queued next turn, whose
			// tools must not blur which turn deleted a touched file.
			r.sweepDeletedFiles()
			r.onTurnEnd()
			r.syncProviderRef(context.Background(), sess)
			r.persistUsage(context.Background(), ev.Usage)
		}
		if ev.Kind == provider.EventToolUse && ev.Tool != nil && ev.Tool.Path != "" && ev.Tool.Mutates {
			r.recordFileChange(ev.Tool.Path)
		}
		r.synthEngine.OnEvent(ev)
		r.digest.recordEvent(ev)
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
// EventPTYOutput chunks on the same hub pump uses for ACP's structured
// events, so GET /output serves both transports uniformly. EventPTYOutput
// (not EventText) keeps these bytes out of chat-mode consumers, which only
// listen for EventText, and is what the hub buffers for terminal replay on
// reattach (see hub.writePTY).
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
					r.sweepDeletedFiles()
					r.onTurnEnd()
					r.syncProviderRef(context.Background(), sess)
					r.persistUsage(context.Background(), ev.Usage)
				}
				if ev.Kind == provider.EventToolUse && ev.Tool != nil && ev.Tool.Path != "" && ev.Tool.Mutates {
					r.recordFileChange(ev.Tool.Path)
				}
				r.synthEngine.OnEvent(ev)
				r.digest.recordEvent(ev)
			}
		}()
	}

	term := sess.Terminal()
	buf := make([]byte, 4096)
	for {
		n, err := term.Read(buf)
		if n > 0 {
			r.hub.broadcast(provider.Event{Kind: provider.EventPTYOutput, Text: string(buf[:n])})
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

// recordFileChange reports one path the agent's tools wrote (Tool.Mutates —
// reads carry a Path too but are not changes) to the Data Service (spec §5).
// A path's first touch this run is classified by stat: providers announce
// tool_use on the wire before the tool executes (providers/claudecode
// wire.go emits it from the "assistant" message, ahead of the "user"
// tool_result), so a path that already exists predates the session's edit
// ("modified") and a missing one is about to be created ("added"). The stat
// races the tool on a provider that only reports tool_use after executing
// it (providers/fake), where a session-created file resolves as "modified"
// — the under-claiming direction. Best effort throughout: a failed stat
// falls back to the old first-sighting "added" rather than interrupting
// the turn. Later touches are "modified" per r.touched's convention.
func (r *Runner) recordFileChange(path string) {
	r.mu.Lock()
	base := r.touchBaseLocked()
	prev, seen := r.touched[path]
	r.mu.Unlock()

	change := "modified"
	if !seen || prev == "deleted" {
		// Missing means the tool is about to create it; any stat failure
		// keeps the old first-sighting verdict rather than dropping the
		// touch.
		change = "added"
		if _, err := os.Stat(touchAbs(base, path)); err == nil {
			change = "modified"
		}
	}

	r.mu.Lock()
	r.touched[path] = change
	r.mu.Unlock()

	r.appendFileChange(path, change, base)
}

// sweepDeletedFiles runs at turn end, once every tool of the turn has
// finished: a deletion (e.g. a Bash rm) carries no Path of its own on the
// wire, so it is only observable as a previously touched path that no
// longer exists. Only a definite absence counts — a stat failure other than
// not-exist is no evidence the file is gone.
func (r *Runner) sweepDeletedFiles() {
	r.mu.Lock()
	base := r.touchBaseLocked()
	paths := make([]string, 0, len(r.touched))
	for path, change := range r.touched {
		if change != "deleted" {
			paths = append(paths, path)
		}
	}
	r.mu.Unlock()
	slices.Sort(paths) // map order would make multi-deletion ledger order flap

	for _, path := range paths {
		if _, err := os.Stat(touchAbs(base, path)); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		r.mu.Lock()
		r.touched[path] = "deleted"
		r.mu.Unlock()
		r.appendFileChange(path, "deleted", base)
	}
}

// touchBaseLocked is the dir tool-reported relative paths resolve against —
// providerCwd once Start has resolved worktree isolation, r.Cwd before.
// Caller holds r.mu.
func (r *Runner) touchBaseLocked() string {
	if r.providerCwd != "" {
		return r.providerCwd
	}
	return r.Cwd
}

// touchAbs resolves a tool-reported path for stat; the ledger keeps the
// path exactly as reported.
func touchAbs(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

// appendFileChange records one row of the append-only §5 ledger. Repo is
// the session's resolved working root (its worktree when isolated), the
// spec §17 multi-repo hook. Best effort: a failed append is logged, never
// interrupts the turn.
func (r *Runner) appendFileChange(path, change, repo string) {
	fc := &contracts.FileChange{
		SessionID: r.sessionID,
		Repo:      repo,
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
