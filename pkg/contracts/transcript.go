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
	// "failed", "interrupted", "blocked" (never ran: permission refused).
	// Empty means unknown.
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
	Role        string       `json:"role"` // "user", "agent", "tool", "tool_result", "compact", "notification", or "inbound"
	Text        string       `json:"text,omitempty"`
	Tool        *ToolUse     `json:"tool,omitempty"`
	Attribution *Attribution `json:"attribution,omitempty"`
	// Timestamp is when the provider recorded this entry, when its wire
	// format carries one. Zero value when unknown.
	Timestamp time.Time `json:"timestamp,omitempty"`
	// IsError marks a Role "tool_result" entry as the tool call having
	// failed, or a Role "agent" entry as the harness reporting a failed
	// turn in the agent's place (auth rejected, API unreachable, out of
	// credits) rather than a reply. Meaningless on other roles.
	IsError bool `json:"is_error,omitempty"`
	// Compact describes a Role "compact" entry (a compaction pass); Text is
	// then the summary carried forward. Mirrors provider.CompactMarker.
	Compact *CompactMarker `json:"compact,omitempty"`
	// Inbound describes a Role "inbound" entry: a message that reached the
	// agent from outside mads, over a channel its harness runs (claude-code's
	// cross-session peer messages). Text is the message body. Not a user
	// turn: it carries no Attribution and doesn't count as one.
	Inbound *InboundSource `json:"inbound,omitempty"`
}

// InboundSource says where an inbound message (TranscriptMessage Role
// "inbound", the live stream's "inbound" event) came from. Built by the
// session layer from providers/provider's Inbound: the harness-level fields
// pass through, and the sender is resolved to a mads session when its
// address matches one (session.peer_address). The sender's raw address is
// deliberately not carried: it is opaque, and only ever matched.
type InboundSource struct {
	// Channel is the harness's channel id ("peer" for claude-code).
	Channel string `json:"channel"`
	// Provider is the RECIPIENT session's provider name ("claude-code"):
	// the harness whose channel carried the message.
	Provider string `json:"provider"`
	// FromName is the harness's own label for the sender — what to show
	// when the sender isn't a mads session.
	FromName string `json:"from_name,omitempty"`
	// SessionID and SessionName are set only when the sender resolved to a
	// mads session; SessionName is its current name.
	SessionID   string `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	// LinkID is the link between sender and recipient, either direction,
	// when one exists.
	LinkID string `json:"link_id,omitempty"`
	// State is "" for a message delivered to the agent, "held" when the
	// harness held it back; HoldReason is the harness's reason then.
	State      string `json:"state,omitempty"`
	HoldReason string `json:"hold_reason,omitempty"`
}

// StreamInbound is the live stream's (GET /output) "inbound" event kind:
// {"kind": "inbound", "text": <body>, "inbound": InboundSource}.
const StreamInbound = "inbound"

// InboundStreamEvent is the body of the live stream's "inbound" event — a
// message from outside mads arrived (see InboundSource). Text is empty for
// a held message the harness reported without its body.
type InboundStreamEvent struct {
	Kind    string         `json:"kind"`
	Text    string         `json:"text"`
	Inbound *InboundSource `json:"inbound"`
}

// PeerAddress is the payload of SessionPeerAddress.
type PeerAddress struct {
	Address string `json:"address"`
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
	// CostUSD is what the provider said the turn cost (provider.Usage's
	// CostUSD); zero when it didn't say.
	CostUSD float64 `json:"cost_usd,omitempty"`
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
	// Kind is what the message is: "message" (a linked session's message),
	// "digest", "signal" or "notice" (MADS itself). Empty at delivery means
	// "message". Together with SourceName, Relation and Turn it is written
	// into the message text as the pkg/linkwrap wrapper, which is where a
	// transcript's Attribution comes from — LinkID is not carried there.
	Kind string `json:"kind,omitempty"`
	// Relation is the sender seen from the RECIPIENT: "your leader", "a
	// session monitoring you", "a session you lead", "a session you
	// monitor", "your pipeline run". Empty for MADS's own notices.
	Relation string `json:"relation,omitempty"`
	// Turn is, on a digest, the subject session's transcript turn the digest
	// covers up to (get_session_turns' 1-based turn model); 0 when unknown.
	Turn int `json:"turn,omitempty"`
}

// InjectRequest is the body of POST /inject (session-service) and
// POST /links/{id}/inject (Router): the text to deliver plus the
// attribution the Router has already resolved server-side.
type InjectRequest struct {
	Text        string      `json:"text"`
	Attribution Attribution `json:"attribution"`
	// Parts, when set, replaces Text/Attribution: several messages delivered
	// as ONE turn, each keeping its own label (the receiving runner wraps
	// each part on its own). A wake's flush of deferred pushes from several
	// links uses it so every update still says who it came from (2026-10-20).
	Parts []InjectPart `json:"parts,omitempty"`
}

// InjectPart is one labelled message within a multi-part InjectRequest.
type InjectPart struct {
	Text        string      `json:"text"`
	Attribution Attribution `json:"attribution"`
}
