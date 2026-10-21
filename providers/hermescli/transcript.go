package hermescli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the repo's CGO-free driver (services/data/internal/store)

	"github.com/drewkidwell/mads/providers/provider"
)

// The store is hermes's own $HERMES_HOME/state.db (SQLite, WAL), written by
// the hermes-acp process. It is NOT append-only: session/load deletes and
// reinserts a session's active rows (fresh row ids, same message_uid —
// acp_adapter/session.py:376-398, survey run3), compaction archives rows
// (active=0, compacted=1) and clones its protected tail
// (hermes_state_messages.py:919-1070), and a compression can continue the
// conversation in a child session (parent ended "compression"). The reader
// therefore reproduces hermes's own display projection rather than reading
// rows in id order:
//
//   - a conversation is the ACP session id's compression lineage, root to
//     tip (hermes_state_compression.py:798-827);
//   - display rows are active OR compacted, minus model-only rows
//     (DISPLAY_VISIBLE_SQL, hermes_state_messages.py:57-58);
//   - each logical message appears once: generations sharing a
//     message_uid (clones copy every column but the id) or hermes's
//     display identity (role, content, timestamp, tool ids) collapse to the
//     live/newest row, placed at its first row's position
//     (_dedupe_display_generations, hermes_state_messages.py:1206-1222).

// errNoStore marks a missing state.db — the home has never run a session.
var errNoStore = errors.New("no hermes state.db")

// errSessionNotFound: state.db has no sessions row for the ref (hermes
// writes it with the first prompt).
var errSessionNotFound = errors.New("session not found")

// openStore opens state.db read-only. mode=ro (not immutable): hermes-acp
// may be writing it right now, and the WAL must be read to see its latest
// rows. busy_timeout rides out a checkpoint in progress.
func (p *Provider) openStore() (*sql.DB, string, error) {
	path := filepath.Join(p.home(), "state.db")
	if _, err := os.Stat(path); err != nil {
		return nil, path, fmt.Errorf("%w at %s", errNoStore, path)
	}
	dsn := "file:" + url.PathEscape(path) + "?mode=ro&_pragma=busy_timeout(2000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, path, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	return db, path, nil
}

// requireStoredSession is the resume guard. hermes answers session/load of
// an id it doesn't know with an empty success (load_session returns None →
// result {}), and the next prompt then resolves stopReason "refusal" (probe
// 2026-10-20, testdata/captures/probe-loadunknown.wire.jsonl) — the
// engine would report a resume that never happened. hermes writes the
// sessions row with the first prompt, so a ref with no row has no
// conversation to resume either way.
func (p *Provider) requireStoredSession(ctx context.Context, ref string) error {
	db, path, err := p.openStore()
	if err != nil {
		return fmt.Errorf("hermes: cannot resume session %s: %w", ref, err)
	}
	defer func() { _ = db.Close() }()
	var one int
	switch err := db.QueryRowContext(ctx, "SELECT 1 FROM sessions WHERE id = ?", ref).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("hermes: session %s not found in %s", ref, path)
	case err != nil:
		return fmt.Errorf("hermes: read %s: %w", path, err)
	}
	return nil
}

// Transcript implements provider.TranscriptReader over state.db, keyed:
// rows this binding's live child matched to what it streamed carry the live
// keys, the rest their own (keys.go). While that child lives, the in-flight
// turn's rows from the first one with nothing live to match yet onward are
// held back, exactly as TranscriptSince holds them: served now under their
// own keys, they would be re-keyed a moment later when the live item
// arrives, and a reader would hold the same row under two keys.
func (p *Provider) Transcript(ctx context.Context, ref string) ([]provider.Message, error) {
	return p.transcript(ctx, ref, false)
}

// KeyedTranscript implements provider.KeyedTranscriptReader: Transcript
// plus a Role "thinking" row before each assistant row's text for the
// reasoning hermes stored with it (reasoning_content, else reasoning: the
// first non-blank, as hermes's own replay reads them), keyed like text —
// the live thinking item's key while this child lives and its thinking was
// matched by position, else "<message_uid>:thinking".
func (p *Provider) KeyedTranscript(ctx context.Context, ref string) ([]provider.Message, error) {
	return p.transcript(ctx, ref, true)
}

