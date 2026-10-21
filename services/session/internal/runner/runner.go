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
	"github.com/drewkidwell/mads/pkg/delegates"
	"github.com/drewkidwell/mads/pkg/githook"
	"github.com/drewkidwell/mads/pkg/inbound"
	"github.com/drewkidwell/mads/pkg/linkwrap"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/pkg/toolstatus"
	"github.com/drewkidwell/mads/pkg/vault"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/session/internal/shellwatch"
	"github.com/drewkidwell/mads/services/session/internal/synth"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// ErrNotReady is returned by Send/Interrupt before Start has completed.
var ErrNotReady = errors.New("runner: session not ready")

// ErrTurnInFlight is returned by Send when a turn is already in progress —
// one turn at a time, matching the chat UX (composer disabled while
// streaming).
var ErrTurnInFlight = errors.New("runner: turn already in flight")

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
	// under WorktreeRoot instead of running directly on Cwd (spec §17,
	// docs/SESSION-WORKTREES-DESIGN.md D1).
	Cwd string

	// WorktreeRoot is where session worktrees live (outside Cwd): main.go
	// sets $MADS_HOME/session-worktrees. Empty disables isolation, which is
	// what every test that doesn't set it gets.
	WorktreeRoot string

	// ShadowRoot is the base dir for per-session shadow git stores
	// (docs/SHADOW-GIT-DIFFS-ADR.md; services/session/shadow) — normally
	// $XDG_DATA_HOME/mads/shadow, set by main.go. Empty (the default)
	// disables shadowing entirely, same convention as WorktreeRoot. Also
	// skipped when the session's resolved cwd is "." — mirroring
	// installHook's guard, a shadow of "whatever directory this process
	// happens to run from" would baseline an arbitrary repo.
	ShadowRoot string

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

	// MadsCmd is the mads CLI binary the orchestrate capability runs as
	// `<MadsCmd> internal orchestrate-mcp` (setupOrchestrate). main.go sets
	// it from -mads-cmd, which defaults to ResolveMadsCmd's pick; empty
	// means "mads" from $PATH.
	MadsCmd string

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

	// wt is where this session's work lives for the header counts (D4),
	// set once in Start; zero when there is nowhere to count (a plain
	// folder, an implicit cwd). Guarded by mu.
	wt worktreeState

	// countsMu guards countsRunning/countsAgain, which coalesce turn-end
	// recounts (refreshCountsAsync) into at most one running and one queued.
	// countsWG lets Shutdown wait out the one running, so it never writes
	// to the record after the session is marked inactive.
	countsMu      sync.Mutex
	countsRunning bool
	countsAgain   bool
	countsClosed  bool
	countsWG      sync.WaitGroup

	// shadowStore is this session's shadow git store, built in Start when
	// ShadowRoot is set (nil otherwise). Every shadow.Store method is
	// nil-safe, so the pump hooks call it unconditionally; every operation
	// inside is best-effort and can never block or fail the agent's turn.
	shadowStore *shadow.Store

	// shell finds the files the session's shell commands write, for a
	// session working in place (shellfiles.go); nil otherwise. Set in Start
	// before the pump runs and never after.
	shell *shellwatch.Tracker

	synthEngine *synth.Engine // built in Start from sess.SynthesisMode; nil-safe no-op if off

	// toolStatus synthesizes tool-call lifecycle statuses (pkg/toolstatus —
	// the one authoritative rule set, docs/CONVERSATION-REDESIGN-TASKS.md
	// T5). Fed by the pump (tool_use/tool_result/turn_end via
	// observeConversation, conversation.go) and by the permission flow
	// (RequestPermission/finishPermission, permissions.go). Own internal
	// lock — those two run on different goroutines.
	toolStatus *toolstatus.Tracker

	// agent tracks what this session's main agent is doing and appends
	// session.agent_state on transitions (agentstate.go) — the control
	// plane's only turn-start/turn-activity signal, which the session rail
	// reads for every session at once. Own internal lock, like toolStatus:
	// the pump, Send/Inject and a coalescing timer all drive it.
	agent agentState

	// delegates owns this session's delegate lifecycle (pkg/delegates,
	// docs/DELEGATES-DESIGN.md) — harness subagents, decided in one place
	// and written through as delegate records (delegates.go). Fed by the
	// pump (task lifecycle, roster, turn end), the process ending, and a
	// roster-expiry timer. Own internal lock.
	delegates   *delegates.Tracker
	delegatePub delegatePublisher

	// tally counts what the session has used — the running totals, the
	// harness's billing snapshot and when it last spoke (usage.go). Fed by
	// observeConversation; own internal lock.
	tally usageTally

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
	busy         bool   // a turn is in flight (Send called, or an inbound message woke the agent; no EventTurnEnd yet)
	closing      bool   // Shutdown has started tearing this session down

	// landing is set while Land runs (worktreeops.go): no turn may start,
	// so Send refuses, and SendOrQueue and Inject queue behind it exactly
	// as they queue behind a turn. endLanding releases the queue.
	landing bool
	// landDone is closed when the Land holding landing ends; Shutdown
	// waits on it (bounded by its context) before tearing anything down.
	landDone chan struct{}

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

	// pendingModel holds a model change requested while a turn was in
	// flight — same deferred-at-turn-boundary pattern as
	// orchestrateRefreshPending, for the same reason: applying a model
	// means cycling the provider process (--model is spawn-time for every
	// provider that takes one, e.g. providers/claudecode/session.go), and
	// swapTransport refuses to cycle mid-turn. Empty means nothing queued;
	// a second request while one is already queued replaces it, since only
	// the last model asked for is the one the user wants. Guarded by mu.
	pendingModel string

	// inbound resolves inbound messages' senders (inbound.go), built on
	// first use once providerName is known. peerAddr is the last
	// session.peer_address this runner recorded. Both guarded by mu.
	inbound  *inbound.Resolver
	peerAddr string

	// sentAny is set once this runner has sent a turn (Send and Inject
	// alike). An injected turn's label isn't kept here: it rides in the
	// message text itself (pkg/linkwrap), so it survives a restart.
	sentAny bool

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

	// forkFrom is the original native ref this run is forking from while
	// the fork's own ref is still unknown — "" for an ordinary session and
	// again once the fork's ref has been persisted (persistProviderRef
	// clears it in the same write that clears Session.ForkOnResume).
	// Guarded by mu; where it is held together with recordMu, recordMu is
	// taken first.
	forkFrom string

	// rerootNote is the note still owed to the agent after a re-root
	// (rerootMissingDir), "" once claimed by the first turn sent. Guarded by
	// mu. Session.RerootNotePending is its durable twin.
	rerootNote string

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

	// toolTouchedAt is when a file tool last announced a write to each path,
	// by absolute path: a shell look skips a file the session's own tool
	// already recorded inside the same window (recordShellChanges).
	toolTouchedAt map[string]time.Time
}

