package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrUnsupported is returned by any call exercising a behavior the provider's
// matrix declares unsupported. Callers gate features on it; conformance
// asserts it appears exactly where the matrix says it should.
var ErrUnsupported = errors.New("behavior unsupported by provider")

// ErrResumeUnsupported is what OneShot returns when OneShotRequest.Resume is
// set on a provider whose matrix declares BehaviorOneShotResume
// unsupported. It wraps ErrUnsupported, so errors.Is matches either. A
// provider never answers a resume request by silently starting fresh.
var ErrResumeUnsupported = fmt.Errorf("one-shot resume: %w", ErrUnsupported)

// ErrTurnInFlight is returned by a Session's Send (or a Compactor's Compact)
// when the harness already has a turn running and takes one at a time — ACP's
// one-prompt-per-session rule (providers/acprpc). The Runner normally never
// gets here, holding its own in-flight flag; this is what lets it tell "busy,
// queue it" from a real failure when the two disagree.
var ErrTurnInFlight = errors.New("provider: a turn is already in flight")

// ErrDiscoveryUnavailable marks a ModelDiscoverer failure that's an expected
// runtime condition — the provider's binary is missing or exec itself
// failed (the same class of problem pkg/preflight checks for ahead of time)
// — as opposed to a genuine internal error. Handlers that surface discovery
// failures to callers degrade to an empty list on this, and only this.
var ErrDiscoveryUnavailable = errors.New("model discovery unavailable")

// RateLimitError is the typed surfacing of a provider rate limit (spec §11.9:
// inform and wait — the platform never silently retry-loops).
type RateLimitError struct {
	RetryAfter time.Duration // zero when the provider didn't say
	Message    string
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited: %s (retry after %s)", e.Message, e.RetryAfter)
	}
	return "rate limited: " + e.Message
}

// Mode selects the transport for an interactive session (spec §11):
// ACP-style structured events, or a raw terminal.
type Mode string

const (
	ModeACP Mode = "acp"
	ModePTY Mode = "pty"
)

// Trust is the session's trust level (MADS-UX §3.2 Trust pill), mirrored
// here rather than imported from pkg/contracts — this package defines its
// own types throughout and has zero import references of pkg/contracts.
// Values match contracts.SessionTrust string-for-string by construction;
// callers cast directly (provider.Trust(sess.Trust)), no lookup table.
type Trust string

const (
	TrustFull Trust = "full"
	// TrustAutoReview: a reviewer inside the harness approves routine calls
	// and escalates only risky or uncertain ones, which reach the user the
	// way limited's asks do (StartOptions.Permissions). Only meaningful on a
	// provider declaring BehaviorTrustAutoReview; EffectiveTrust maps it to
	// TrustLimited everywhere else, and an adapter without a reviewer must
	// treat it as TrustLimited if it is handed one anyway — never as the
	// unknown-value fallthrough to full that some trust switches have.
	TrustAutoReview Trust = "auto_review"
	TrustLimited    Trust = "limited"
	TrustReadOnly   Trust = "read_only"
)

// EffectiveTrust is the level a session requesting t actually runs at on a
// provider declaring m: auto_review on a harness with no reviewer
// (BehaviorTrustAutoReview not working) falls back to limited — the
// nearest level that still asks the user about risky calls, and strictly
// safer than the request, never looser. Every other level, and auto_review
// where the reviewer exists, runs as requested. The Session runner starts
// the provider at this level and the Router reports it on the session
// record (Session.EffectiveTrust) whenever it differs from the request, so
// the downgrade is never silent.
func EffectiveTrust(t Trust, m Matrix) Trust {
	if t == TrustAutoReview && !m[BehaviorTrustAutoReview].Works() {
		return TrustLimited
	}
	return t
}

// FullTrustOnly reports whether a provider declaring m can run sessions at
// full trust only: every restricted level (limited, read_only, auto_review)
// declared not working. Such a provider refuses a session asking for any
// of them (hermes — it asks before only the calls its own reviewer flags,
// so approvals can't be enforced), which is why a session that never chose
// a level defaults to TrustFull on it rather than to the configured
// default. Derived from the matrix, never from a provider name.
func FullTrustOnly(m Matrix) bool {
	return !m[BehaviorTrustLimited].Works() && !m[BehaviorTrustReadOnly].Works() && !m[BehaviorTrustAutoReview].Works()
}