func (p *Provider) transcript(ctx context.Context, ref string, thinking bool) ([]provider.Message, error) {
	rows, err := p.readRows(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	al := p.aliasesFor(ref, rows, thinking)
	if al.hold < len(rows) {
		rows = rows[:al.hold]
	}
	return normalizeRows(rows, al), nil
}

// TranscriptSince implements provider.IncrementalTranscriptReader. Row ids
// are no cursor — hermes rewrites a session's rows with fresh ids on every
// load — so the cursor is the message_uid of the last row served, and the
// answer is the display rows after that uid's current row. A uid that's
// gone (compaction) is provider.ErrCursorStale. Rows of the turn in flight
// that have nothing live to match yet are held back (keys.go). Rows are
// the keyed form, thinking included (KeyedTranscript).
func (p *Provider) TranscriptSince(ctx context.Context, ref string, cursor provider.TranscriptCursor) ([]provider.Message, provider.TranscriptCursor, error) {
	rows, err := p.readRows(ctx, ref)
	if err != nil {
		return nil, cursor, fmt.Errorf("transcript since: %w", err)
	}
	from := 0
	if c, ok := cursor.(hermesCursor); ok {
		i := cursorIndex(rows, c)
		if i < 0 {
			return nil, cursor, fmt.Errorf("hermes session %s no longer has row %q: %w", ref, c.uid, provider.ErrCursorStale)
		}
		from = i + 1
	}
	al := p.aliasesFor(ref, rows, true)
	to := len(rows)
	if al.hold < to {
		to = al.hold
	}
	if to <= from {
		return nil, cursor, nil
	}
	served := rows[from:to]
	last := served[len(served)-1]
	return normalizeRows(served, al), hermesCursor{uid: last.uid, n: to}, nil
}

// readRows is the display projection of ref's rows.
func (p *Provider) readRows(ctx context.Context, ref string) ([]storedRow, error) {
	if ref == "" {
		return nil, fmt.Errorf("empty session ref")
	}
	db, path, err := p.openStore()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := readDisplayRows(ctx, db, ref)
	if err != nil {
		return nil, fmt.Errorf("%s in %s: %w", ref, path, err)
	}
	return rows, nil
}

// storedSession is the slice of a sessions row the lineage walk needs.
type storedSession struct {
	id, source, parent, endReason, modelConfig string
}

func scanSession(row interface{ Scan(...any) error }) (storedSession, error) {
	var s storedSession
	var parent, endReason, modelConfig sql.NullString
	err := row.Scan(&s.id, &s.source, &parent, &endReason, &modelConfig)
	s.parent, s.endReason, s.modelConfig = parent.String, endReason.String, modelConfig.String
	return s, err
}

const sessionCols = "id, source, parent_session_id, end_reason, model_config"

// lineage walks forward from the ACP session id (the root: hermes keys the
// root hermes session by the ACP id, survey runc1 sessionProvenance) through
// each compression continuation, mirroring get_compression_lineage: follow
// the earliest child of a parent that ended "compression", skipping tool
// children and explicit branch/delegate/reset forks.
func lineage(ctx context.Context, db *sql.DB, ref string) ([]string, error) {
	cur, err := scanSession(db.QueryRowContext(ctx, "SELECT "+sessionCols+" FROM sessions WHERE id = ?", ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	chain := []string{cur.id}
	seen := map[string]bool{cur.id: true}
	for cur.endReason == "compression" && len(chain) < 100 {
		rows, err := db.QueryContext(ctx, "SELECT "+sessionCols+" FROM sessions WHERE parent_session_id = ? ORDER BY started_at ASC", cur.id)
		if err != nil {
			return nil, err
		}
		var next *storedSession
		for rows.Next() {
			child, err := scanSession(rows)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			if next == nil && isContinuation(child) {
				c := child
				next = &c
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if next == nil || seen[next.id] {
			break
		}
		chain = append(chain, next.id)
		seen[next.id] = true
		cur = *next
	}
	return chain, nil
}

// isContinuation is _is_compression_child_row minus the parent check the
// caller already made: not a tool child, and no _branched_from /
// _delegate_from / _reset_from marker naming the parent
// (hermes_state_messages.py:1908-1927).
func isContinuation(s storedSession) bool {
	if s.source == "tool" {
		return false
	}
	var cfg map[string]any
	if json.Unmarshal([]byte(s.modelConfig), &cfg) != nil {
		return true
	}
	for _, k := range []string{"_branched_from", "_delegate_from", "_reset_from"} {
		if v, ok := cfg[k].(string); ok && v == s.parent {
			return false
		}
	}
	return true
}

// storedRow is one messages row, as much as normalization reads.
type storedRow struct {
	id                                      int64
	role, content, toolCalls, toolCallID    string
	toolName, displayKind, displayMeta, uid string
	// reasoning and reasoningContent: an assistant row's reasoning columns
	// (empty where the schema has none).
	reasoning, reasoningContent string
	timestamp                   float64
	active, summary             bool
}

// thinking is the row's stored reasoning as hermes replays it
// (_history_reasoning_text, acp_adapter/server.py): the first of
// reasoning_content and reasoning that isn't blank — a lone " " is the pad
// hermes writes for thinking-mode providers, not reasoning. Unlike the
// replay it isn't trimmed: reasoning_content is the streamed reasoning
// deltas joined ("".join(reasoning_parts), chat_completion_helpers.py), so
// as stored it equals the live thought chunks.
func (r storedRow) thinking() string {
	for _, s := range []string{r.reasoningContent, r.reasoning} {
		if t := decodeContent(s); strings.TrimSpace(t) != "" {
			return t
		}
	}
	return ""
}

func readDisplayRows(ctx context.Context, db *sql.DB, ref string) ([]storedRow, error) {
	ids, err := lineage(ctx, db, ref)
	if err != nil {
		return nil, err
	}
	reasoningCols := "NULL, NULL"
	if hasColumns(ctx, db, "messages", "reasoning", "reasoning_content") {
		reasoningCols = "reasoning, reasoning_content"
	}
	var all []storedRow
	for _, id := range ids {
		rows, err := db.QueryContext(ctx, `SELECT id, role, content, tool_calls, tool_call_id, tool_name,
			display_kind, display_metadata, message_uid, timestamp, active, _compressed_summary, `+reasoningCols+`
			FROM messages WHERE session_id = ? AND (active = 1 OR compacted = 1) ORDER BY id`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r storedRow
			var content, toolCalls, toolCallID, toolName, kind, meta, uid, reasoning, reasoningContent sql.NullString
			if err := rows.Scan(&r.id, &r.role, &content, &toolCalls, &toolCallID, &toolName,
				&kind, &meta, &uid, &r.timestamp, &r.active, &r.summary, &reasoning, &reasoningContent); err != nil {
				_ = rows.Close()
				return nil, err
			}
			r.content, r.toolCalls, r.toolCallID, r.toolName = content.String, toolCalls.String, toolCallID.String, toolName.String
			r.displayKind, r.displayMeta, r.uid = kind.String, meta.String, uid.String
			r.reasoning, r.reasoningContent = reasoning.String, reasoningContent.String
			if !modelOnly(r.displayMeta) {
				all = append(all, r)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return dedupeGenerations(all), nil
}

// hasColumns reports whether table has every one of cols (a hermes schema
// without them reads them as NULL rather than failing the whole read).
func hasColumns(ctx context.Context, db *sql.DB, table string, cols ...string) bool {
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			have[name] = true
		}
	}
	for _, c := range cols {
		if !have[c] {
			return false
		}
	}
	return true
}

// modelOnly is DISPLAY_VISIBLE_SQL's test: display_metadata.model_only
// truthy (agent/context_compressor.py:303) keeps a row out of every
// display projection.
func modelOnly(meta string) bool {
	if meta == "" {
		return false
	}
	var m map[string]any
	if json.Unmarshal([]byte(meta), &m) != nil {
		return false
	}
	switch v := m["model_only"].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	}
	return false
}

// dedupeGenerations collapses compaction/load generations of one logical
// message: rows join a group by message_uid or by hermes's display
// identity; the group is represented by its active-then-newest row and
// ordered by its first row id. rows must be in id order.
func dedupeGenerations(rows []storedRow) []storedRow {
	type group struct {
		first int64
		rep   storedRow
	}
	var groups []*group
	byUID := map[string]*group{}
	byKey := map[string]*group{}
	for _, r := range rows {
		key := fmt.Sprintf("%s\x00%s\x00%v\x00%s\x00%s\x00%s", r.role, r.content, r.timestamp, r.toolCallID, r.toolCalls, r.toolName)
		g := byUID[r.uid]
		if r.uid == "" {
			g = nil
		}
		if g == nil {
			g = byKey[key]
		}
		if g == nil {
			g = &group{first: r.id, rep: r}
			groups = append(groups, g)
		} else if r.active || !g.rep.active {
			g.rep = r // live beats archived; among equals the newest wins
		}
		if r.uid != "" {
			byUID[r.uid] = g
		}
		byKey[key] = g
	}
	out := make([]storedRow, len(groups))
	for i, g := range groups {
		out[i] = g.rep
	}
	return out
}

// Presentation rules (design doc "Transcript"):
const (
	// correctionMarker joins an interrupted prompt to the user's next one
	// (_attach_interrupted_prompt, acp_adapter/server.py:201-202). The
	// stored row repeats the cancelled prompt — already its own earlier
	// row — so the user message shown is what follows the LAST marker.
	correctionMarker = "\n\nUser correction/guidance after interrupt: "
	// failedTurnKind and the two notices are hermes's boundary row for a
	// turn that ended without a reply (agent/turn_failure_copy.py:39-48);
	// rows written before hermes typed them carry only the exact text.
	failedTurnKind      = "failed_turn"
	failedTurnNotice    = "Your request was not processed. Send it again if you still want me to carry it out."
	partialFailedNotice = "This turn did not complete. Some actions may already have run; verify their effects before resending."
	// contentJSONPrefix marks structured (multimodal) content
	// (hermes_state.py:1613, _encode_content).
	contentJSONPrefix = "\x00json:"
)

// normalizeRows maps display rows to Messages, keyed (keys.go): user rows
// "user:"+message_uid; assistant text by its live key when al matched it,
// else its message_uid; tool calls and results by the live tool key when
// matched, else the stored call id. In the keyed form (al.thinking) an
// assistant row's reasoning comes first as a thinking row, keyed by its
// matched live key, else "<message_uid>:thinking".
func normalizeRows(rows []storedRow, al aliases) []provider.Message {
	var msgs []provider.Message
	for _, r := range rows {
		ts := unixFloat(r.timestamp)
		text := decodeContent(r.content)
		key := rowKey(r)
		if r.summary {
			// A compaction carrier: the summary hermes carried forward.
			msgs = append(msgs, provider.Message{Role: "compact", Text: text, Compact: &provider.CompactMarker{}, Timestamp: ts, Key: key})
			continue
		}
		switch r.role {
		case "user":
			if text = userText(r.content); text != "" {
				msgs = append(msgs, provider.Message{Role: "user", Text: text, Timestamp: ts, Key: "user:" + key})
			}
		case "assistant":
			if failedTurn(r.displayKind, text) {
				// Shown as the harness reporting a failed turn in the agent's
				// place (Message.IsError), with hermes's own words: the model
				// sees this row on its next turn (survey run3), so hiding it
				// would make the conversation read differently from what the
				// agent knows. Never streamed: a notice, keyed by its row.
				msgs = append(msgs, provider.Message{Role: "agent", Text: strings.TrimSpace(text), IsError: true, Notice: true, Timestamp: ts, Key: key})
				continue
			}
			if thought := r.thinking(); al.thinking && thought != "" {
				tkey := key + ":thinking"
				if live, ok := al.thought[r.uid]; ok {
					tkey = live
				}
				msgs = append(msgs, provider.Message{Role: "thinking", Text: thought, Timestamp: ts, Key: tkey})
			}
			if text != "" {
				if live, ok := al.text[r.uid]; ok {
					key = live
				}
				msgs = append(msgs, provider.Message{Role: "agent", Text: text, Timestamp: ts, Key: key})
			}
			for _, m := range toolCallMessages(r.toolCalls, ts) {
				m.Key = m.Tool.ID
				if live, ok := al.tool[m.Tool.ID]; ok {
					m.Key = live
				}
				msgs = append(msgs, m)
			}
		case "tool":
			key := r.toolCallID
			if live, ok := al.tool[r.toolCallID]; ok {
				key = live
			}
			msgs = append(msgs, provider.Message{
				Role:      "tool_result",
				Tool:      &provider.ToolUse{ID: r.toolCallID, Name: r.toolName},
				Text:      text,
				IsError:   toolResultFailed(text),
				Timestamp: ts,
				Key:       key,
			})
		}
		// system and anything else: not conversation.
	}
	return msgs
}

// userText is what the user typed, from a stored user row: without the
// persona the adapter prefixed to a process's first prompt
// (wrapSystemPrompt), and after the last interrupt-correction marker.
func userText(content string) string {
	text := stripSystemPrompt(decodeContent(content))
	if i := strings.LastIndex(text, correctionMarker); i >= 0 {
		text = text[i+len(correctionMarker):]
	}
	return text
}

func failedTurn(kind, text string) bool {
	if kind == failedTurnKind {
		return true
	}
	t := strings.TrimSpace(text)
	return kind == "" && (t == failedTurnNotice || t == partialFailedNotice)
}

// decodeContent reverses _encode_content: plain strings as stored,
// "\x00json:"-prefixed JSON as its text parts (a JSON string decodes to
// itself; image parts are dropped — Message carries text).
func decodeContent(content string) string {
	raw, ok := strings.CutPrefix(content, contentJSONPrefix)
	if !ok {
		return content
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return content // fail closed, like hermes: the raw string
	}
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, part := range c {
			switch pt := part.(type) {
			case string:
				parts = append(parts, pt)
			case map[string]any:
				if s, ok := pt["text"].(string); ok && s != "" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if s, ok := c["text"].(string); ok {
			return s
		}
	}
	return ""
}

// toolCallMessages expands an assistant row's OpenAI-shaped tool_calls
// ([{id, type:"function", function:{name, arguments:"<json>"}}], survey
// runc1 row 6) into one "tool" message each. The ids are the stored call_…
// ids, which tool rows' tool_call_id match — not the live tc-… ids
// (FINDINGS §2).
func toolCallMessages(raw string, ts time.Time) []provider.Message {
	if raw == "" {
		return nil
	}
	var calls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal([]byte(raw), &calls) != nil {
		return nil
	}
	var msgs []provider.Message
	for _, c := range calls {
		var input json.RawMessage
		if json.Valid([]byte(c.Function.Arguments)) {
			input = json.RawMessage(c.Function.Arguments)
		} else if c.Function.Arguments != "" {
			input, _ = json.Marshal(c.Function.Arguments)
		}
		msgs = append(msgs, provider.Message{
			Role:      "tool",
			Tool:      &provider.ToolUse{ID: c.ID, Name: c.Function.Name, Input: input},
			Timestamp: ts,
		})
	}
	return msgs
}

// toolResultFailed reads hermes's JSON tool result envelope ({"output",
// "exit_code", "error"}, survey runc1 row 7): a non-empty "error" string is
// a failed call. Anything else — including a non-zero exit code, which the
// model sees and handles as output — is a result.
func toolResultFailed(text string) bool {
	var env map[string]any
	if json.Unmarshal([]byte(text), &env) != nil {
		return false
	}
	s, ok := env["error"].(string)
	return ok && strings.TrimSpace(s) != ""
}

// unixFloat converts hermes's REAL unix-seconds timestamp to UTC time.
func unixFloat(sec float64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	whole, frac := math.Modf(sec)
	return time.Unix(int64(whole), int64(frac*1e9)).UTC()
}
