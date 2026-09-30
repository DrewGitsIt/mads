package contracts

import (
	"encoding/json"
	"time"
)

// ToolUse mirrors providers/provider/event.go ToolUse — the wire shape of a
// normalized tool invocation inside a transcript message. Duplicated here
// (not imported from providers/provider) so pkg/contracts stays free of a
// dependency on the provider layer, same as every other cross-boundary type
// in this package.
type ToolUse struct {
	// ID is the provider-native tool_use identifier, when the wire format
	// has one — the correlation key back to a matching tool_result.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
	Path  string          `json:"path,omitempty"`
	// Kind classifies the call for rendering — providers/provider/event.go
	// ToolKind's vocabulary: "read", "edit", "delete", "move", "search",
	// "execute", "think", "fetch", "agent", "mcp", "skill", "plan", "other".
	// Empty on records persisted before enrichment; readers must treat that
	// as unknown, not "other".
	Kind string `json:"kind,omitempty"`
	// Title is the adapter-built one-line human summary of the call.
	Title string `json:"title,omitempty"`
	// Status is the call's lifecycle state — providers/provider/event.go
	// ToolStatus's vocabulary: "pending", "in_progress", "completed",
	// "failed", "interrupted". Empty means unknown.
	Status string `json:"status,omitempty"`
	// Locations lists the file positions the call touched, when known.
	Locations []ToolLocation `json:"locations,omitempty"`
	// Server is the MCP server name, set only when Kind is "mcp".
	Server string `json:"server,omitempty"`
}

// ToolLocation mirrors providers/provider/event.go ToolLocation — one file
// position a tool call touched, absolute path when the harness reports one.
// Line is 1-based; zero means the whole file.
type ToolLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// TranscriptMessage mirrors providers/provider/event.go Message — one
// normalized transcript entry, as returned by GET /sessions/{id}/transcript.
type TranscriptMessage struct {
	Role        string       `json:"role"` // "user", "agent", "tool", "tool_result", "compact", or "notification"
	Text        string       `json:"text,omitempty"`
	Tool        *ToolUse     `json:"tool,omitempty"`
	Attribution *Attribution `json:"attribution,omitempty"`
	// Timestamp is when the provider recorded this entry, when its wire
	// format carries one. Zero value when unknown.
	Timestamp time.Time `json:"timestamp,omitempty"`
	// IsError marks a Role "tool_result" entry as the tool call having
	// failed. Meaningless on other roles.
	IsError bool `json:"is_error,omitempty"`
	// Compact describes a Role "compact" entry (a compaction pass); Text is
	// then the summary carried forward. Mirrors provider.CompactMarker.
	Compact *CompactMarker `json:"compact,omitempty"`
}

// CompactMarker mirrors providers/provider's CompactMarker.
type CompactMarker struct {
	Trigger    string `json:"trigger,omitempty"`
	PreTokens  int    `json:"pre_tokens,omitempty"`
	PostTokens int    `json:"post_tokens,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// Usage mirrors providers/provider/event.go Usage — duplicated here (not
// imported from providers/provider) so pkg/contracts stays free of a
// dependency on the provider layer, same as every other cross-boundary type
// in this package. Persisted on Session.LastUsage so a resumed session's
// context occupancy is known at wake time, not only after its first turn_end
// in this process (ui/src/conversation/Header/ContextMeter.tsx's data gap;
// also the resume-compaction trigger's input, services/session/internal/
// runner/compact.go).
type Usage struct {
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens,omitempty"`
	Model                    string `json:"model,omitempty"`
	// ContextTokens is the prompt-side occupancy of the turn's final API
	// call — the accurate context measure. The token fields above are a
	// per-turn billing aggregate on providers that iterate (claude-code),
	// which overstates occupancy by ~the iteration count; prefer this when
	// set. Zero when the provider doesn't report per-call occupancy.
	ContextTokens int `json:"context_tokens,omitempty"`
	// ContextWindow is the provider-reported window size for Model —
	// authoritative over the static ContextWindowForModel table. Zero when
	// unreported.
	ContextWindow int `json:"context_window,omitempty"`
}

// Attribution identifies the linked session a message was injected from
// (docs/LINKED-SESSIONS-DESIGN.md decision 2): every injected message is
// stamped with its source, end-to-end, so it can never be rendered or
// reasoned about as the subject user's own turn. SourceName is the granting
// session's Name, snapshotted at delivery time — links don't cascade, so a
// later rename or deletion of the granting session must not change history.
type Attribution struct {
	LinkID     string `json:"link_id"`
	SourceName string `json:"source_name"`
	// DigestAttributed marks an injection as a job.GenerateDigest push
	// (docs/LINKED-SESSIONS-DESIGN.md decision 4), as opposed to a real
	// user/leader message. A turn whose injected entries are solely
	// digest-attributed is excluded from re-triggering digestwatch, so a
	// monitor's own forced turn can't self-amplify a monitor chain.
	DigestAttributed bool `json:"digest_attributed,omitempty"`
}

// InjectRequest is the body of POST /inject (session-service) and
// POST /links/{id}/inject (Router): the text to deliver plus the
// attribution the Router has already resolved server-side.
type InjectRequest struct {
	Text        string      `json:"text"`
	Attribution Attribution `json:"attribution"`
}