// pendingInjection is one queued Inject or SendOrQueue call, waiting for
// the in-flight turn to end.
type pendingInjection struct {
	text        string
	attribution contracts.Attribution
	// wire is what the provider is sent for this entry, set by
	// drainNextLocked: text inside its wrapper for an injection (each
	// digest wrapped on its own when a run coalesces), text as-is for a
	// user entry.
	wire string
	// preWire is a multi-part injection's wire, wrapped part by part when it
	// was queued (InjectParts); drainNextLocked delivers it as is.
	preWire string
	// userID is set for the user's own message queued by SendOrQueue, empty
	// for a linked-session injection. A user entry delivers as an ordinary
	// user turn (no Attribution) and is the only kind the queue API exposes.
	userID   string
	queuedAt time.Time
}

// New builds a Runner for sessionID. data's actor must be sessionID (spec
// §15's per-service actor convention) so events and lease claims attribute
// correctly.
func New(sessionID string, data *dataclient.Client, providers *provider.Registry) *Runner {
	return &Runner{
		sessionID:     sessionID,
		data:          data,
		providers:     providers,
		Cwd:           ".",
		Died:          make(chan struct{}),
		hub:           newHub(),
		touched:       make(map[string]string),
		toolTouchedAt: make(map[string]time.Time),
		shutdownCh:    make(chan struct{}),
		permPending:   make(map[string]*pendingPermission),
		toolStatus:    toolstatus.New(),
		delegates:     delegates.New(sessionID, 0),
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
	// Delegate capability defaults come from the provider's matrix; then,
	// before the provider starts, the stored delegates — its first events may
	// resume one a previous process spawned, and must find that record.
	r.delegates.SetDefaults(delegates.Defaults(p.Matrix()))
	r.seedDelegates(ctx)

	// A directory the session asked for (Launchpad folder, workspace
	// primary repo, Config.DefaultDir — resolved by the Router at create)
	// replaces -cwd as the starting point for everything below, merge-back
	// included: it is the primary the worktree branches from.
	//
	// A folder that no longer exists is replaced first (rerootMissingDir),
	// so isolation below branches from the folder the session will really
	// run in.
	startDir, err := r.rerootMissingDir(ctx, &sess)
	if err != nil {
		return err
	}
	if startDir != "" {
		r.Cwd = startDir
	}
	cwd, err := r.isolate(ctx, &sess)
	if err != nil {
		return err
	}
	r.appendProvisioning(ctx, "worktree", nil)
	// Stamps this session into the worktree's own config for the hook — or,
	// working in place, into the user's checkout.
	r.installHook(ctx, cwd)
	r.appendProvisioning(ctx, "hook", nil)

	// Shadow store provisioning (docs/SHADOW-GIT-DIFFS-ADR.md): record the
	// store's base path on the session record now (persisted with the
	// provider ref below, so the Router never guesses), baseline the
	// primary root in the background once startup succeeds. Skipped for an
	// implicit cwd, same rationale as installHook's guard.
	if r.ShadowRoot != "" && cwd != "" && cwd != "." {
		r.shadowStore = shadow.New(r.ShadowRoot, r.sessionID)
		sess.ShadowDir = r.shadowStore.Dir()
	}

	agent, agentOK := r.loadAgent(ctx, sess)
	capEnv := r.setupCapabilities(ctx, cwd, agent, agentOK)
	orchestrateMCP, orchestrateErr := r.setupOrchestrate(ctx, sess)
	r.appendProvisioning(ctx, "mcp_up", nil)

	opts := provider.StartOptions{
		Mode:         provider.ModeACP,
		Model:        sess.Model,
		Cwd:          cwd,
		SystemPrompt: effectivePersonaPrompt(agent, agentOK), // "" when no persona — no mads preamble, ever
		Resume:       sess.ProviderRef,
		// ForkOnResume: ProviderRef is a native conversation mads must not
		// append to (an import), so this start forks it. The flag stays set
		// on the record until the fork's own ref is persisted.
		Fork: sess.ForkOnResume && sess.ProviderRef != "",
		Env:  capEnv,
		// The level the provider can actually run: auto_review on a
		// harness with no reviewer starts as limited. The record keeps
		// what the user asked for; the Router reports the difference
		// (Session.EffectiveTrust), so the fallback is visible, not silent.
		Trust: provider.EffectiveTrust(provider.Trust(sess.Trust), p.Matrix()),
	}
	// Persona-level tool-intent policy (agent-concept redesign §2). The
	// persona's Allowlist/Denylist fold through EffectiveToolIntents,
	// which also handles the pre-redesign `capabilities.tools` +
	// `denied_commands` shapes so legacy records keep their enforcement.
	// agentOK is false (no persona) means the zero-value ToolIntents —
	// "no stance" — and the adapter's trust layer is the whole story.
	//
	// HONESTY NOTE (the goose/kiro case): some adapters (goose, kiro)
	// declare every permission intent Unsupported in their Matrix — they
	// have no native tool-restriction surface of their own. For those,
	// this ToolIntents policy can't be enforced natively (the resolver
	// map still names the tools, so a future native mechanism can pick it
	// up), so the persona's *allowance* is additionally spelled into the
	// system prompt below: a prompt-level "you may not shell" is the only
	// channel the harness has, and it's honest about being a prompt
	// rather than a tool block. (claude-code's --disallowedTools is
	// native — see providers/claudecode/personaTools.go.)
	if agentOK {
		allowed, denied := agent.EffectiveToolIntents()
		opts.ToolIntents = provider.ToolIntents{
			Allowed: toProviderIntents(allowed),
			Denied:  toProviderIntents(denied),
		}
		opts.PersonaKey = "mads-persona:" + agent.ID
		// Persona path-scope allowlist (agent-concept redesign §2): the
		// portable form of the "path_scope" permission intent. A provider
		// with a native path-scope surface (claude-code's PreToolUse hook —
		// see providers/claudecode/permission.go) enforces it; a provider
		// without one (goose, kiro) declares IntentPathScope: Unsupported
		// and takes this as "no stance". Empty when the persona declared
		// no path scope — the adapter then wires no hook at all.
		if len(agent.Permissions.AllowedPaths) > 0 {
			opts.PersonaAllowedPaths = agent.Permissions.AllowedPaths
		}
	}
	if orchestrateMCP != nil {
		opts.MCPServers = []provider.MCPServerRef{*orchestrateMCP}
	}
	// The harness's own permission prompts come to the user through this
	// session (permissions.go). Adapters wire it only where nobody sits at
	// the harness's own UI — the chat transport — and only when trust lets
	// the harness ask at all; carried in baseOpts, a switch to the terminal
	// transport simply leaves it unused there.
	opts.Permissions = r.RequestPermission
	// The keyed contract (settle hints) for a provider whose keys are the
	// agent's own. Not for an emulated one (hermes): it would have to
	// change live content to meet it, which a legacy consumer would see.
	opts.Keyed = p.Matrix()[provider.BehaviorStableKeys] == provider.Native
	spawnedAt := time.Now()
	provSess, err := p.Start(ctx, opts)
	if err != nil {
		return fmt.Errorf("runner: start provider session: %w", err)
	}
	r.appendProvisioning(ctx, "agent_starting", nil)

	// Persist the resume handle before returning success, so a crash right
	// after this point still leaves a resumable ref recorded.
	sess.ProviderRef = provSess.Ref()
	if opts.Fork {
		// A forked session's Ref() is empty until the fork exists (see
		// StartOptions.Fork), and an empty one must not overwrite the
		// original: a crash before the new ref lands re-forks from it. A
		// ref that is already the fork's own is persisted here together
		// with the cleared flag, like in persistProviderRef.
		if ref := provSess.Ref(); ref == "" || ref == opts.Resume {
			sess.ProviderRef = opts.Resume
		} else {
			sess.ForkOnResume = false
		}
	}
	sess.Activity = contracts.SessionActive
	sess.Cwd = recordedCwd(cwd) // where the agent actually runs — the docked shell opens here
	r.recordMu.Lock()
	if err := r.data.UpdateSession(ctx, &sess); err != nil {
		r.recordMu.Unlock()
		return fmt.Errorf("runner: persist provider ref: %w", err)
	}
	r.persistedRef = sess.ProviderRef
	r.recordMu.Unlock()
	forkFrom := ""
	if opts.Fork && sess.ForkOnResume {
		forkFrom = opts.Resume
	}

	if orchestrateErr != nil {
		r.markOrchestrateLinks(ctx, contracts.OrchestrateFailed, orchestrateErr.Error())
	}
	if orchestrateMCP != nil {
		// A fresh spawn always launches with the current desired MCP set
		// already in opts.MCPServers (set above) — there is no "pending"
		// case at Start, only whether this provider actually wires launch-
		// time MCP config in at all (Matrix's BehaviorMCPServers).
		status := contracts.OrchestrateUnsupported
		if p.Matrix()[provider.BehaviorMCPServers].Works() {
			status = contracts.OrchestrateActive
		}
		r.markOrchestrateLinks(ctx, status, "")
	}

	r.appendProvisioning(ctx, "provider_ready", map[string]string{"provider": sess.Provider})
	if err := r.data.Append(ctx, &contracts.Envelope{
		Type:      contracts.SessionActivityChanged,
		SessionID: r.sessionID,
		Payload:   mustJSON(map[string]string{"activity": string(contracts.SessionActive)}),
	}); err != nil {
		log.Printf("runner: append session.activity_changed: %v", err)
	}

	r.synthEngine = synth.New(sess.SynthesisMode, r.sessionID, dataclient.New(r.data.BaseURL(), "synthesizer"), r.FastModel)

	// Decided before the session is installed so the pass is in flight
	// from the moment anything can reach the session (beginCompactLocked).
	resumeCompact := resumeCompactor(provSess, sess.LastUsage, sess.Model)

	r.mu.Lock()
	r.sess = provSess
	r.providerName = sess.Provider
	r.prov = p
	r.baseOpts = opts
	r.mode = opts.Mode
	r.lastRef = provSess.Ref()
	r.forkFrom = forkFrom
	if sess.RerootNotePending {
		r.rerootNote = rerootNote(sess.RerootedFrom, cwd)
	}
	r.spawnedAt = spawnedAt
	r.providerCwd = cwd
	if resumeCompact != nil {
		r.beginCompactLocked()
	}
	r.mu.Unlock()
	if resumeCompact != nil {
		r.markAgentWorking("/compact")
	}

	r.watchRefAnnounce(ctx, provSess)

	// Primary-root baseline, backgrounded: measured 6.3s on a 39 GB
	// checkout — it must never block provisioning. Other roots baseline on
	// first touch (shadow.Store.Capture). No-op when already baselined
	// (bounce/resume) or when shadowing is disabled (nil-safe). Also a no-op
	// when cwd isn't in a git repo: a session rooted at ~ or / must not copy
	// the folder into git objects, so those get per-capture pre-images only.
	go r.shadowStore.Baseline(cwd)

	r.startShellWatch()
	go r.pump(provSess)
	if resumeCompact != nil {
		r.requestResumeCompact(ctx, resumeCompact)
	}
	// Counted in the background: `git status` on a large checkout takes
	// seconds, and provisioning must not wait on the header.
	r.refreshCountsAsync()
	return nil
}

// recordedCwd is the working directory to put on the session record.
//
// cwd is "." for every session that did not choose one — the Router spawns the
// Session Service without -cwd, so the child inherits the Router's directory
// and the provider child inherits it in turn. "." is the honest value for the
// code paths below, which have to tell a *chosen* directory from an inherited
// one: installHook and the shadow store both refuse to act on "." because
// writing into whatever repo this process happens to have been started from is
// not the same act as writing into a directory someone asked for.
//
// Reporting it is a different question, and "." is no answer to it: the rail's
// hover read "Directory: ." on every session in the list, and the UI had to
// suppress the field to avoid printing junk. The resolution is not an
// inference — os.Getwd() is literally the directory the provider child runs in.
// A failure leaves the record saying what it said before rather than guessing.
func recordedCwd(cwd string) string {
	if cwd != "" && cwd != "." && cwd != "./" {
		return cwd
	}
	wd, err := os.Getwd()
	if err != nil {
		return cwd
	}
	return wd
}

// installHook installs the mads post-commit hook (spec §5) in the session's
// working tree and points it at the Data Service, so interactive commits
// report with session/task linkage the same way pipeline task commits do.
// Not in another session's worktree (a linked session joined it): that
// folder's stamp stays its owner's.
//
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
	// A session linked at start can join another session's folder
	// (Router's validateStagedLinks). That folder's config names its owner,
	// whose own commits the hook would otherwise start crediting to this
	// session; the owner installed the hook there already.
	if branch, err := sessionwt.CurrentBranch(ctx, dir); err == nil &&
		strings.HasPrefix(branch, sessionwt.BranchPrefix) && branch != sessionwt.Branch(r.sessionID) {
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

// worktreeState is where the header counts are taken (D4): the session's
// worktree and branch when isolated, or the folder it works in (branch
// empty) when not.
type worktreeState struct {
	dir    string
	branch string
	base   string
}

// implicitDir reports a cwd nobody chose: "." is wherever the Router was
// started. installHook and the shadow store refuse to act on it, and so does
// isolation — branching a worktree off whatever repo the Router happens to
// run in is not something the user asked for.
func implicitDir(dir string) bool {
	return dir == "" || dir == "." || dir == "./"
}

// isolate decides where the agent runs (docs/SESSION-WORKTREES-DESIGN.md
// D1) and returns that directory. A session is isolated when it didn't ask
// for the current directory, its folder is a git repo, and either it already
// has a worktree (resume) or it has never started (no ProviderRef). A
// session that has run in place before stays in place: claude-code finds a
// conversation to resume by its starting folder, so moving it would strand
// the conversation.
//
// The worktree comes from sessionwt.EnsureWithLock, which works from the
// deterministic path and branch and repairs a lost or blocked folder (D2)
// under this session's own lock — not the repo lock, so a pipeline land
// running its gate in the same repo never holds up a start. Its fields are
// persisted here, before the provider starts, so a provider
// that fails to start leaves a record the next start reuses instead of
// tripping over its own branch.
func (r *Runner) isolate(ctx context.Context, sess *contracts.Session) (string, error) {
	inPlace := func() (string, error) {
		if !implicitDir(r.Cwd) && sessionwt.IsGitRepo(ctx, r.Cwd) {
			r.mu.Lock()
			r.wt = worktreeState{dir: r.Cwd}
			r.mu.Unlock()
		}
		return r.Cwd, nil
	}
	if r.WorktreeRoot == "" || sess.Isolation == contracts.IsolationCurrentDir || implicitDir(r.Cwd) {
		return inPlace()
	}
	if sess.WorktreeDir == "" && sess.ProviderRef != "" {
		return inPlace() // ran in place before isolation existed: stays there
	}
	if !sessionwt.IsGitRepo(ctx, r.Cwd) {
		return inPlace()
	}

	spec := sessionwt.Spec{Primary: r.Cwd, Root: r.WorktreeRoot, SessionID: r.sessionID, Base: sess.WorktreeBase}
	if sess.WorktreeDir == "" {
		base, err := sessionwt.CurrentBranch(ctx, r.Cwd)
		if errors.Is(err, sessionwt.ErrDetachedHEAD) {
			// No branch to land into, so nothing to isolate against.
			r.appendWorktreeEvent(ctx, map[string]any{"action": "not_isolated", "reason": "detached_head"})
			return inPlace()
		}
		if err != nil {
			return "", fmt.Errorf("runner: read primary branch: %w", err)
		}
		spec.Base = base
	}
	res, err := sessionwt.EnsureWithLock(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("runner: ensure worktree: %w", err)
	}
	if sess.WorktreeDir != res.Dir || sess.WorktreeBranch != res.Branch || sess.WorktreeBase != spec.Base {
		r.recordMu.Lock()
		err := r.data.SetSessionWorktree(ctx, r.sessionID, contracts.SessionWorktreePatch{
			WorktreeDir: &res.Dir, WorktreeBranch: &res.Branch, WorktreeBase: &spec.Base,
		})
		r.recordMu.Unlock()
		if err != nil {
			return "", fmt.Errorf("runner: persist worktree: %w", err)
		}
		sess.WorktreeDir, sess.WorktreeBranch, sess.WorktreeBase = res.Dir, res.Branch, spec.Base
	}
	if res.Notable() {
		r.appendWorktreeEvent(ctx, res.Payload(spec.Base))
	}
	r.mu.Lock()
	r.wt = worktreeState{dir: res.Dir, branch: res.Branch, base: spec.Base}
	r.mu.Unlock()

	// A session asked to start in a subdirectory of the repo starts in the
	// same subdirectory of its worktree.
	cwd := res.Dir
	if prefix, err := sessionwt.Prefix(ctx, r.Cwd); err == nil && prefix != "" {
		if fi, err := os.Stat(filepath.Join(res.Dir, prefix)); err == nil && fi.IsDir() {
			cwd = filepath.Join(res.Dir, prefix)
		}
	}
	return cwd, nil
}

// appendProvisioning records that one Start step finished, for the
// Launchpad's provisioning strip (ui/src/conversation/provisioningSteps.ts).
// Steps arrive in Start's order — worktree, hook, mcp_up, agent_starting,
// provider_ready — and a step that had nothing to do (working in place, no
// capabilities) is still reported, so the strip never stalls on it. Best
// effort, like every other Start event.
func (r *Runner) appendProvisioning(ctx context.Context, step string, extra map[string]string) {
	payload := map[string]string{"step": step}
	for k, v := range extra {
		payload[k] = v
	}
	if err := r.data.Append(ctx, &contracts.Envelope{
		Type: contracts.SessionProvisioning, SessionID: r.sessionID, Payload: mustJSON(payload),
	}); err != nil {
		log.Printf("runner: append session.provisioning: %v", err)
	}
}

// appendWorktreeEvent records what isolation did, so it shows as a card
// rather than healing silently (D2). Best effort, like the other session
// events Start appends.
func (r *Runner) appendWorktreeEvent(ctx context.Context, payload map[string]any) {
	if err := r.data.Append(ctx, &contracts.Envelope{
		Type: contracts.WorktreeEnsured, SessionID: r.sessionID, Payload: mustJSON(payload),
	}); err != nil {
		log.Printf("runner: append worktree.ensured: %v", err)
	}
}

// RefreshWorktreeCounts recomputes the header counts (D4) — session files
// git still reports changed, and commits on the session branch its base
// doesn't have — and writes them onto the record through the scoped
// worktree write, so nothing else on the record can be clobbered. Runs at
// Start and turn end (refreshCountsAsync); Commit, Land and pipeline merges
// call it directly once they exist. A no-op for a session with nowhere to
// count.
func (r *Runner) RefreshWorktreeCounts(ctx context.Context) error {
	r.mu.Lock()
	wt := r.wt
	r.mu.Unlock()
	if wt.dir == "" {
		return nil
	}
	top, err := sessionwt.Toplevel(ctx, wt.dir)
	if err != nil {
		return err
	}
	rows, err := r.data.FileChanges(ctx, r.sessionID)
	if err != nil {
		return fmt.Errorf("load session files: %w", err)
	}
	c, err := sessionwt.Count(ctx, sessionwt.CountTarget{
		Dir: top, Files: sessionwt.SessionFiles(top, rows), Branch: wt.branch, Base: wt.base,
	})
	if err != nil {
		return err
	}
	r.recordMu.Lock()
	defer r.recordMu.Unlock()
	return r.data.SetSessionWorktree(ctx, r.sessionID, contracts.SessionWorktreePatch{
		UncommittedCount: &c.Uncommitted, UnlandedCount: &c.Unlanded, OutsideCount: &c.Outside,
	})
}

// refreshCountsAsync runs RefreshWorktreeCounts off the caller's goroutine
// — the pump must not stall a turn's events behind a `git status`. Calls
// that land while one is running coalesce into a single rerun, which still
// sees everything they would have.
func (r *Runner) refreshCountsAsync() {
	r.countsMu.Lock()
	if r.countsClosed {
		r.countsMu.Unlock()
		return
	}
	if r.countsRunning {
		r.countsAgain = true
		r.countsMu.Unlock()
		return
	}
	r.countsRunning = true
	r.countsWG.Add(1)
	r.countsMu.Unlock()
	go func() {
		defer r.countsWG.Done()
		// Shutdown cancels a recount mid-flight rather than waiting out a
		// slow `git status`.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-r.shutdownCh:
				cancel()
			case <-ctx.Done():
			}
		}()
		for {
			if err := r.RefreshWorktreeCounts(ctx); err != nil && ctx.Err() == nil {
				log.Printf("runner: refresh worktree counts: %v", err)
			}
			r.countsMu.Lock()
			if !r.countsAgain || ctx.Err() != nil {
				r.countsRunning = false
				r.countsMu.Unlock()
				return
			}
			r.countsAgain = false
			r.countsMu.Unlock()
		}
	}()
}

