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
		res, err := listPage(ctx, c, params)
		if err != nil {
			return nil, err
		}
		all = append(all, res.Data...)
		if res.NextCursor == nil || *res.NextCursor == "" || len(res.Data) == 0 {
			return all, nil
		}
		cursor = *res.NextCursor
	}
	return all, nil
}

// Every row carries its item's key (itemKey): the same key the live events
// for the item carried. A tool row and its result row share it.
//
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
				msgs = append(msgs, provider.Message{Role: "user", Text: text, Key: itemKey(&it)})
			}
		case it.Type == "agentMessage":
			if it.Text != "" {
				msgs = append(msgs, provider.Message{Role: "agent", Text: it.Text, Key: it.ID})
			}
		case it.Type == "contextCompaction":
			msgs = append(msgs, provider.Message{Role: "compact", Compact: &provider.CompactMarker{}, Key: it.ID})
		case it.isTool():
			tool := it.toolUse()
			text, isError, status := it.toolResult("")
			if it.Status != "inProgress" {
				tool.Status = status
			}
			msgs = append(msgs, provider.Message{Role: "tool", Tool: tool, Key: it.ID})
			if it.Status == "inProgress" {
				continue
			}
			msgs = append(msgs, provider.Message{
				Role:    "tool_result",
				Tool:    &provider.ToolUse{ID: it.ID, Name: tool.Name, Kind: tool.Kind, Status: status},
				Text:    text,
				IsError: isError,
				Key:     it.ID,
			})
		}
	}
	return msgs
}

// itemsCursor is TranscriptSince's position in a thread's item store: the
// last item already returned, and a codex cursor anchored on it.
//
// Why an anchor from a descending page (probed against codex 0.154.0,
// 2026-10-21, a scratch thread, a whole turn appended between reads):
//
//   - nextCursor can't continue after the last item: codex returns null on
//     a page that reaches the end of the thread, even a full one (limit 2
//     over exactly 2 items → null). It is only set while more items exist.
//   - backwardsCursor ("pass as cursor when reversing sortDirection") is
//     anchored on the page's first item, includeAnchor true. On a
//     descending page of limit 1 that first item is the newest one, so the
//     cursor reads ascending from the newest item, itself included — and
//     it stayed valid after more turns were appended: asked again after a
//     second turn, it returned [the anchor, user 2, agent 2], exactly the
//     anchor plus what had been added since. (Its rolloutOrdinal is the
//     item's position in the append-only rollout, which appends don't
//     move.)
//
// So each read lists ascending from the anchor, checks the first item is
// the one the cursor named (anything else means the store no longer reads
// as it did: ErrCursorStale), drops it, and re-anchors on the new last
// item. Reads cost the new items plus one, never the session's length.
type itemsCursor struct {
	ref    string
	last   string // item id of the last item returned; "" before any
	anchor string // codex cursor: ascending from last, last included; "" = none
}

// TranscriptSince implements provider.IncrementalTranscriptReader over
// thread/items/list on the live session's own app-server — the session
// child's settle reads, cheap because the connection is already open. With
// no live session for ref it returns provider.ErrUnsupported: spawning an
// app-server per incremental read would cost more than the read saves, and
// the caller reads Transcript instead.
func (p *Provider) TranscriptSince(ctx context.Context, ref string, cursor provider.TranscriptCursor) ([]provider.Message, provider.TranscriptCursor, error) {
	s := p.liveSession(ref)
	if s == nil {
		return nil, cursor, fmt.Errorf("transcript since: no live codex session for %s: %w", ref, provider.ErrUnsupported)
	}
	cur := &itemsCursor{ref: ref}
	switch c := cursor.(type) {
	case nil:
	case *itemsCursor:
		if c.ref != ref {
			return nil, cursor, provider.ErrCursorStale
		}
		cur = c
	default:
		return nil, cursor, provider.ErrCursorStale
	}

	var entries []itemEntry
	var err error
	switch {
	case cur.last == "":
		entries, err = listItems(ctx, s.c, ref)
	case cur.anchor != "":
		entries, err = listItemsFrom(ctx, s.c, ref, cur.anchor)
		if err == nil {
			if len(entries) == 0 || entries[0].Item.ID != cur.last {
				return nil, cursor, provider.ErrCursorStale
			}
			entries = entries[1:]
		}
	default: // no anchor could be had last time: skip through last
		entries, err = listItems(ctx, s.c, ref)
		if err == nil {
			i := indexOfItem(entries, cur.last)
			if i < 0 {
				return nil, cursor, provider.ErrCursorStale
			}
			entries = entries[i+1:]
		}
	}
	if err != nil {
		return nil, cursor, fmt.Errorf("transcript since %s: %w", ref, err)
	}
	if len(entries) == 0 {
		return nil, cur, nil
	}
	last := entries[len(entries)-1].Item.ID
	anchor, err := anchorAt(ctx, s.c, ref, last)
	if err != nil {
		return nil, cursor, fmt.Errorf("transcript since %s: %w", ref, err)
	}
	return normalizeItems(entries), &itemsCursor{ref: ref, last: last, anchor: anchor}, nil
}

func indexOfItem(entries []itemEntry, id string) int {
	for i, e := range entries {
		if e.Item.ID == id {
			return i
		}
	}
	return -1
}

// itemsPage is one thread/items/list response.
type itemsPage struct {
	Data            []itemEntry `json:"data"`
	NextCursor      *string     `json:"nextCursor"`
	BackwardsCursor *string     `json:"backwardsCursor"`
}

func listPage(ctx context.Context, c *conn, params map[string]any) (itemsPage, error) {
	var res itemsPage
	raw, err := c.request(ctx, "thread/items/list", params)
	if err != nil {
		return res, fmt.Errorf("thread/items/list: %w", err)
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return res, fmt.Errorf("thread/items/list: %w", err)
	}
	return res, nil
}

// listItemsFrom lists ascending from cursor to the end of the thread.
func listItemsFrom(ctx context.Context, c *conn, ref, cursor string) ([]itemEntry, error) {
	var all []itemEntry
	for range 10000 {
		res, err := listPage(ctx, c, map[string]any{"threadId": ref, "limit": itemsPageSize, "cursor": cursor})
		if err != nil {
			return nil, err
		}
		all = append(all, res.Data...)
		if res.NextCursor == nil || *res.NextCursor == "" || len(res.Data) == 0 {
			break
		}
		cursor = *res.NextCursor
	}
	return all, nil
}

// anchorSteps bounds anchorAt's walk back from the newest item: items that
// landed between the read and the anchoring (a settle read mid-turn races
// the next item) are few.
const anchorSteps = 32

// anchorAt returns a cursor that lists ascending from item id, id included:
// the backwardsCursor of a one-item descending page whose item is id (see
// itemsCursor). Usually the first page — id is the newest item — but an
// item stored since the read moves id down, so it walks back. "" when id
// isn't within reach: the next read then skips through id itself.
func anchorAt(ctx context.Context, c *conn, ref, id string) (string, error) {
	cursor := ""
	for range anchorSteps {
		params := map[string]any{"threadId": ref, "limit": 1, "sortDirection": "desc"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := listPage(ctx, c, params)
		if err != nil {
			return "", err
		}
		if len(res.Data) == 0 {
			return "", nil
		}
		if res.Data[0].Item.ID == id {
			if res.BackwardsCursor == nil {
				return "", nil
			}
			return *res.BackwardsCursor, nil
		}
		if res.NextCursor == nil || *res.NextCursor == "" {
			return "", nil
		}
		cursor = *res.NextCursor
	}
	return "", nil
}