// StartOptions configures an interactive session. Resume carries the
// provider-native ref of a previous session (Session.Ref); empty starts
// fresh. Resuming is the same spawn path as starting — that is how every
// real harness works — so it lives here rather than on a separate method.
type StartOptions struct {
	Mode         Mode
	Model        string
	Cwd          string
	SystemPrompt string   // the agent definition's prompt (registry, spec §9)
	Resume       string   // provider session ref to reattach to
	Env          []string // extra environment, KEY=value
	// Fork, only meaningful with Resume, continues the conversation in a NEW
	// provider session seeded from Resume's history instead of appending to
	// Resume itself: the original stays byte-for-byte untouched (an imported
	// native conversation must never be written to). The new session's ref
	// is not Resume — Ref() must not return Resume for a forked session, and
	// stays "" until the fork actually exists (RefAnnouncer, or the turn-end
	// sync), so a caller never persists a handle that resolves to nothing. A
	// provider that cannot fork (BehaviorBranching unsupported) must fail
	// Start with ErrUnsupported rather than silently resume in place; Fork
	// without Resume is ignored, there being nothing to fork from.
	Fork bool
	// Trust gates which tools the session may use (spec §11, linked-sessions
	// design decision 3). Zero value "" behaves exactly like TrustFull in
	// every provider — existing call sites that don't set it never regress.
	Trust Trust
	// MCPServers lists MCP servers the agent's own process should be able to
	// call as tools (docs/LINKED-SESSIONS-DESIGN.md, the orchestrate
	// capability). claudecode threads this into a real `--mcp-config` launch
	// (both transports); kirocli declares BehaviorMCPServers unsupported
	// (providers/kirocli/README.md) — the descriptor is simply never
	// materialized for its sessions.
	MCPServers []MCPServerRef
	// Permissions, when set, is who answers the harness's own permission
	// prompts on a transport with no human at the harness's terminal (the
	// chat/ACP transport — never PTY, where the harness's native prompt is
	// the surface). mads asks the user only when the harness itself would
	// ask: an adapter routes the harness's host permission channel here
	// (claude-code's --permission-prompt-tool stdio can_use_tool requests;
	// ACP's session/request_permission) and never invents asks of its own.
	// Nil means nobody answers: an adapter must then let the harness refuse
	// whatever would have asked, never stall on it. See PermissionAsker.
	Permissions PermissionAsker
	// Keyed asks for the keyed conversation contract
	// (docs/CHAT-STREAM-DESIGN.md §1, §3): every live item's text, tool name
	// and tool result exactly as the agent's store will hold them, so the
	// live item and the stored row are interchangeable under one Key, plus
	// EventSettle marks where the store has likely caught up. An adapter that
	// must change live content to get there (hermes swaps a tool's title for
	// its stored name and its rendered result for the stored output) does so
	// only under Keyed. Off by default: a consumer that doesn't ask sees
	// exactly the stream it always has. Keys themselves (Event.Key) are
	// carried either way.
	Keyed bool
	// ToolIntents is the persona-level allow/deny policy expressed as a
	// closed-set intent vocabulary (agent-concept redesign §2). Allowed
	// is the persona's Allowlist (empty == allow-all, the harness's
	// default palette). Denied is the persona's Denylist; it always wins
	// over Allowed. A provider that implements ToolIntentResolver
	// enforces these at its native surface (claudecode adds them to
	// --disallowedTools; goose honors them in its system prompt); a
	// provider without a resolver takes this as "no stance" and the
	// session runs on whatever its trust level already enforces.
	//
	// This field is OPTIONAL and additive: zero value == no persona-level
	// policy, and any adapter that doesn't know this field must still
	// compile. The runner's EffectiveToolIntents (pkg/contracts)
	// computes both slices from a persona before setting them.
	ToolIntents ToolIntents
	// PersonaAllowedPaths, when non-empty, is the persona's path-scope
	// allowlist (pkg/contracts AgentPermissions.AllowedPaths, folded in by
	// the runner exactly as ToolIntents is). It is the portable form of the
	// "path_scope" permission intent: file reads and writes are confined to
	// the listed paths. Each entry is a directory root expressed relative
	// to Cwd (or absolute); a tool call touching a path outside every root
	// must be denied by a provider that enforces it.
	//
	// A provider with a native path-scope surface (claude-code's PreToolUse
	// hook) enforces it; a provider without one (goose, kiro) takes it as
	// "no stance" and declines honestly in its IntentMatrix (IntentPathScope
	// = Unsupported). Zero value == no persona-level path restriction — the
	// session runs on whatever its trust level already confines.
	PersonaAllowedPaths []string
	// PersonaKey names the idempotency scope for SystemPrompt when a
	// mid-session SetModel re-invokes Start (redesign §5): the adapter
	// is expected to overwrite a prior persona with the same key rather
	// than appending a duplicate. goose already does this (key
	// "mads-agent"); claudecode's --append-system-prompt is idempotent
	// by construction. Empty when no identity is required (e.g. a
	// provider whose own session state makes re-injection harmless).
	PersonaKey string
}