// deliverLocked marks a turn as in-flight. Must be called with r.mu held;
// the caller sends on r.sess itself, after unlocking.
func (r *Runner) deliverLocked() {
	r.busy = true
	r.sentAny = true
}

// Send relays one user turn. Returns ErrTurnInFlight (surfaced as HTTP 409)
// if a turn is already in flight.
func (r *Runner) Send(ctx context.Context, text string) error {
	r.mu.Lock()
	if r.sess == nil {
		r.mu.Unlock()
		return ErrNotReady
	}
	if r.busy || r.landing {
		r.mu.Unlock()
		return ErrTurnInFlight
	}
	var armed bool
	var prov provider.Provider
	var mode provider.Mode
	var cwd string
	var spawnedAt time.Time
	// A fork's ref is deterministic — the provider confirms its own new id
	// (RefAnnouncer) — so the locator, which correlates on the first user
	// message and would see the original conversation's, stays off.
	if !r.locateArmed && r.lastRef == "" && !r.sentAny && r.forkFrom == "" {
		r.locateArmed = true
		armed = true
		prov = r.prov
		mode = r.baseOpts.Mode
		cwd = r.providerCwd
		spawnedAt = r.spawnedAt
	}
	r.deliverLocked()
	sess := r.sess
	// The user's own words go to the synthesis engine and digest; the
	// provider (and the locator, which matches the transcript's first
	// message) gets them with any owed re-root note in front.
	wire, note := r.claimRerootNoteLocked(text)
	r.mu.Unlock()

	if armed {
		go r.locateProviderRef(prov, mode, cwd, spawnedAt, wire)
	}

	r.synthEngine.OnUserSend(text)
	r.digest.recordUser(text, false)

	r.markAgentWorking(text)
	if err := sess.Send(ctx, wire); err != nil {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
		r.restoreRerootNote(note)
		r.markAgentIdle()
		if errors.Is(err, provider.ErrTurnInFlight) {
			// The harness has a turn this runner didn't know about: the
			// same 409 as one it did, and SendOrQueue's cue to queue.
			return fmt.Errorf("%w: %w", ErrTurnInFlight, err)
		}
		return err
	}
	if note != "" {
		r.noteDelivered(ctx)
	}
	return nil
}

