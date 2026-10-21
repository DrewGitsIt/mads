package goosecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/providers/provider"
)

// Transcript implements provider.TranscriptReader over goose's own store,
// sessions.db (store.go), falling back to `goose session export
// --session-id <ref> --format json` when the store isn't there, doesn't
// hold the session, or its private schema isn't the one this reader knows
// (design doc "Transcript normalization"; export shape pinned against
// testdata/captures/oneshot-export.json). Both sources carry the same
// message ids and created stamps, so the keys agree either way.
func (p *Provider) Transcript(ctx context.Context, ref string) ([]provider.Message, error) {
	if ref == "" {
		return nil, fmt.Errorf("transcript: empty session ref")
	}
	rows, _, err := p.readStore(ctx, ref, nil)
	if err == nil {
		return normalizeExport(rows), nil
	}
	if !errors.Is(err, errStoreUnusable) {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	return p.exportTranscript(ctx, ref)
}

// TranscriptSince implements provider.IncrementalTranscriptReader over
// sessions.db with a rowid cursor (store.go). ErrUnsupported whenever
// Transcript would have fallen back to the export: re-serialising the
// whole session is no incremental read.
func (p *Provider) TranscriptSince(ctx context.Context, ref string, cursor provider.TranscriptCursor) ([]provider.Message, provider.TranscriptCursor, error) {
	if ref == "" {
		return nil, cursor, fmt.Errorf("transcript: empty session ref")
	}
	cur, _ := cursor.(storeCursor)
	rows, next, err := p.readStore(ctx, ref, &cur)
	switch {
	case errors.Is(err, errStoreUnusable):
		return nil, cursor, fmt.Errorf("transcript since: %w: %w", err, provider.ErrUnsupported)
	case err != nil:
		return nil, cursor, err
	}
	return normalizeExport(rows), next, nil
}

// exportTranscript is the fallback reader: goose's own export, one spawn
// that re-serialises the whole session.
func (p *Provider) exportTranscript(ctx context.Context, ref string) ([]provider.Message, error) {
	out, err := p.gooseQuery(ctx, "session", "export", "--session-id", ref, "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	var doc exportDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("transcript: parse export for %s: %w", ref, err)
	}
	return normalizeExport(doc.Conversation), nil
}

// gooseQuery execs a goose CLI bookkeeping subcommand (`session export`,
// `session list`) and returns its stdout. Env composition is the same
// direct agentenv form the PTY spawn uses; cfg.ExtraArgs are deliberately
// NOT appended — they are agent-spawn flags (REPL/ACP) and would break the
// bookkeeping subcommands. No cmd.Dir: goose's session store is global. An
// exit failure carries goose's stderr in the error — that's where `Session
// not found` lands.
func (p *Provider) gooseQuery(ctx context.Context, args ...string) ([]byte, error) {
	bin := p.bin()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = agentenv.Build(p.cfg.EnvMode, os.Environ(), p.cfg.Env, p.cfg.EnvPassthrough, nil)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	return out, nil
}

// exportDoc is `goose session export --format json`'s top level — only the
// fields the adapter reads (the full document also carries usage,
// extension_data, model_config, …).
type exportDoc struct {
	ID           string          `json:"id"`
	WorkingDir   string          `json:"working_dir"`
	Conversation []exportMessage `json:"conversation"`
}

// exportMessage is one conversation[] entry — and one sessions.db messages
// row, which holds the same fields (message_id, role, created_timestamp,
// content_json, metadata_json). Created is unix seconds: the first
// streamed chunk's _meta.goose.created for an assistant message (survey
// captures/keys). rowID is the store's row id (0 from an export).
type exportMessage struct {
	ID       string `json:"id"`
	rowID    int64
	Role     string          `json:"role"`
	Created  int64           `json:"created"`
	Content  []exportBlock   `json:"content"`
	Metadata *exportMetadata `json:"metadata"`
}

// visible reports whether the entry is user-visible conversation: goose
// stamps injected turn-context entries `metadata.userVisible:false`. Only an
// explicit false hides — absent metadata stays visible.
func (m exportMessage) visible() bool {
	return m.Metadata == nil || m.Metadata.UserVisible == nil || *m.Metadata.UserVisible
}

type exportMetadata struct {
	UserVisible *bool `json:"userVisible"`
}

// exportBlock is one content[] block: text, thinking, toolRequest, or
// toolResponse. ID is the toolRequest/toolResponse correlation key
// (`call_…`), shared with the ACP stream's tool ids.
type exportBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	ID   string `json:"id"`
	// ToolCall is a toolRequest's payload: {status, value:{name, arguments}}.
	ToolCall *struct {
		Value struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"value"`
	} `json:"toolCall"`
	// ToolResult is a toolResponse's payload: {status, value:{content[],
	// structuredContent, isError}}.
	ToolResult *struct {
		Value struct {
			Content []exportBlock `json:"content"`
			IsError bool          `json:"isError"`
		} `json:"value"`
	} `json:"toolResult"`
}

// normalizeExport maps goose messages (an export's conversation[] or
// sessions.db rows) onto the provider-independent Message form (design doc
// "Transcript normalization"): user text → "user", assistant text →
// "agent", toolRequest → "tool", toolResponse → "tool_result" (these ride
// role:"user" entries — never misread them as user turns), thinking blocks
// and userVisible:false entries (the injected <turn-context> row after
// every prompt) skipped. Timestamps are the entry's unix `created`, held in
// UTC so output is machine-independent.
//
// Keys (docs/CHAT-STREAM-DESIGN.md §1), equal to the live ones acprpc
// derives under goose's KeyModeMessageRun knob:
//   - assistant text: "<message id>@<created>:<run>", run being the item's
//     index among the message's text items (thinking isn't counted: goose
//     moves a tool-calling message's thinking onto its tool-call rows, so
//     the stored text row may not have it). created tells apart ollama's
//     chatcmpl-0..999 ids, which repeat within a session.
//   - a tool call and its result: the call id (call_…). Live, a tool call
//     carries the preceding text's messageId in _meta; stored, it is a row
//     of its own (msg_<uuid>, sometimes a chatcmpl id), so only the call
//     id is the same on both sides.
//   - a user row: "user:"+its stored id. goose stores no caller id, so mads
//     matches its own sends to these rows on settle (no KeyedSender).
//
// A message stored without an id (none seen) is keyed by its row, or for
// an export by its position: synthetic, but stable for that source.
func normalizeExport(conv []exportMessage) []provider.Message {
	var msgs []provider.Message
	for i, m := range conv {
		if !m.visible() {
			continue
		}
		id := m.ID
		if id == "" {
			id = fmt.Sprintf("row-%d", m.rowID)
			if m.rowID == 0 {
				id = fmt.Sprintf("export-%d", i)
			}
		}
		ts := time.Unix(m.Created, 0).UTC()
		switch m.Role {
		case "user":
			var texts []string
			for _, b := range m.Content {
				switch b.Type {
				case "text":
					if b.Text != "" {
						texts = append(texts, b.Text)
					}
				case "toolResponse":
					msg := provider.Message{
						Role:      "tool_result",
						Tool:      &provider.ToolUse{ID: b.ID},
						Timestamp: ts,
						Key:       b.ID,
					}
					if b.ToolResult != nil {
						msg.Text = toolResponseText(b.ToolResult.Value.Content)
						msg.IsError = b.ToolResult.Value.IsError
					}
					msgs = append(msgs, msg)
				}
			}
			if len(texts) > 0 {
				msgs = append(msgs, provider.Message{Role: "user", Text: strings.Join(texts, "\n"), Timestamp: ts, Key: "user:" + id})
			}
		case "assistant":
			run := 0
			for _, b := range m.Content {
				switch b.Type {
				case "text":
					if b.Text != "" {
						msgs = append(msgs, provider.Message{Role: "agent", Text: b.Text, Timestamp: ts, Key: fmt.Sprintf("%s@%d:%d", id, m.Created, run)})
					}
					run++
				case "toolRequest":
					if b.ToolCall == nil {
						continue
					}
					msgs = append(msgs, provider.Message{
						Role:      "tool",
						Tool:      &provider.ToolUse{ID: b.ID, Name: b.ToolCall.Value.Name, Input: b.ToolCall.Value.Arguments},
						Timestamp: ts,
						Key:       b.ID,
					})
				}
			}
		}
	}
	return msgs
}

// toolResponseText joins a toolResult value's text blocks — the same
// content[] block shape, one level down.
func toolResponseText(content []exportBlock) string {
	var texts []string
	for _, b := range content {
		if b.Type == "text" && b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
}