// ToolIntents is the runner-facing allow/deny pair for the persona-level
// policy (agent-concept redesign §2). Both slices are ToolIntent — the
// closed v1 vocabulary, provider-agnostic. Denied always wins over
// Allowed on any shared intent; EmptyAllowed means "allow the harness's
// default palette" (i.e. no persona-level allow restriction).
type ToolIntents struct {
	Allowed []ToolIntent
	Denied  []ToolIntent
}

// MCPServerRef describes one MCP server the provider's launch could point an
// agent at (e.g. by writing a .mcp.json or an equivalent --mcp-config
// entry). Command is a shell-splittable launch string, matching
// contracts.CapabilityConfig.Command's convention.
type MCPServerRef struct {
	Name    string
	Command string
	Env     []string
	// Type is the server's transport: "" or "stdio" (Command carries the
	// launch string), "http", or "sse" (URL carries the endpoint). The zero
	// value means stdio, so every existing caller is unchanged. Providers
	// that can only materialize a subset (e.g. an ACP agent that doesn't
	// advertise mcpCapabilities.http) drop what they can't send.
	Type string
	// URL is the endpoint for Type "http"/"sse". Unused for stdio.
	URL string
	// AlwaysLoad asks the harness to put this server's tools in the prompt
	// up front rather than behind its deferred tool search, for a server the
	// agent should see without having to go looking (the linked-session
	// tools, 2026-10-19). claude-code honors it (alwaysLoad, 2.1.285+);
	// other providers ignore it.
	AlwaysLoad bool
}

// OneShotRequest is a single non-interactive invocation — the Fast Model
// baseline (spec §11): prompt in, final text out, no session left behind.
type OneShotRequest struct {
	Prompt string
	System string
	Model  string
	Cwd    string
	// Resume, if set, is a provider-native session ref (a previous
	// OneShotResult.Ref or EventSessionRef, or an interactive Session.Ref)
	// whose conversation this invocation continues, in Cwd, with Prompt as
	// the next user message. Gated by BehaviorOneShotResume: a provider
	// declaring it unsupported returns ErrResumeUnsupported rather than
	// starting fresh, and an unknown ref is an error, never a fresh start.
	// Whether the resumed call keeps the same ref is per provider (all
	// shipped ones do); callers take the ref it reports, as for any call.
	// Model and System still apply where the harness allows.
	Resume string
	// OnEvent, if set, is called with normalized events (text deltas,
	// tool use) as they arrive during the invocation — best-effort. A
	// provider with no incremental output mechanism simply never calls
	// it; callers must not assume it fires at all. Final OneShotResult
	// semantics (Text, Usage, FailMarker detection on the full text) are
	// unchanged either way.
	OnEvent func(Event)
}