// Inject delivers a message from a linked session into this session's
// conversation (docs/LINKED-SESSIONS-DESIGN.md decisions 1-2). It never
// interrupts an in-flight turn: if the runner is busy, the message queues
// and onTurnEnd delivers it once the current turn ends, FIFO with any other
// queued injections. The provider gets text inside the attribution's
// wrapper (pkg/linkwrap) — who sent it, in the conversation itself, where
// both the agent and every transcript reader see it. Trust capping (decision 3) needs no extra plumbing
// here — sess.Send is the same call Send makes, so an injected turn is
// bound by the subject session's own trust level exactly as a user turn is.
func (r *Runner) Inject(ctx context.Context, text string, attribution contracts.Attribution) error {
	return r.InjectParts(ctx, []contracts.InjectPart{{Text: text, Attribution: attribution}})
}

// InjectParts delivers several labelled messages as one turn, each wrapped
// on its own (pkg/linkwrap) so every part keeps saying who sent it — a
// wake's flush of updates deferred from several links (2026-10-20). One part
// is exactly Inject. The turn counts as digest-only (decision 4's
// self-amplification guard) only if every part is digest-attributed.
func (r *Runner) InjectParts(ctx context.Context, parts []contracts.InjectPart) error {
	if len(parts) == 0 {
		return nil
	}
	attribution := parts[0].Attribution
	texts := make([]string, len(parts))
	wires := make([]string, len(parts))
	for i, p := range parts {
		texts[i] = p.Text
		wires[i] = linkwrap.Wrap(p.Text, p.Attribution)
		attribution.DigestAttributed = attribution.DigestAttributed && p.Attribution.DigestAttributed
	}
	text := strings.Join(texts, "\n\n")
	audit := func() {
		for _, p := range parts {
			r.recordInjectionAudit(ctx, p.Attribution, p.Text)
		}
	}
	r.mu.Lock()
	if r.sess == nil {
		r.mu.Unlock()
		return ErrNotReady
	}
	if r.busy || r.landing {
		entry := pendingInjection{text: text, attribution: attribution}
		if len(parts) > 1 {
			// Already a combined turn: delivered as built, never coalesced
			// further (drainNextLocked).
			entry.preWire = strings.Join(wires, "\n\n")
		}
		r.pending = append(r.pending, entry)
		r.mu.Unlock()
		audit()
		return nil
	}
	r.deliverLocked()
	sess := r.sess
	// The label goes on the wire, in the message (pkg/linkwrap); any owed
	// re-root note stays outside, ahead of it.
	wire, note := r.claimRerootNoteLocked(strings.Join(wires, "\n\n"))
	r.mu.Unlock()

	r.synthEngine.OnUserSend(text)
	r.digest.recordUser(text, attribution.DigestAttributed)
	audit()

	r.markAgentWorking(text)
	if err := sess.Send(ctx, wire); err != nil {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
		r.restoreRerootNote(note)
		r.markAgentIdle()
		return err
	}
	if note != "" {
		r.noteDelivered(ctx)
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
	actor := "link-" + attribution.LinkID
	if attribution.LinkID == "" {
		actor = "router" // a notice from the Router itself, not over a link
	}
	client := dataclient.New(r.data.BaseURL(), actor)
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
	// Ahead of the lock, so this lands before the queued-injection drain
	// below reports the NEXT turn working: appended the other way round, a
	// session would read idle for the whole of that live turn.
	r.markAgentIdle()

	r.mu.Lock()
	r.busy = false
	if len(r.pending) > 0 && !r.landing {
		r.deliverNextLocked()
		return
	}
	refreshPending := r.orchestrateRefreshPending
	r.orchestrateRefreshPending = false
	pendingModel := r.pendingModel
	r.pendingModel = ""
	closing := r.closing
	r.mu.Unlock()

	// Both deferred actions cycle the provider process, so they run in one
	// goroutine sequentially rather than two racing ones: opMu would
	// serialize them anyway, but ordering them here means the refresh cycles
	// with the model the switch just installed instead of the stale one it
	// would have captured before the switch landed.
	//
	// This quiet boundary (turn over, nothing queued) is also where a
	// pending orchestrate grant activates even if no refresh was requested:
	// orchestrateGrantPending makes it level-triggered. One refresh mounts
	// the one orchestrate MCP server that serves every link, so several
	// pending links still cost a single provider cycle, resumed on the same
	// conversation.
	if !closing {
		go func() {
			ctx := context.Background()
			if pendingModel != "" {
				if _, err := r.SetModel(ctx, pendingModel); err != nil {
					log.Printf("runner: deferred model switch: %v", err)
				}
			}
			if refreshPending || r.orchestrateGrantPending(ctx) {
				if err := r.RefreshOrchestrate(ctx); err != nil {
					log.Printf("runner: deferred orchestrate refresh: %v", err)
				}
			}
		}()
	}
}

// deliverNextLocked starts the next queued message (or coalesced digest
// run, see drainNextLocked) as a turn. Called with r.mu held and
// r.pending non-empty, by onTurnEnd and endLanding; it unlocks r.mu.
func (r *Runner) deliverNextLocked() {
	next := r.drainNextLocked()
	var delivered *contracts.QueuedMessage
	var remaining []contracts.QueuedMessage
	r.deliverLocked()
	if next.userID != "" {
		delivered = &contracts.QueuedMessage{ID: next.userID, Text: next.text, QueuedAt: next.queuedAt}
		remaining = r.queuedLocked()
	}
	sess := r.sess
	wire, note := r.claimRerootNoteLocked(next.wire)
	r.mu.Unlock()
	if sess == nil {
		r.restoreRerootNote(note)
		return
	}

	r.synthEngine.OnUserSend(next.text)
	r.digest.recordUser(next.text, next.attribution.DigestAttributed)
	if delivered != nil {
		r.publishQueue(contracts.QueueChanged{Queued: remaining, Delivered: delivered})
	}

	r.markAgentWorking(next.text)
	if err := sess.Send(context.Background(), wire); err != nil {
		log.Printf("runner: deliver queued message: %v", err)
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
		r.restoreRerootNote(note)
		r.markAgentIdle()
		if delivered != nil {
			r.publishQueue(contracts.QueueChanged{Queued: r.QueuedMessages(), Failed: delivered, Error: err.Error()})
		}
	} else if note != "" {
		r.noteDelivered(context.Background())
	}
	// orchestrateRefreshPending and pendingModel, if set, are left
	// untouched here — both are only drained once the queue is truly
	// empty (onTurnEnd), so a refresh or model switch requested
	// mid-queue-drain retries at the next real turn boundary instead of
	// cycling the provider process out from under this queued
	// injection's own in-flight turn.
}

// drainNextLocked pops the next queued injection(s) to deliver as one
// forced turn. If the head of r.pending is a digest push
// (Attribution.DigestAttributed), every contiguous queued digest push from
// the same link is coalesced into a single combined injection — a burst of
// per_turn digests that piled up while busy becomes one forced turn, not
// one per queued digest. Any other queued injection (a real user/leader
// message, or a digest from a different link) stops the coalescing and is
// left at the front of the queue for its own turn, so non-digest ordering
// is untouched. Each coalesced digest keeps its own wrapper in the combined
// wire text, so the transcript still shows one labelled digest per push.
// Must be called with r.mu held; mutates r.pending in place.
func (r *Runner) drainNextLocked() pendingInjection {
	head := r.pending[0]
	if head.preWire != "" {
		r.pending = r.pending[1:]
		head.wire = head.preWire
		return head
	}
	if head.userID != "" {
		r.pending = r.pending[1:]
		head.wire = head.text
		return head
	}
	if !head.attribution.DigestAttributed {
		r.pending = r.pending[1:]
		head.wire = linkwrap.Wrap(head.text, head.attribution)
		return head
	}

	texts := []string{head.text}
	wires := []string{linkwrap.Wrap(head.text, head.attribution)}
	i := 1
	for i < len(r.pending) {
		item := r.pending[i]
		if item.userID != "" || item.preWire != "" || !item.attribution.DigestAttributed || item.attribution.LinkID != head.attribution.LinkID {
			break
		}
		texts = append(texts, item.text)
		wires = append(wires, linkwrap.Wrap(item.text, item.attribution))
		i++
	}
	r.pending = r.pending[i:]
	head.text = strings.Join(texts, "\n\n")
	head.wire = strings.Join(wires, "\n\n")
	return head
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
	mcp, model := r.currentSwapBasis()
	return r.swapTransport(ctx, mode, mcp, model, false)
}

// currentSwapBasis reads the MCP server set and model the currently-running
// provider process was last started (or cycled) with — the "leave this
// alone" arguments for a swapTransport call that only means to change one
// of the other dimensions. Read under a single lock hold so a concurrent
// SetModel can't tear the pair apart.
func (r *Runner) currentSwapBasis() ([]provider.MCPServerRef, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.baseOpts.MCPServers, r.baseOpts.Model
}

// SetModel changes the model this session's provider process runs. --model
// is a spawn-time flag (providers/claudecode/session.go), so applying it
// live means cycling the process via swapTransport — resumed by ref, so the
// conversation continues rather than restarting. Callers are expected to
// have already persisted the new model on the session record, which is what
// a later cold start reads; this is only about the process running now.
//
// It never interrupts an in-flight turn. While busy the request is parked in
// pendingModel and onTurnEnd applies it once the session goes idle, and
// queued=true tells the caller to say "applies when this turn finishes"
// instead of claiming the switch already happened.
func (r *Runner) SetModel(ctx context.Context, model string) (queued bool, err error) {
	r.mu.Lock()
	if r.prov == nil {
		r.mu.Unlock()
		return false, ErrNotReady
	}
	if r.baseOpts.Model == model {
		// Already live. Clear any queued change too: switching A→B and back
		// to A mid-turn is a no-op overall, and leaving pendingModel set
		// would otherwise fire a pointless cycle to B at turn end.
		r.pendingModel = ""
		r.mu.Unlock()
		return false, nil
	}
	if r.busy {
		r.pendingModel = model
		r.mu.Unlock()
		return true, nil
	}
	mode, mcp := r.mode, r.baseOpts.MCPServers
	r.mu.Unlock()

	// requireResumable: cycling without a ref would start an unrelated fresh
	// provider session and silently drop the conversation — far worse than
	// reporting that the model lands on the next start.
	if _, err := r.swapTransport(ctx, mode, mcp, model, true); err != nil {
		return false, err
	}
	return false, nil
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
		if a[i].Name != b[i].Name || a[i].Command != b[i].Command || a[i].AlwaysLoad != b[i].AlwaysLoad ||
			len(a[i].Env) != len(b[i].Env) {
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
// start a new one" cycle behind SwitchTransport (mode changes),
// RefreshOrchestrate (materializing/tearing down the orchestrate MCP server
// on a running session without a user-visible restart, capability.go) and
// SetModel (mid-conversation model switches) — same primitive, parameterized
// by mode, the MCP server set the new process should advertise, and the
// model it should run. Callers changing one of those pass the live values
// for the other two (currentSwapBasis).
//
// Refused with ErrSwitchBusy while a turn is in flight. When
// requireResumable is set (RefreshOrchestrate only — SwitchTransport passes
// false, preserving its pre-existing "start fresh, let locate-mode discover
// the ref" fallback for the mid-first-connection case, see locateArmed),
// also refused with ErrNoResumeRef when there is no resumable ref yet:
// cycling here would silently start an unrelated fresh provider session
// instead of resuming, discarding conversation history; RefreshOrchestrate
// treats that as "leave pending_restart, retry later" rather than a hard
// failure. Idempotent: mode, mcp and model all already matching what's live
// is a no-op that returns the current ref without touching the provider
// process.
func (r *Runner) swapTransport(ctx context.Context, mode provider.Mode, mcp []provider.MCPServerRef, model string, requireResumable bool) (string, error) {
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
	if r.sess != nil && r.sess.Mode() == mode && mcpServersEqual(r.baseOpts.MCPServers, mcp) && r.baseOpts.Model == model {
		ref := r.sess.Ref()
		r.mu.Unlock()
		return ref, nil
	}
	old := r.sess
	resumeRef := r.lastRef
	if old != nil {
		resumeRef = old.Ref()
	}
	// A fork whose own ref is still unknown re-forks from the untouched
	// original; once the ref is known, resuming it is a plain resume.
	fork := false
	if resumeRef == "" && r.forkFrom != "" {
		resumeRef, fork = r.forkFrom, true
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
	// The old process's delegates died with it. Concluded here rather than
	// by the old pump's wind-down, which can run after the new process has
	// started delegates of its own (see pump). Its background processes
	// went the same way.
	r.endDelegatesForProcessExit()
	r.clearBackground()

	opts.Mode = mode
	opts.Resume = resumeRef
	opts.Fork = fork
	opts.MCPServers = mcp
	opts.Model = model

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
	r.baseOpts.Model = model
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
	if ref == r.forkFrom {
		// Never the fork's own ref: the original must not be adopted as
		// (or written back into the live session as) the new session's.
		r.mu.Unlock()
		return
	}
	forkFrom := r.forkFrom
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
	if forkFrom != "" {
		// The fork's own ref replaces the original in this one write, so
		// there is no state in which the flag is cleared while the record
		// still points at the original, or vice versa.
		cur.ForkOnResume = false
	}
	if err := r.data.UpdateSession(ctx, &cur); err != nil {
		log.Printf("runner: persist provider ref: %v", err)
		return
	}
	r.persistedRef = ref
	if forkFrom != "" {
		r.mu.Lock()
		if r.forkFrom == forkFrom {
			r.forkFrom = ""
		}
		r.mu.Unlock()
	}
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

// persistTurnEnd stamps the session record at the end of one turn:
// LastTurnAt always, and LastUsage whenever the provider counted any tokens —
// the context meter's data source at wake time (ui/src/conversation/Header/
// ContextMeter.tsx, before this process has seen a turn_end of its own) and
// the resume-compaction trigger's input (compact.go's shouldCompactOnResume).
// Called on every EventTurnEnd, unlike persistProviderRef's
// dedup-on-unchanged-value: both fields legitimately change turn to turn, so
// there is nothing to skip. LastTurnAt is stamped even on a turn that carried
// no usage, because it means "the last turn ended here" — a provider that
// reports no token accounting still ends turns.
//
// One scoped write, not the load-modify-write round trip this replaced: that
// pair carried a whole snapshot of the record across two calls and could drop
// whatever landed in the gap (this runner's own agent state, several
// transitions a turn). recordMu is still held, since the ref round trips it
// guards are still round trips.
//
// Best-effort: a failure here only means the meter, the trigger and the
// rail's last-reply stamp are stale until the next turn, never surfaced to
// the caller.
func (r *Runner) persistTurnEnd(ctx context.Context, usage *provider.Usage) {
	var last *contracts.Usage
	// A usage that counts no tokens (claude-code's /compact turn, when the
	// CLI didn't report what the compaction left) says nothing about the
	// context, and saving it would read as an empty one.
	if usage != nil && usage.ContextTokens+usage.InputTokens+usage.OutputTokens+
		usage.CacheCreationInputTokens+usage.CacheReadInputTokens > 0 {
		last = &contracts.Usage{
			InputTokens:              usage.InputTokens,
			OutputTokens:             usage.OutputTokens,
			CacheCreationInputTokens: usage.CacheCreationInputTokens,
			CacheReadInputTokens:     usage.CacheReadInputTokens,
			Model:                    usage.Model,
			ContextTokens:            usage.ContextTokens,
			ContextWindow:            usage.ContextWindow,
			CostUSD:                  usage.CostUSD,
		}
	}
	r.recordMu.Lock()
	defer r.recordMu.Unlock()
	if err := r.data.RecordSessionTurnEnd(ctx, r.sessionID, last, time.Now().UTC()); err != nil {
		log.Printf("runner: persist turn end: %v", err)
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
	landDone := r.landDone
	r.mu.Unlock()

	// A Land in flight finishes first, bounded by ctx (the shutdown grace,
	// inside the Router's own before it kills the process group): its
	// merge in the user's checkout is in a process group of its own, but
	// the abort after a refused merge is this process's to run.
	if landDone != nil {
		select {
		case <-landDone:
		case <-ctx.Done():
			log.Printf("runner: shutdown: a Land is still running: %v", ctx.Err())
		}
	}

	var closeErr error
	if sess != nil {
		closeErr = sess.Close()
	}
	// A last look for shell calls still open, recorded before the recount
	// below closes (bounded by ctx, like the Land wait).
	if r.shell != nil {
		r.shell.ProcessEnded(time.Now())
		r.shell.Stop(ctx)
	}
	// shutdownCh (closed above) cancels a recount in flight; let it finish
	// unwinding before the record is marked inactive, and start no more.
	r.countsMu.Lock()
	r.countsClosed = true
	r.countsMu.Unlock()
	r.countsWG.Wait()

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

	// Nothing left to report for. Stops a retry the agent-state writer may
	// have armed (agentstate.go) from re-arming past the Runner's life — the
	// retry loop is deliberately unbounded, so shutdown has to be what ends it.
	r.agent.mu.Lock()
	r.agent.stopTimerLocked()
	r.agent.mu.Unlock()

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
	var join textJoiner
	for ev := range sess.Events() {
		if ev.Kind == provider.EventSettle {
			// For the v2 tail alone: a contentless hint the legacy
			// pipeline below never saw before the keyed contract, and
			// still doesn't.
			continue
		}
		// First, so every consumer below (conversation status, synth,
		// digest, the live stream) sees text as it always read.
		ev = join.apply(ev)
		r.notePeerAddress(sess)
		if ev.Kind == provider.EventInbound {
			ev = r.onInbound(ev)
		}
		r.observeShell(ev, time.Now())
		if ev.Kind == provider.EventTurnEnd {
			// Before onTurnEnd: it can start a queued next turn, whose
			// tools must not blur which turn deleted a touched file.
			r.sweepDeletedFiles()
			r.shadowStore.CommitTurnAsync()
			r.onTurnEnd()
			r.syncProviderRef(context.Background(), sess)
			r.persistTurnEnd(context.Background(), ev.Usage)
			r.refreshCountsAsync()
		}
		if ev.Kind == provider.EventToolUse && ev.Tool != nil && ev.Tool.Path != "" && ev.Tool.Mutates {
			// Pre-image capture first: on providers that announce tool_use
			// before executing (claudecode), this reads the file inside the
			// same pre-execution window recordFileChange's stat relies on.
			r.shadowCapture(ev.Tool)
			r.recordFileChange(ev.Tool.Path)
		}
		// After the shadow/file-change block (their pre-execution stat
		// window must stay tight), before synth/digest/broadcast (so every
		// consumer sees the status-stamped event) — see conversation.go.
		ev = r.observeConversation(ev)
		r.synthEngine.OnEvent(ev)
		r.digest.recordEvent(ev)
		if ev.Kind == provider.EventBackground {
			r.setAgentBackground(mainAgentBackground(ev.Background))
		}
		r.hub.broadcast(ev)
	}

	// The stream closed. Normally the turn-end sweep already ran and this
	// is a no-op; a mid-turn close (Shutdown, or the provider dying) is the
	// "session stops" case of the status policy — resolve every still-open
	// tool call to interrupted, and the agent state to idle, so the event
	// log never shows either running forever. The delegates that ran inside
	// the process end with it — unless this is an intentional transport
	// swap (r.sess no longer this session), which concludes them itself
	// before starting the new process.
	// Shell calls the process took with it get looked at as they stand.
	if r.shell != nil {
		r.shell.ProcessEnded(time.Now())
	}

	r.mu.Lock()
	current := r.sess == sess
	r.mu.Unlock()
	if current {
		r.endDelegatesForProcessExit()
		r.clearBackground()
	}
	r.flushTurnState()

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
			var join textJoiner
			for ev := range events {
				if ev.Kind == provider.EventSettle {
					continue // v2-only, and terminal mode has no v2 chat frames
				}
				ev = join.apply(ev)
				if ev.Kind == provider.EventTurnEnd {
					r.sweepDeletedFiles()
					r.shadowStore.CommitTurnAsync()
					r.onTurnEnd()
					r.syncProviderRef(context.Background(), sess)
					r.persistTurnEnd(context.Background(), ev.Usage)
					r.refreshCountsAsync()
				}
				if ev.Kind == provider.EventToolUse && ev.Tool != nil && ev.Tool.Path != "" && ev.Tool.Mutates {
					r.shadowCapture(ev.Tool)
					r.recordFileChange(ev.Tool.Path)
				}
				// Same status/persistence hook as pump — no broadcast
				// here, but the event log must not depend on transport.
				ev = r.observeConversation(ev)
				r.synthEngine.OnEvent(ev)
				r.digest.recordEvent(ev)
			}
			r.flushTurnState()
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

// shadowCapture records one mutating tool call's pre-image blob into the
// session's shadow store (docs/SHADOW-GIT-DIFFS-ADR.md), keyed by the
// provider-native tool_use ID. The read happens here, synchronously in the
// pump — on providers that announce tool_use pre-execution the file is
// still the pre-image, at single-file cost (~1–5 ms, same budget as
// recordFileChange's stat). Nil-safe end to end and best-effort inside;
// a tool_use without a wire ID has nothing to correlate a diff to, so it
// is skipped (the turn-end tree commit still catches its effect).
func (r *Runner) shadowCapture(tool *provider.ToolUse) {
	if r.shadowStore == nil || tool.ID == "" {
		return
	}
	r.mu.Lock()
	base := r.touchBaseLocked()
	r.mu.Unlock()
	r.shadowStore.Capture(tool.ID, touchAbs(base, tool.Path))
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
	r.toolTouchedAt[touchAbs(base, path)] = time.Now()
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
