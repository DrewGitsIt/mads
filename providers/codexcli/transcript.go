package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/drewkidwell/mads/providers/provider"
)

// itemsPageSize is thread/items/list's page size. The survey listed 17
// items with limit 100; a long session pages.
const itemsPageSize = 200

// itemEntry is one ThreadItemEntry from thread/items/list.
type itemEntry struct {
	TurnID string     `json:"turnId"`
	Item   threadItem `json:"item"`
}

// Transcript implements provider.TranscriptReader with thread/items/list:
// codex's own item store, read through codex rather than around it.
//
// Asked of the session's own app-server when one is running (the items it
// streamed are already in the store — survey lag 0–27 ms), else of a
// short-lived app-server spawned for the call: thread/items/list needs no
// thread load, just initialize (≈0.8 s spawn-to-answer, probed against an
// unloaded thread).
//
// Why not the rollout at ~/.codex/sessions/YYYY/MM/DD/rollout-*-<id>.jsonl:
// it is codex's private file format, and every trap the survey found lives
// there — the user message's response_item carries a different id from its
// item_completed event; injected context (environment_context,
// skills_instructions, recommended_plugins, turn_aborted,
// world_state/turn_context) is written as user-role messages and would
// have to be recognised and dropped by name, forever, as codex adds more.
// thread/items/list carries none of that, uses the ids the live events
// used, and is versioned with the protocol this adapter already speaks.
// What it lacks is timestamps: Message.Timestamp stays zero.
//
// An interrupted turn's partial reply is in neither: codex stores an item
// only once it completes (survey finding 6).
func (p *Provider) Transcript(ctx context.Context, ref string) ([]provider.Message, error) {
	if ref == "" {
		return nil, errors.New("transcript: empty thread id")
	}
	var entries []itemEntry
	list := func(c *conn) error {
		entries = entries[:0]
		var err error
		entries, err = listItems(ctx, c, ref)
		return err
	}
	var err error
	if s := p.liveSession(ref); s != nil {
		err = list(s.c)
		if errors.Is(err, errConnClosed) {
			err = p.withServer(ctx, list)
		}
	} else {
		err = p.withServer(ctx, list)
	}
	if err != nil {
		return nil, fmt.Errorf("transcript %s: %w", ref, err)
	}
	return normalizeItems(entries), nil
}

func listItems(ctx context.Context, c *conn, ref string) ([]itemEntry, error) {
	var all []itemEntry
	cursor := ""
	for range 10000 {
		params := map[string]any{"threadId": ref, "limit": itemsPageSize}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(ctx, "thread/items/list", params)
		if err != nil {
			return nil, fmt.Errorf("thread/items/list: %w", err)
		}
		var res struct {
			Data       []itemEntry `json:"data"`
			NextCursor *string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("thread/items/list: %w", err)
		}
		all = append(all, res.Data...)
		if res.NextCursor == nil || *res.NextCursor == "" || len(res.Data) == 0 {
			return all, nil
		}
		cursor = *res.NextCursor
	}
	return all, nil
}

// normalizeItems maps thread items onto the normalized transcript, in
// store order: a user message per userMessage, an agent message per
// agentMessage (commentary and final answer alike — each is its own item),
// a tool row per tool item followed by its result once the item finished,
// a compact marker per contextCompaction. Reasoning summaries are left
// out (Message has no thinking role); so are plan, review-mode and hook
// items. A tool item still in progress — the turn was killed mid-command —
// keeps its tool row and gets no result.
func normalizeItems(entries []itemEntry) []provider.Message {
	var msgs []provider.Message
	for _, e := range entries {
		it := e.Item
		switch {
		case it.Type == "userMessage":
			if text := it.userText(); text != "" {
				msgs = append(msgs, provider.Message{Role: "user", Text: text})
			}
		case it.Type == "agentMessage":
			if it.Text != "" {
				msgs = append(msgs, provider.Message{Role: "agent", Text: it.Text})
			}
		case it.Type == "contextCompaction":
			msgs = append(msgs, provider.Message{Role: "compact", Compact: &provider.CompactMarker{}})
		case it.isTool():
			tool := it.toolUse()
			text, isError, status := it.toolResult("")
			if it.Status != "inProgress" {
				tool.Status = status
			}
			msgs = append(msgs, provider.Message{Role: "tool", Tool: tool})
			if it.Status == "inProgress" {
				continue
			}
			msgs = append(msgs, provider.Message{
				Role:    "tool_result",
				Tool:    &provider.ToolUse{ID: it.ID, Name: tool.Name, Kind: tool.Kind, Status: status},
				Text:    text,
				IsError: isError,
			})
		}
	}
	return msgs
}