// OneShotResult is the completed invocation.
type OneShotResult struct {
	Text string `json:"text"`
	// Ref is the provider-native session ref for this invocation, when the
	// provider's wire format announces one (e.g. claude-code's session_id
	// on every stream line) — exact attribution for transcript lookup,
	// no path guessing. Empty when the provider has no such concept.
	Ref   string `json:"ref,omitempty"`
	Usage Usage  `json:"usage"`
}

// Model is one selectable model (Launchpad model popover, spec §11.5).
type Model struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default,omitempty"`
}

// Provider is the core interface every adapter implements. Behaviors that
// vary by harness (transcript access, model discovery) are optional strategy
// interfaces below — the matrix declares which are present, and conformance
// holds the two in agreement.
// AccountKeyer is implemented by a provider that can name the login its
// sessions run on (AccountStatus.Account) without starting one: the key a
// session launched with no extra environment would report.
type AccountKeyer interface {
	DefaultAccount() string
}

type Provider interface {
	// Name is the registry key ("claude-code", "fake", ...).
	Name() string
	// Matrix declares support for every known behavior. Must validate.
	Matrix() Matrix
	// Start launches (or with opts.Resume, reattaches to) an interactive
	// session in the requested mode. ErrUnsupported for an unsupported mode
	// or an unsupported resume.
	Start(ctx context.Context, opts StartOptions) (Session, error)
	// OneShot runs a single non-interactive invocation to completion.
	OneShot(ctx context.Context, req OneShotRequest) (*OneShotResult, error)
}

// Session is a live interactive agent conversation. One Send begins a turn;
// the response streams as Events and finishes with EventTurnEnd. Sessions
// survive Interrupt (the turn ends, the session lives). Close terminates the
// underlying process; Ref stays valid for StartOptions.Resume afterward.
type Session interface {
	// Ref is the provider-native handle used to resume this session later.
	Ref() string
	Mode() Mode
	Send(ctx context.Context, text string) error
	// Interrupt stops the in-flight response. The turn ends (EventTurnEnd
	// with Interrupted set where the transport can express it) and the
	// session remains usable.
	Interrupt(ctx context.Context) error
	// Events is the session's ordered event stream. It closes when the
	// session ends — after Close, or when the underlying process exits.
	// A close with no preceding EventTurnEnd means the process died
	// mid-turn (the Router's supervision signal). PTY sessions may return
	// nil; their output flows through Terminal instead.
	Events() <-chan Event
	Close() error
}

// PTYSession is the terminal transport: the same Session contract with the
// byte stream exposed. Send still injects user messages programmatically;
// Terminal carries everything the harness renders.
type PTYSession interface {
	Session
	Terminal() io.ReadWriter
	Resize(cols, rows int) error
}

// ModelDiscoverer is the optional strategy for BehaviorModelDiscovery.
type ModelDiscoverer interface {
	Models(ctx context.Context) ([]Model, error)
}

// TranscriptReader is the optional strategy for BehaviorTranscript: session
// history in normalized form, by provider-native ref.
type TranscriptReader interface {
	Transcript(ctx context.Context, ref string) ([]Message, error)
}

// TranscriptCursor is an adapter's opaque position in its agent's store,
// for IncrementalTranscriptReader. Nil is the start. It lives in memory
// with whoever holds it (the session child) and is never persisted or
// serialised, so an adapter may keep whatever state its reader needs in it.
type TranscriptCursor any

// IncrementalTranscriptReader is the optional strategy for reading a
// session's store as it grows (docs/CHAT-STREAM-DESIGN.md §3): the rows
// stored after cursor, in conversation order, each with the same Key a full
// Transcript gives it, and the cursor after them. Chained calls from nil
// add up to exactly Transcript — or KeyedTranscript, thinking rows
// included, for an adapter that implements KeyedTranscriptReader. ErrCursorStale means the store was
// rewritten under the cursor; the caller re-reads with Transcript.
type IncrementalTranscriptReader interface {
	TranscriptSince(ctx context.Context, ref string, cursor TranscriptCursor) ([]Message, TranscriptCursor, error)
}

