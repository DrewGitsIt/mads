// Package provider defines the abstraction every agent harness plugs in
// through (spec §11): a small core interface, optional strategy interfaces
// for behaviors that vary by harness, and a capability matrix in which each
// provider declares — per behavior — whether its support is native, emulated,
// or absent. The matrix is not documentation: the conformance suite
// (providers/conformance) executes it against the adapter and fails on any
// mismatch between declaration and behavior.
package provider

import (
	"fmt"
	"sort"
	"strings"
)

// Behavior is one row of the §11 capability matrix.
type Behavior string

const (
	// BehaviorInteractive — start an agent session, send messages, receive a
	// streamed response. Required.
	BehaviorInteractive Behavior = "interactive"
	// BehaviorInterrupt — stop the current response without killing the
	// session. Required.
	BehaviorInterrupt Behavior = "interrupt"
	// BehaviorOneShot — non-interactive single invocation (the Fast Model
	// baseline, spec §11). Required.
	BehaviorOneShot Behavior = "one_shot"
	// BehaviorResume — reattach to a previous session by its provider-native
	// ref with conversation context intact. Expected.
	BehaviorResume Behavior = "resume"
	// BehaviorTranscript — read back a session's history in the normalized
	// Message form (what synthesis and retro consume, spec §4, §7). Expected.
	BehaviorTranscript Behavior = "transcript"
	// BehaviorModelDiscovery — enumerate selectable models. Expected.
	BehaviorModelDiscovery Behavior = "model_discovery"
	// BehaviorCostReporting — token usage on turn end / one-shot results
	// (feeds the event log, the metrics store, spec §8). Expected.
	BehaviorCostReporting Behavior = "cost_reporting"
	// BehaviorACP — the structured event protocol transport. At least one of
	// acp/pty must be supported.
	BehaviorACP Behavior = "acp"
	// BehaviorPTY — the terminal transport. At least one of acp/pty must be
	// supported; PTY-only providers render in terminal mode only (§11).
	BehaviorPTY Behavior = "pty"
	// BehaviorBranching — fork a session into alternatives: StartOptions.Fork
	// with Resume starts a new, independent session seeded from the resumed
	// one's history, leaving the original untouched. Forking at an earlier
	// turn (rewind) is a distinct, not-yet-modeled surface — Native here
	// means fork-on-resume only. Optional.
	BehaviorBranching Behavior = "branching"
	// BehaviorCwdReroot — move a live session to a different working
	// directory. Optional.
	BehaviorCwdReroot Behavior = "cwd_reroot"
	// BehaviorMCPServers — thread StartOptions.MCPServers into the provider's
	// real launch, so the agent process can actually call the declared
	// servers as tools (the L2c orchestrate capability,
	// docs/LINKED-SESSIONS-DESIGN.md item 5, is the first and only producer
	// today). Optional: a provider with no wiring mechanism simply never
	// materializes the declared server for its sessions — no error, no
	// partial/degraded state, the capability just silently doesn't reach the
	// agent.
	BehaviorMCPServers Behavior = "mcp_servers"
	// BehaviorTrustLimited — a "limited" trust session denies command
	// execution (mapping documented per-provider). Expected: every provider
	// must take an honest stance, even if that stance is Unsupported.
	BehaviorTrustLimited Behavior = "trust_limited"
	// BehaviorTrustReadOnly — a "read_only" trust session denies both file
	// writes and command execution. Expected, same honesty requirement as
	// BehaviorTrustLimited.
	BehaviorTrustReadOnly Behavior = "trust_read_only"
	// BehaviorTrustAutoReview — an "auto_review" trust session hands routine
	// approvals to a reviewer inside the harness (claude-code's auto mode
	// classifier, goose's smart_approve LLM check) and escalates only what
	// the reviewer blocks or can't decide to the user. Optional: a provider
	// without a reviewer declares it Unsupported, and such a session runs
	// as limited instead (EffectiveTrust) — visibly, never silently.
	BehaviorTrustAutoReview Behavior = "trust_auto_review"
	// BehaviorOneShotStream — OneShotRequest.OnEvent actually fires with
	// incremental text (and tool use) during the invocation, rather than
	// only the final result being available once OneShot returns. Optional:
	// a provider with no incremental output mechanism simply never calls
	// OnEvent; the engine falls back to a single end-of-run chunk.
	BehaviorOneShotStream Behavior = "one_shot_stream"
	// BehaviorOneShotResume — OneShotRequest.Resume continues a previous
	// session's conversation (by the ref a one-shot reported, or an
	// interactive Session.Ref) instead of starting fresh. Separate from
	// BehaviorResume, which is about interactive Start: a harness can
	// reattach a terminal session yet have a one-shot path that takes no
	// ref (kiro-cli's `chat --no-interactive` reports none at all), and the
	// pipeline engine decides between resuming and a fresh fallback attempt
	// from this row alone, without calling. Optional; unsupported means
	// OneShot with Resume returns ErrResumeUnsupported.
	BehaviorOneShotResume Behavior = "one_shot_resume"
	// BehaviorRefAnnounce — the session's provider-native ref becomes knowable
	// before the first turn completes, via RefAnnouncer, rather than only
	// after a turn ends. Expected: every provider takes an honest stance, even
	// if that stance ends up Unsupported for a transport with no mechanism at
	// all (a provider is allowed to declare this Unsupported if it genuinely
	// has nothing — none of the three shipped providers do).
	BehaviorRefAnnounce Behavior = "ref_announce"
	// BehaviorCompaction — the harness runs its own context-compaction pass
	// on request, out of band from a normal turn (provider.Compactor on the
	// structured session). Optional, and never emulated: a summary mads
	// wrote itself would be a different conversation, so a provider without
	// one declares Unsupported and every Compact affordance hides for it.
	BehaviorCompaction Behavior = "compaction"
	// BehaviorCompactionFocus — the compaction pass takes free-text focus
	// instructions (Compactor.Compact's instructions) and the harness
	// actually steers its summary by them. Optional; working requires
	// BehaviorCompaction working.
	BehaviorCompactionFocus Behavior = "compaction_focus"

	// Delegates (docs/DELEGATES-DESIGN.md): subagents the harness runs on a
	// session's behalf. These rows are the per-harness defaults; what is
	// observed about one delegate at run time (DelegateInspector) overrides
	// them for that delegate. All optional — most harnesses have none.

	// BehaviorDelegateLifecycle — the adapter reports a delegate's lifecycle
	// (EventTask/EventTaskRoster) separately from the tool call that spawned
	// it, so a delegate can outlive its parent's turn. Without it a delegate
	// is only ever its spawning call.
	BehaviorDelegateLifecycle Behavior = "delegate_lifecycle"
	// BehaviorDelegateTranscript — a delegate keeps its own durable
	// transcript the adapter can locate (DelegateInspector). Native means
	// the strategy is implemented; whether a given delegate has one is a
	// run-time answer.
	BehaviorDelegateTranscript Behavior = "delegate_transcript"
	// BehaviorDelegateSteer — a user's message can reach a running or
	// finished delegate: Native = sent to it directly; Emulated = relayed by
	// its parent agent (a message to the parent asking it to pass it on —
	// Claude Code's SendMessage), so the parent decides how faithfully.
	BehaviorDelegateSteer Behavior = "delegate_steer"
	// BehaviorDelegateStop — mads can stop one delegate without stopping
	// its parent.
	BehaviorDelegateStop Behavior = "delegate_stop"

	// BehaviorStableKeys — every conversation item carries a Key derived
	// from the agent's own ids, the same live (Event.Key) and stored
	// (Message.Key), with the same text on both sides
	// (docs/CHAT-STREAM-DESIGN.md §1; the conformance stable_keys probe).
	// Native: the agent's stored ids are the live ids. Emulated: the
	// adapter matches live items to stored rows itself (hermes, by
	// position). Optional; internal plumbing, not a user-facing capability.
	BehaviorStableKeys Behavior = "stable_keys"
)

