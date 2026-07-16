package contracts

import "encoding/json"

// ToolUse mirrors providers/provider/event.go ToolUse — the wire shape of a
// normalized tool invocation inside a transcript message. Duplicated here
// (not imported from providers/provider) so pkg/contracts stays free of a
// dependency on the provider layer, same as every other cross-boundary type
// in this package.
type ToolUse struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
	Path  string          `json:"path,omitempty"`
}

// TranscriptMessage mirrors providers/provider/event.go Message — one
// normalized transcript entry, as returned by GET /sessions/{id}/transcript.
type TranscriptMessage struct {
	Role string   `json:"role"` // "user", "agent", or "tool"
	Text string   `json:"text,omitempty"`
	Tool *ToolUse `json:"tool,omitempty"`
}