// KeyedTranscriptReader is the keyed-consumer form of TranscriptReader
// (docs/CHAT-STREAM-DESIGN.md §4): Transcript's rows plus a keyed
// Role "thinking" row for every thinking item the agent stores (claude's
// summaries, codex's reasoning summaries, goose, hermes's reasoning
// columns), in conversation order, each with the Key its live
// EventThinking carried. Transcript itself never carries thinking, so the
// consumers that read it (digest, synth, retro, pipeline turns, export)
// are unchanged. An adapter that implements IncrementalTranscriptReader
// and this returns the keyed form from TranscriptSince too: chained reads
// add up to KeyedTranscript.
type KeyedTranscriptReader interface {
	KeyedTranscript(ctx context.Context, ref string) ([]Message, error)
}

// ErrCursorStale is IncrementalTranscriptReader's "start over" answer.
var ErrCursorStale = errors.New("provider: transcript cursor stale")

// KeyedSender is the optional strategy for a send whose user message the
// agent stores under a caller-supplied id (claude-code's stdin uuid,
// codex's clientUserMessageId): id is minted by mads (a UUID), and the
// stored user row's Key is "user:"+id. A mid-turn send keeps the same id.
// Callers without an id, or adapters without the strategy, use Send.
type KeyedSender interface {
	SendKeyed(ctx context.Context, text, id string) error
}

// DelegateInspector is the optional strategy for BehaviorDelegateTranscript:
// what the harness's own store says about one delegate (docs/
// DELEGATES-DESIGN.md) of the session whose provider-native ref is ref, found
// by the harness's id for it (Claude Code's agentId). The run-time half of a
// delegate's capabilities — the matrix gives the defaults, this the facts.
// ok is false when the store has nothing for it (yet: a delegate's files may
// appear after its first lifecycle line).
//
// DelegateTranscript reads that delegate's own transcript in the same
// normalized form as TranscriptReader, its prompt included as the first
// "user" message. ok is false, with no error, when there is none to read.
type DelegateInspector interface {
	InspectDelegate(ctx context.Context, ref, harnessID string) (info DelegateInfo, ok bool, err error)
	DelegateTranscript(ctx context.Context, ref, harnessID string) (msgs []Message, ok bool, err error)
	// DelegateHistory lists every delegate the harness's store holds for the
	// session ref, with what the parent's own history says about how each
	// ended — for a conversation mads never watched run (an import). None is
	// an empty list, not an error.
	DelegateHistory(ctx context.Context, ref string) ([]DelegateRecord, error)
}

// DelegateRecord is one delegate as a harness's history records it, start to
// end. Zero values mean the history didn't say.
type DelegateRecord struct {
	DelegateInfo
	// HarnessID is the harness's id for the delegate (Claude Code's agentId).
	HarnessID string
	// ToolUseID is the parent's call that spawned it.
	ToolUseID   string
	Description string
	// StartedAt and EndedAt are the harness's own stamps. EndedAt is set
	// whenever Status is.
	StartedAt, EndedAt time.Time
	// Status is the harness's word for how it ended ("completed", "failed",
	// "killed"); empty when the history holds no ending.
	Status string
	// Runs is how many times it ran: more than one when the parent messaged
	// it again after it ended.
	Runs int
	// DurationMS and ToolUses are totals across its runs; TotalTokens is
	// the last ending's. PriorDurationMS and PriorToolUses are the totals
	// of the runs before the last.
	DurationMS      int
	TotalTokens     int
	ToolUses        int
	PriorDurationMS int
	PriorToolUses   int
}

// DelegateInfo is one delegate as the harness's own store records it. Zero
// values mean the store didn't say.
type DelegateInfo struct {
	// TranscriptPath is the delegate's own durable transcript.
	TranscriptPath string
	// Type is the delegate's agent type ("general-purpose", "Explore").
	Type string
	// Depth is its nesting depth (1 = spawned by the main agent).
	Depth int
	// WorktreePath is the separate worktree it runs in, when it has one.
	WorktreePath string
	// Background reports the harness ran it in the background.
	Background bool
	// Model and Branch are what its latest recorded line says it ran on;
	// Usage that line's API call's accounting.
	Model  string
	Branch string
	Usage  *Usage
}

