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
}

// TranscriptMessage mirrors providers/provider/event.go Message — one
// normalized transcript entry, as returned by GET /sessions/{id}/transcript.
type TranscriptMessage struct {
	Role        string       `json:"role"` // "user", "agent", "tool", or "tool_result"
	Text        string       `json:"text,omitempty"`
	Tool        *ToolUse     `json:"tool,omitempty"`
	Attribution *Attribution `json:"attribution,omitempty"`
	// Timestamp is when the provider recorded this entry, when its wire
	// format carries one. Zero value when unknown.
	Timestamp time.Time `json:"timestamp,omitempty"`
	// IsError marks a Role "tool_result" entry as the tool call having
	// failed. Meaningless on other roles.
	IsError bool `json:"is_error,omitempty"`
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
