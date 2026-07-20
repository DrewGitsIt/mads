package provider

import (
	"encoding/json"
	"time"
)

// EventKind classifies one normalized session event. Every adapter maps its
// harness's wire format onto these — the platform above the provider layer
// never sees a harness-specific shape (spec §11).
type EventKind string

const (
	// EventText — a streamed chunk of the agent's response.
	EventText EventKind = "text"
	// EventToolUse — the agent invoked a tool; Tool carries the details.
	// Tool uses with a Path are the raw material for §5 file tracking.
	EventToolUse EventKind = "tool_use"
	// EventTurnEnd — the response finished (or was interrupted). Usage is
	// attached when the provider reports cost.
	EventTurnEnd EventKind = "turn_end"
	// EventError — a provider-level failure surfaced mid-session (rate
	// limit, protocol error). The session may or may not survive; a
	// subsequent channel close without EventTurnEnd means it did not.
	EventError EventKind = "error"
	// EventSessionRef — the provider announced its native session ref
	// (Ref) mid-invocation, before the turn finished. OneShot callers that
	// need exact attribution before completion (e.g. the pipeline engine,
	// to record which transcript belongs to a running stage attempt) rely
	// on this rather than waiting for OneShotResult.Ref. Fired at most
	// once per invocation; providers with no such wire-level announcement
	// never emit it.
	EventSessionRef EventKind = "session_ref"
)

// Event is one entry in a session's normalized stream.
type Event struct {
	Kind EventKind `json:"kind"`
	// Text for EventText: the chunk. For EventError: the message.
	Text string `json:"text,omitempty"`
	// Tool for EventToolUse.
	Tool *ToolUse `json:"tool,omitempty"`
	// Usage for EventTurnEnd, when cost reporting works.
	Usage *Usage `json:"usage,omitempty"`
	// Interrupted marks a turn ended by Interrupt rather than completion.
	Interrupted bool `json:"interrupted,omitempty"`
	// Ref for EventSessionRef: the provider-native session ref.
	Ref string `json:"ref,omitempty"`
}

// ToolUse is a normalized tool invocation.
type ToolUse struct {
	// ID is the provider-native tool_use identifier, when the wire format
	// has one — the correlation key back to a matching tool_result.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
	// Path is set when the tool touched a file — the provider layer's
	// contribution to §5 attribution (origin tool_use).
	Path string `json:"path,omitempty"`
}

// Usage is token accounting for one turn or one-shot (event log → metrics
// store, spec §8).
type Usage struct {
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Model        string `json:"model,omitempty"`
	// CacheCreationInputTokens and CacheReadInputTokens are the prompt-cache
	// halves of context Claude Code bills separately from InputTokens —
	// without them InputTokens wildly understates real context on any turn
	// that hit the cache.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	// CostUSD is the caller-computed dollar cost, when the provider reports
	// it (BehaviorCostReporting). Zero when unknown — the platform does no
	// pricing math of its own (spec §11's hookpoint for usage.recorded).
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// Message is one normalized transcript entry (TranscriptReader). Synthesis
// and retro (spec §4, §7) read this shape regardless of harness.
type Message struct {
	Role string   `json:"role"` // "user", "agent", "tool", or "tool_result"
	Text string   `json:"text,omitempty"`
	Tool *ToolUse `json:"tool,omitempty"`
	// Timestamp is when the provider recorded this entry, when the wire
	// format carries one. Zero value when unknown.
	Timestamp time.Time `json:"timestamp,omitempty"`
	// IsError marks a Role "tool_result" entry as the tool call having
	// failed. Meaningless on other roles.
	IsError bool `json:"is_error,omitempty"`
}