// HistoryScanner is the optional capability of listing the conversations a
// user already has in the provider's own native store, from before mads
// existed or outside it — what the onboarding import turns into session
// records that point at the native conversation (Session.ProviderRef) rather
// than copying it. Not part of the Behavior matrix: callers detect it with a
// type assertion, like Completer. A provider with no readable history store
// simply doesn't implement it.
type HistoryScanner interface {
	// ScanHistory reads the native store and returns every conversation worth
	// importing. Filtering what must never be imported (automation runs,
	// conversations a live process still owns, empty ones) is the provider's
	// call, since only it knows its store's markers; deduping against
	// sessions mads already has is the caller's. A store that doesn't exist
	// yields an empty scan, not an error.
	ScanHistory(ctx context.Context) (HistoryScan, error)
}

// HistoryScan is one provider's native conversation history.
type HistoryScan struct {
	// LookedFor lists the places read (directories), so a caller can say what
	// was looked at when the result is empty.
	LookedFor     []string
	Conversations []HistoryConversation
}

// HistoryConversation is one importable native conversation.
type HistoryConversation struct {
	// Ref is the provider-native handle: Session.ProviderRef, and what
	// TranscriptReader.Transcript reads by.
	Ref string
	// Dir is the folder the conversation ran in, as the store itself recorded
	// it. It may no longer exist.
	Dir string
	// Title is the provider's own name for the conversation, falling back to
	// the first thing the user said. Never empty.
	Title string
	// CreatedAt and LastActiveAt are the first and last native timestamps.
	CreatedAt    time.Time
	LastActiveAt time.Time
	// Usage is the last turn's token accounting, nil when the store has none.
	Usage *Usage
	// Trust is the level the conversation last ran at, mapped from the
	// harness's own permission mode as the store recorded it (claude-code:
	// the most recent permissionMode in the transcript). Empty when the
	// store records none, or a mode with no honest mads equivalent — the
	// importer then applies its default.
	Trust Trust
}

// RefAnnouncer is the optional strategy for BehaviorRefAnnounce: a session
// whose Ref() is already non-empty at Start-return needs no subscription —
// callers must check Ref() first. Otherwise, if the session implements this
// interface, RefAnnounced returns a channel that delivers exactly one
// non-empty ref and closes, or closes with no value sent if no announcement
// will ever come (e.g. a transport that never announces, or PTY discovery
// giving up). A session that will never announce and does not implement
// this interface at all is the same "never" case — callers must not block
// waiting for an interface assertion that isn't there.
type RefAnnouncer interface {
	RefAnnounced() <-chan string
}

// SessionLocator is the optional strategy backing BehaviorRefAnnounce when a
// session has no wire-level announcement (PTY) or the announcement never
// arrives (a dropped/degraded structured session). It identifies the
// session started in cwd no earlier than spawnedAfter whose first user turn
// was firstMessage — content correlation against the provider's own local
// session index/transcripts, not a heuristic. Implementations must verify
// any candidate before returning it and decline on ambiguity: ok=false on 0
// or 2+ matching candidates, timeout, or cancellation. A wrong ref would
// silently resume someone else's conversation, so guessing is never
// acceptable — callers may call LocateSession repeatedly (best-effort
// forever) until ctx is done.
type SessionLocator interface {
	LocateSession(ctx context.Context, cwd string, spawnedAfter time.Time, firstMessage string) (ref string, ok bool)
}

// RefSetter is the write side of SessionLocator: implemented by a Session
// whose Ref() starts empty and has no self-announcement mechanism (PTY), or
// whose announcement never arrived (a degraded ACP session). Once the
// Runner's SessionLocator backstop resolves a ref, it calls SetRef so the
// live session object itself starts reporting it — every caller that reads
// Ref() directly off the live session (Transcript, SwitchTransport's resume
// handle) sees the discovered ref too, not just the Runner's own persisted
// copy. A session that always knows its own ref has no need to implement
// this.
type RefSetter interface {
	SetRef(ref string)
}

