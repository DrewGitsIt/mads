package hermescli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Conversation keys, emulated (docs/CHAT-STREAM-DESIGN.md §1; README
// "Conversation keys"). hermes gives no id that is the same live and stored:
//
//   - the live messageId (a UUID4 per model call, agent/events.py:185-236)
//     is never stored, and session/load replays chunks with none;
//   - the stored message_uid exists only once its row is written — the
//     assistant row at the end of its model call, or just before its tools
//     run (turn_tool_round.py:131-133) — so no live chunk can carry it;
//   - live tool ids are tc-<hex> (acp_adapter/tools.py:67), stored ones the
//     model's call_… (survey runc1 lines 21-22 vs row 6).
//
// So live events carry provisional keys from the live ids (the engine's
// KeyModeMessageRun: "<messageId>:<run>" for text, tc-… for tools), and this
// binding keeps, per session child, a ledger of what each turn streamed. The
// readers match stored rows to it by position, the way hermes writes them:
// the turn's user row is the Nth stored user row (N counted just before the
// prompt went out, and checked against the text sent); within the turn, the
// k-th stored assistant row with text is the k-th live text item, and the
// stored tool calls, in order across the turn's assistant rows, are the live
// tool calls in order. A row so matched is keyed with the live key while that
// child lives; every other row — and every row once the child is gone
// (restart, no child) — with its own message_uid or stored call id.
//
// Limits, all honest one-sided keys rather than wrong ones:
//   - after the child exits, the same rows switch to their native keys, so
//     a viewer holding live keys from that child must re-read (C2 replaces
//     the tail then anyway);
//   - a turn whose stored user row doesn't read back as the text sent (an
//     unknown hermes rewrite) isn't matched at all;
//   - text items are matched by count, not content: a model call that
//     streams text hermes then doesn't store (or the reverse) shifts the
//     rest of that turn's text keys;
//   - an in-flight turn's stored row that has nothing live to match yet
//     (hermes can write a tool-call row a few ms before the tool_call frame)
//     reads native; TranscriptSince holds such rows back until the turn ends
//     or the live item arrives, so an incremental reader never sees a row's
//     key change.

// ledger is one session child's record of its turns.
type ledger struct {
	mu    sync.Mutex
	turns []*liveTurn
}

// liveTurn is what one prompt of this child streamed.
type liveTurn struct {
	user  int    // the turn's user row, as an index among the stored user rows; -1 unknown
	sent  string // the text mads sent
	texts []string
	tools []string
	open  bool
}

func (l *ledger) begin(user int, sent string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.turns = append(l.turns, &liveTurn{user: user, sent: sent, open: true})
}

// abort drops the turn begin just recorded (its Send failed).
func (l *ledger) abort() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := len(l.turns); n > 0 {
		l.turns = l.turns[:n-1]
	}
}

func (l *ledger) current() *liveTurn {
	if n := len(l.turns); n > 0 {
		return l.turns[n-1]
	}
	return nil
}

func (l *ledger) text(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t := l.current(); t != nil && key != "" && (len(t.texts) == 0 || t.texts[len(t.texts)-1] != key) {
		t.texts = append(t.texts, key)
	}
}

func (l *ledger) tool(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t := l.current(); t != nil && key != "" {
		t.tools = append(t.tools, key)
	}
}

func (l *ledger) end() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t := l.current(); t != nil {
		t.open = false
	}
}

// snapshot copies the turns for a reader.
func (l *ledger) snapshot() []liveTurn {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]liveTurn, len(l.turns))
	for i, t := range l.turns {
		out[i] = *t
		out[i].texts = append([]string(nil), t.texts...)
		out[i].tools = append([]string(nil), t.tools...)
	}
	return out
}

// liveSessions maps a ref to its live child's ledger.
type liveSessions struct {
	mu sync.Mutex
	m  map[string]*ledger
}

func (ls *liveSessions) add(ref string) *ledger {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.m == nil {
		ls.m = map[string]*ledger{}
	}
	l := &ledger{}
	ls.m[ref] = l
	return l
}

// remove forgets ref's ledger if it is still l (a resumed child may have
// replaced it).
func (ls *liveSessions) remove(ref string, l *ledger) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.m[ref] == l {
		delete(ls.m, ref)
	}
}

func (ls *liveSessions) get(ref string) *ledger {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.m[ref]
}

// aliases is the position match for one read: live keys by message_uid
// (text rows) and by stored call id (tool calls and their results), and
// hold, the index of the first row an incremental read must not serve yet.
type aliases struct {
	text map[string]string
	tool map[string]string
	hold int
}