// Support is a provider's declared implementation status for one behavior.
type Support string

const (
	// Native — the harness does this itself.
	Native Support = "native"
	// Emulated — the adapter fakes it well enough to satisfy conformance
	// (e.g. a pinned model list where the harness has no discovery call).
	Emulated Support = "emulated"
	// Unsupported — calls exercising the behavior return ErrUnsupported.
	Unsupported Support = "unsupported"
)

// Works reports whether the declaration promises functioning behavior —
// conformance probes cannot (and need not) tell native from emulated.
func (s Support) Works() bool { return s == Native || s == Emulated }

// Level is how much the platform insists on a behavior (spec §11).
type Level string

const (
	// LevelRequired — a provider that lacks this cannot be registered.
	LevelRequired Level = "required"
	// LevelExpected — providers should have this; the UI degrades visibly
	// where it is missing.
	LevelExpected Level = "expected"
	// LevelOptional — nice to have; features gate on its presence.
	LevelOptional Level = "optional"
)

// requirements is the authoritative behavior list. A Matrix must declare
// exactly these keys — no more, no fewer — so a behavior added here fails
// every adapter's conformance run until that adapter takes a stance.
var requirements = map[Behavior]Level{
	BehaviorInteractive:     LevelRequired,
	BehaviorInterrupt:       LevelRequired,
	BehaviorOneShot:         LevelRequired,
	BehaviorResume:          LevelExpected,
	BehaviorTranscript:      LevelExpected,
	BehaviorModelDiscovery:  LevelExpected,
	BehaviorCostReporting:   LevelExpected,
	BehaviorACP:             LevelExpected, // "one of" rule enforced separately
	BehaviorPTY:             LevelExpected,
	BehaviorBranching:       LevelOptional,
	BehaviorCwdReroot:       LevelOptional,
	BehaviorMCPServers:      LevelOptional,
	BehaviorTrustLimited:    LevelExpected,
	BehaviorTrustReadOnly:   LevelExpected,
	BehaviorTrustAutoReview: LevelOptional,
	BehaviorOneShotStream:   LevelOptional,
	BehaviorOneShotResume:   LevelOptional,
	BehaviorRefAnnounce:     LevelExpected,
	BehaviorCompaction:      LevelOptional,
	BehaviorCompactionFocus: LevelOptional,

	BehaviorDelegateLifecycle:  LevelOptional,
	BehaviorDelegateTranscript: LevelOptional,
	BehaviorDelegateSteer:      LevelOptional,
	BehaviorDelegateStop:       LevelOptional,

	BehaviorStableKeys: LevelOptional,
}