// PeerAddresser is implemented by a live session whose harness has an
// inbound channel other processes can message it on (claude-code's
// cross-session peer socket). PeerAddress is this session's own address on
// that channel, in the same opaque format its Inbound.From values use, so
// the layer above can match a sender to a session by plain string equality
// — never by parsing it. Empty until the harness has announced one. A
// harness with no such channel doesn't implement this.
type PeerAddresser interface {
	PeerAddress() string
}

// MCPHotSetter is implemented by a live session that can change its MCP
// server set on the running process, without a restart and without losing
// the conversation: new tools are visible from the next turn. The Runner
// tries it before falling back to cycling the process
// (RefreshOrchestrate). SetMCPServers takes the full desired set, like
// StartOptions.MCPServers. A session that can only apply some changes (it
// can add servers but not remove ones it was launched with) returns
// ErrMCPHotSetUnsupported for the rest, so the caller cycles instead.
type MCPHotSetter interface {
	SetMCPServers(ctx context.Context, servers []MCPServerRef) error
}

// ErrMCPHotSetUnsupported means this particular change can't be applied
// live; restart the process with the new set instead.
var ErrMCPHotSetUnsupported = errors.New("provider: MCP server change needs a restart")

// CompleteRequest is a single hermetic, stateless completion — the Fast
// Model's default-provider transport. No session, no tools, no conversation
// history.
type CompleteRequest struct {
	System string
	Prompt string
	// MaxOutputTokens bounds the reply length. Zero means the provider's own
	// default; a provider without a native enforcement mechanism may treat
	// this as best-effort/unenforced.
	MaxOutputTokens int
	// JSONSchemaHint is descriptive text (a JSON Schema or plain description)
	// guiding the reply's shape. Providers must never validate against it —
	// JSON validation and the single bounded repair-retry live in the
	// fastmodel layer only, never per-provider.
	JSONSchemaHint string
	Model          string
}

// CompleteResult is one hermetic completion's raw output.
type CompleteResult struct {
	Text  string
	Usage Usage
}

// Completer is the optional hermetic one-shot completion capability backing
// Fast Model's "use my provider" resolution: stateless, no session left
// behind, no tools, no conversation. A provider MAY implement it in addition
// to the required OneShot (which stays session/tool-shaped and is not
// hermetic by contract). Not part of the Behavior matrix — like RefSetter
// and SessionLocator, callers detect it with a type assertion.
type Completer interface {
	Complete(ctx context.Context, req CompleteRequest) (CompleteResult, error)
}

// Compactor is the optional capability for a session that can be asked to
// run its harness's native context-compaction pass proactively, out of band
// from a normal turn — the Runner's resume-above-threshold trigger
// (services/session/internal/runner/compact.go), so resuming a big
// transcript in headless mode doesn't silently burn usage limits the way an
// uncompacted resume would (interactive claude-code offers a "resume from
// summary" prompt headless -p mode has no equivalent of) — and the user's
// own Compact action. The provider-level declaration is the matrix's
// BehaviorCompaction (and BehaviorCompactionFocus for instructions), which
// the conformance suite holds to this interface on the structured session;
// callers still detect it per session with a type assertion, since a
// provider's terminal transport never has it. A session without it simply
// doesn't compact: the resume trigger no-ops (no error, no status shown,
// resume proceeds exactly as before this capability existed), and the UI
// hides Compact for the provider.
type Compactor interface {
	// Compact asks the session to run its native compaction pass, steering
	// the summary by instructions when non-empty ("" = the harness's
	// default pass — what auto-compaction always sends). Instructions are
	// only meaningful where the matrix declares BehaviorCompactionFocus; an
	// implementation that can't take them returns ErrUnsupported for a
	// non-empty value rather than silently dropping them. Progress and
	// completion are reported on the session's normal Events() channel
	// (EventCompact) — this call's error is only about failing to even
	// submit the request (e.g. a closed stdin), not the compaction outcome.
	Compact(ctx context.Context, instructions string) error
}