// userRows are the indices of the display rows that are user messages —
// the turn boundaries, counted the same way when a prompt goes out
// (storedUserRows) and when rows are matched.
func userRows(rows []storedRow) []int {
	var idx []int
	for i, r := range rows {
		if r.role == "user" && !r.summary {
			idx = append(idx, i)
		}
	}
	return idx
}

// match computes the aliases of rows against a ledger's turns (nil: none).
func match(rows []storedRow, turns []liveTurn) aliases {
	al := aliases{text: map[string]string{}, tool: map[string]string{}, hold: len(rows)}
	users := userRows(rows)
	for _, t := range turns {
		if t.user < 0 || t.user >= len(users) {
			continue
		}
		ui := users[t.user]
		if userText(rows[ui].content) != strings.TrimSpace(t.sent) {
			continue // not the row this turn wrote
		}
		end := len(rows)
		if t.user+1 < len(users) {
			end = users[t.user+1]
		}
		ti, tj := 0, 0
		for k := ui + 1; k < end; k++ {
			r := rows[k]
			text := decodeContent(r.content)
			if r.role != "assistant" || r.summary || failedTurn(r.displayKind, text) {
				continue
			}
			unmatched := false
			if text != "" {
				if ti < len(t.texts) {
					al.text[r.uid] = t.texts[ti]
					ti++
				} else {
					unmatched = true
				}
			}
			for _, c := range toolCallMessages(r.toolCalls, time.Time{}) {
				if tj < len(t.tools) {
					al.tool[c.Tool.ID] = t.tools[tj]
					tj++
				} else {
					unmatched = true
				}
			}
			if unmatched && t.open && k < al.hold {
				al.hold = k
			}
		}
	}
	return al
}

// aliasesFor is the match against ref's live child, if it has one.
func (p *Provider) aliasesFor(ref string, rows []storedRow) aliases {
	var turns []liveTurn
	if l := p.live.get(ref); l != nil {
		turns = l.snapshot()
	}
	return match(rows, turns)
}

// storedUserRows counts ref's stored user rows: the index the next prompt's
// row will have. A session hermes hasn't stored yet has none.
func (p *Provider) storedUserRows(ctx context.Context, ref string) (int, error) {
	db, _, err := p.openStore()
	if errors.Is(err, errNoStore) {
		return 0, nil
	}
	if err != nil {
		return -1, err
	}
	defer func() { _ = db.Close() }()
	rows, err := readDisplayRows(ctx, db, ref)
	if errors.Is(err, errSessionNotFound) {
		return 0, nil
	}
	if err != nil {
		return -1, err
	}
	return len(userRows(rows)), nil
}

// storedToolResult waits briefly for the stored result of the live tool
// call liveKey and returns its text. hermes renders a tool result for the
// ACP stream (acp_adapter/tools.py build_tool_complete: "terminal
// result\n- **output:** …") but stores the tool's own output (the JSON
// envelope, runc1 row 7), and passes the stored text on the wire only for
// unstructured results (raw_output) — so the live text can't be turned into
// the stored one, only replaced by it. The tool row lands 3-23 ms after the
// completed frame (survey).
func (p *Provider) storedToolResult(ctx context.Context, ref, liveKey string, wait time.Duration) (string, bool) {
	deadline := time.Now().Add(wait)
	for {
		if text, ok := p.lookupToolResult(ctx, ref, liveKey); ok {
			return text, true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return "", false
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func (p *Provider) lookupToolResult(ctx context.Context, ref, liveKey string) (string, bool) {
	db, _, err := p.openStore()
	if err != nil {
		return "", false
	}
	defer func() { _ = db.Close() }()
	rows, err := readDisplayRows(ctx, db, ref)
	if err != nil {
		return "", false
	}
	al := p.aliasesFor(ref, rows)
	for _, r := range rows {
		if r.role == "tool" && al.tool[r.toolCallID] == liveKey {
			return decodeContent(r.content), true
		}
	}
	return "", false
}

// rowKey is a stored row's own key: its message_uid, or (rows written
// before hermes had uids) its row id, which a load rewrites.
func rowKey(r storedRow) string {
	if r.uid != "" {
		return r.uid
	}
	return fmt.Sprintf("row-%d", r.id)
}

// hermesCursor is TranscriptSince's position: the message_uid of the last
// row served (row ids are rewritten on load), and how many rows had been
// served, for a row without a uid.
type hermesCursor struct {
	uid string
	n   int
}

// cursorIndex finds where a cursor's row now is in rows; -1 = gone.
func cursorIndex(rows []storedRow, c hermesCursor) int {
	if c.uid == "" {
		if c.n <= len(rows) {
			return c.n - 1
		}
		return -1
	}
	for i, r := range rows {
		if r.uid == c.uid {
			return i
		}
	}
	return -1
}
