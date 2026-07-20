package claudecode

import (
	"encoding/json"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// wireLine is one stream-json output line (or the whole one-shot JSON
// result). Field shapes verified against claude-code 2.1.207.
type wireLine struct {
	Type      string       `json:"type"`
	Subtype   string       `json:"subtype"`
	SessionID string       `json:"session_id"`
	Message   *wireMessage `json:"message"`

	// result-line fields
	Result       string                     `json:"result"`
	IsError      bool                       `json:"is_error"`
	Usage        *wireUsage                 `json:"usage"`
	TotalCostUSD float64                    `json:"total_cost_usd"`
	ModelUsage   map[string]json.RawMessage `json:"modelUsage"`

	// init-line fields
	Model string `json:"model"`
}

// model names the model where the line says: init lines carry it directly,
// result lines only as the modelUsage key.
func (l *wireLine) model() string {
	if l.Model != "" {
		return l.Model
	}
	if len(l.ModelUsage) == 1 {
		for m := range l.ModelUsage {
			return m
		}
	}
	return ""
}

// wireMessage is an API message; content is either a plain string or a
// block list, so it stays raw until blocks() decodes it.
type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type wireBlock struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	// tool_result-block fields (only present in a "user" role message that
	// feeds a tool's outcome back into context).
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// blocks decodes message content: a bare string becomes one text block.
func (m *wireMessage) blocks() []wireBlock {
	if m == nil || len(m.Content) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return []wireBlock{{Type: "text", Text: s}}
	}
	var bs []wireBlock
	if err := json.Unmarshal(m.Content, &bs); err != nil {
		return nil
	}
	return bs
}

// toolResultText decodes a tool_result block's content: a bare string, or an
// array of blocks (mirroring wireMessage.blocks' string-vs-array handling) —
// only text blocks contribute.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var bs []wireBlock
	if err := json.Unmarshal(raw, &bs); err != nil {
		return ""
	}
	var texts []string
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// toolPath digs the touched file out of a tool input, if any — the §5
// tool-use attribution signal.
func toolPath(input json.RawMessage) string {
	var in struct {
		FilePath     string `json:"file_path"`
		Path         string `json:"path"`
		NotebookPath string `json:"notebook_path"`
	}
	_ = json.Unmarshal(input, &in)
	switch {
	case in.FilePath != "":
		return in.FilePath
	case in.NotebookPath != "":
		return in.NotebookPath
	default:
		return in.Path
	}
}

// events normalizes one stream line. interrupted marks the turn end when an
// interrupt was requested since the turn began.
func (l *wireLine) events(interrupted bool) []provider.Event {
	switch l.Type {
	case "assistant":
		var evs []provider.Event
		for _, b := range l.Message.blocks() {
			switch b.Type {
			case "text":
				if b.Text != "" {
					evs = append(evs, provider.Event{Kind: provider.EventText, Text: b.Text})
				}
			case "tool_use":
				evs = append(evs, provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{
					ID: b.ID, Name: b.Name, Input: b.Input, Path: toolPath(b.Input),
				}})
			}
		}
		return evs
	case "result":
		ev := provider.Event{Kind: provider.EventTurnEnd, Interrupted: interrupted}
		if l.Usage != nil {
			ev.Usage = &provider.Usage{
				InputTokens:              l.Usage.InputTokens,
				OutputTokens:             l.Usage.OutputTokens,
				CacheCreationInputTokens: l.Usage.CacheCreationInputTokens,
				CacheReadInputTokens:     l.Usage.CacheReadInputTokens,
				Model:                    l.model(),
			}
		}
		if l.IsError {
			return []provider.Event{
				{Kind: provider.EventError, Text: l.Result},
				ev,
			}
		}
		return []provider.Event{ev}
	case "user":
		// claude-code feeds a tool's outcome back into the model's context
		// as a "user" role message — the same shape normalizeTranscript
		// reads from disk — including permission denials, which otherwise
		// produce no event at all on a headless transport with nobody to
		// answer the CLI's own prompt. Surfacing it live, rather than
		// waiting for the next transcript refetch, is what lets a blocked
		// tool call render instead of silently stalling.
		var evs []provider.Event
		for _, b := range l.Message.blocks() {
			if b.Type != "tool_result" {
				continue
			}
			evs = append(evs, provider.Event{
				Kind:    provider.EventToolResult,
				Tool:    &provider.ToolUse{ID: b.ToolUseID},
				Text:    toolResultText(b.Content),
				IsError: b.IsError,
			})
		}
		return evs
	default:
		// init, thinking_tokens, rate_limit_event, control_response, ...:
		// bookkeeping, not conversation.
		return nil
	}
}
