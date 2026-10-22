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
//
// A failed turn has no item saying so: its failure is on the turn
// (thread/turns/list status "failed", error), read alongside and placed
// after the turn's items as an IsError agent row (turnFailures).
func (p *Provider) Transcript(ctx context.Context, ref string) ([]provider.Message, error) {
	entries, failed, err := p.readItems(ctx, ref)
	if err != nil {
		return nil, err
	}
	return normalizeItems(entries, failed, false), nil
}

// KeyedTranscript implements provider.KeyedTranscriptReader: Transcript's
// rows plus a Role "thinking" row per reasoning item whose summary has
// text, keyed by the item id and with the text the live EventThinking
// carried (reasoningText of the same summary; survey run1: four of four
// equal live and stored, one of them empty). An empty summary makes no
// row, as it makes no live item.
func (p *Provider) KeyedTranscript(ctx context.Context, ref string) ([]provider.Message, error) {
	entries, failed, err := p.readItems(ctx, ref)
	if err != nil {
		return nil, err
	}
	return normalizeItems(entries, failed, true), nil
}

// readItems lists ref's items, and the failures of its failed turns, from
// the live session's app-server, else a short-lived one.
func (p *Provider) readItems(ctx context.Context, ref string) ([]itemEntry, map[string]*turnError, error) {
	if ref == "" {
		return nil, nil, errors.New("transcript: empty thread id")
	}
	var entries []itemEntry
	var failed map[string]*turnError
	list := func(c *conn) error {
		var err error
		entries, err = listItems(ctx, c, ref)
		if err != nil || len(entries) == 0 {
			// Nothing stored, so no turn to have failed: codex refuses
			// thread/turns/list before a thread's first user message.
			failed = nil
			return err
		}
		failed, err = turnFailures(ctx, c, ref)
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
		return nil, nil, fmt.Errorf("transcript %s: %w", ref, err)
	}
	return entries, failed, nil
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

// turnPageSize is thread/turns/list's page size: turns, not items, so a
// long session still takes few pages.
const turnPageSize = 200

// turnFailures maps each failed turn of ref to its error, from
// thread/turns/list (items not loaded: only status and error are read).
// Captured 2026-10-22 (testdata/captures/auth-*-turns-list.json): a failed
// turn keeps its status and its TurnError, codexErrorInfo included, in the
// rollout, so a fresh app-server reads it back. A codex without
// thread/turns/list yields none: the rows then just lack the failures.
func turnFailures(ctx context.Context, c *conn, ref string) (map[string]*turnError, error) {
	failed := map[string]*turnError{}
	cursor := ""
	for range 10000 {
		params := map[string]any{"threadId": ref, "limit": turnPageSize, "itemsView": "notLoaded"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(ctx, "thread/turns/list", params)
		var re *rpcError
		if errors.As(err, &re) && re.Code == errMethodNotFound {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("thread/turns/list: %w", err)
		}
		var res struct {
			Data []struct {
				ID     string     `json:"id"`
				Status string     `json:"status"`
				Error  *turnError `json:"error"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("thread/turns/list: %w", err)
		}
		for _, t := range res.Data {
			if t.Status == "failed" {
				terr := t.Error
				if terr == nil || terr.Message == "" {
					terr = &turnError{Message: "turn failed"}
				}
				failed[t.ID] = terr
			}
		}
		if res.NextCursor == nil || *res.NextCursor == "" || len(res.Data) == 0 {
			break
		}
		cursor = *res.NextCursor
	}
	return failed, nil
}

// turnErrorKey keys the row standing for a failed turn's error: the turn
// has no item of its own to key it by.
func turnErrorKey(turnID string) string { return "turn-error:" + turnID }

// Every row carries its item's key (itemKey): the same key the live events
// for the item carried. A tool row and its result row share it.
//
// normalizeItems maps thread items onto the normalized transcript, in
// store order: a user message per userMessage, an agent message per
// agentMessage (commentary and final answer alike — each is its own item),
// a tool row per tool item followed by its result once the item finished,
// a compact marker per contextCompaction, and (thinking, the keyed form
// only) a thinking row per reasoning item with summary text. Plan,
// review-mode and hook items are left out. A tool item still in progress — the turn was killed mid-command —
// keeps its tool row and gets no result.
//
// failed (turnFailures) adds, after the last item of each failed turn, an
// agent row marked IsError with the turn's error — the harness speaking in
// the agent's place, as claude-code's API-error lines do — classified
// (ErrorKind) the way the live EventError is. A failed turn with no stored
// item makes no row.
func normalizeItems(entries []itemEntry, failed map[string]*turnError, thinking bool) []provider.Message {
	var msgs []provider.Message
	for i, e := range entries {
		msgs = appendItem(msgs, e.Item, thinking)
		if terr := failed[e.TurnID]; terr != nil && (i+1 == len(entries) || entries[i+1].TurnID != e.TurnID) {
			msgs = append(msgs, provider.Message{
				Role: "agent", Text: terr.Message, IsError: true, ErrorKind: terr.errorKind(), Key: turnErrorKey(e.TurnID),
			})
		}
	}
	return msgs
}

// appendItem appends one item's rows (normalizeItems).
func appendItem(msgs []provider.Message, it threadItem, thinking bool) []provider.Message {
	switch {
	case it.Type == "userMessage":
		if text := it.userText(); text != "" {
			msgs = append(msgs, provider.Message{Role: "user", Text: text, Key: itemKey(&it)})
		}
	case it.Type == "agentMessage":
		if it.Text != "" {
			msgs = append(msgs, provider.Message{Role: "agent", Text: it.Text, Key: it.ID})
		}
	case it.Type == "reasoning":
		if text := it.reasoningText(); thinking && text != "" {
			msgs = append(msgs, provider.Message{Role: "thinking", Text: text, Key: it.ID})
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
			return msgs
		}
		msgs = append(msgs, provider.Message{
			Role:    "tool_result",
			Tool:    &provider.ToolUse{ID: it.ID, Name: tool.Name, Kind: tool.Kind, Status: status},
			Text:    text,
			IsError: isError,
			Key:     it.ID,
		})
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
// the caller reads Transcript instead. Rows are the keyed form, thinking
// included (KeyedTranscript).
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
	// No failed-turn rows (turnFailures): these reads settle the live tail,
	// and a turn's failure was never a live row to settle. Whole reads
	// (Transcript, KeyedTranscript) carry them.
	return normalizeItems(entries, nil, true), &itemsCursor{ref: ref, last: last, anchor: anchor}, nil
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
	if notStoredYet(err) {
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("thread/items/list: %w", err)
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return res, fmt.Errorf("thread/items/list: %w", err)
	}
	return res, nil
}

// notStoredYet: codex 0.154 answers thread/items/list for a thread its
// local thread store doesn't have — a new thread before its first turn is
// written, as well as an unknown id — with -32601 and this exact message
// (probed 2026-10-21 against a fresh thread/start). Nothing is stored yet:
// an empty page, not an error.
func notStoredYet(err error) bool {
	var re *rpcError
	return errors.As(err, &re) && re.Code == errMethodNotFound && re.Message == "thread/items/list is not supported yet"
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
