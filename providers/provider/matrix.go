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
	// BehaviorBranching — fork a session into alternatives. Optional.
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
	// BehaviorOneShotStream — OneShotRequest.OnEvent actually fires with
	// incremental text (and tool use) during the invocation, rather than
	// only the final result being available once OneShot returns. Optional:
	// a provider with no incremental output mechanism simply never calls
	// OnEvent; the engine falls back to a single end-of-run chunk.
	BehaviorOneShotStream Behavior = "one_shot_stream"
	// BehaviorRefAnnounce — the session's provider-native ref becomes knowable
	// before the first turn completes, via RefAnnouncer, rather than only
	// after a turn ends. Expected: every provider takes an honest stance, even
	// if that stance ends up Unsupported for a transport with no mechanism at
	// all (a provider is allowed to declare this Unsupported if it genuinely
	// has nothing — none of the three shipped providers do).
	BehaviorRefAnnounce Behavior = "ref_announce"
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
	BehaviorInteractive:    LevelRequired,
	BehaviorInterrupt:      LevelRequired,
	BehaviorOneShot:        LevelRequired,
	BehaviorResume:         LevelExpected,
	BehaviorTranscript:     LevelExpected,
	BehaviorModelDiscovery: LevelExpected,
	BehaviorCostReporting:  LevelExpected,
	BehaviorACP:            LevelExpected, // "one of" rule enforced separately
	BehaviorPTY:            LevelExpected,
	BehaviorBranching:      LevelOptional,
	BehaviorCwdReroot:      LevelOptional,
	BehaviorMCPServers:     LevelOptional,
	BehaviorTrustLimited:   LevelExpected,
	BehaviorTrustReadOnly:  LevelExpected,
	BehaviorOneShotStream:  LevelOptional,
	BehaviorRefAnnounce:    LevelExpected,
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
	if len(probs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid capability matrix: %s", strings.Join(probs, "; "))
}