// Requirement returns the platform's requirement level for a behavior, or ""
// for an unknown behavior.
func Requirement(b Behavior) Level { return requirements[b] }

// Behaviors returns every known behavior, sorted, for iteration in tests and
// UI (the Settings provider table renders from this).
func Behaviors() []Behavior {
	out := make([]Behavior, 0, len(requirements))
	for b := range requirements {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Matrix is a provider's declared support for every known behavior.
type Matrix map[Behavior]Support

// Validate reports every structural problem at once (§10.3 ethos): unknown or
// missing behaviors, invalid support values, required behaviors declared
// unsupported, and neither transport working.
func (m Matrix) Validate() error {
	var probs []string
	for b := range m {
		if _, known := requirements[b]; !known {
			probs = append(probs, fmt.Sprintf("unknown behavior %q", b))
		}
	}
	for _, b := range Behaviors() {
		s, ok := m[b]
		if !ok {
			probs = append(probs, fmt.Sprintf("behavior %q not declared", b))
			continue
		}
		switch s {
		case Native, Emulated, Unsupported:
		default:
			probs = append(probs, fmt.Sprintf("behavior %q: invalid support %q", b, s))
			continue
		}
		if requirements[b] == LevelRequired && !s.Works() {
			probs = append(probs, fmt.Sprintf("required behavior %q declared unsupported", b))
		}
	}
	if !m[BehaviorACP].Works() && !m[BehaviorPTY].Works() {
		probs = append(probs, "at least one of acp/pty must be supported")
	}
	if m[BehaviorCompactionFocus].Works() && !m[BehaviorCompaction].Works() {
		probs = append(probs, "compaction_focus needs compaction")
	}
	if m[BehaviorStableKeys].Works() && !m[BehaviorTranscript].Works() {
		probs = append(probs, "stable_keys needs transcript (a key is only stable if a stored row carries it)")
	}
	// Never emulated (see BehaviorCompaction): Works() would count Emulated
	// as working and light up every Compact affordance.
	for _, b := range []Behavior{BehaviorCompaction, BehaviorCompactionFocus} {
		if m[b] == Emulated {
			probs = append(probs, fmt.Sprintf("behavior %q cannot be emulated", b))
		}
	}
	if len(probs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid capability matrix: %s", strings.Join(probs, "; "))
}
