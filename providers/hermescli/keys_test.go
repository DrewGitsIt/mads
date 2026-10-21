package hermescli

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

func keysOf(msgs []provider.Message) string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Role+" "+m.Key)
	}
	return strings.Join(out, "\n")
}

// A synthetic turn shaped like hermes's: a text+tool_calls row (two
// parallel calls), their tool rows, a tool-only row, its tool row, a final
// text row, then the next turn's user row.
func positionRows() []storedRow {
	two := `[{"id":"call_a","function":{"name":"terminal","arguments":"{}"}},{"id":"call_b","function":{"name":"read_file","arguments":"{}"}}]`
	one := `[{"id":"call_c","function":{"name":"terminal","arguments":"{}"}}]`
	return []storedRow{
		{id: 1, role: "user", content: "go", uid: "u1"},
		{id: 2, role: "assistant", content: "First.", toolCalls: two, uid: "a1"},
		{id: 3, role: "tool", content: `{"output":"x"}`, toolCallID: "call_a", toolName: "terminal", uid: "t1"},
		{id: 4, role: "tool", content: `{"content":"y"}`, toolCallID: "call_b", toolName: "read_file", uid: "t2"},
		{id: 5, role: "assistant", content: "", toolCalls: one, uid: "a2"},
		{id: 6, role: "tool", content: `{"output":"z"}`, toolCallID: "call_c", toolName: "terminal", uid: "t3"},
		{id: 7, role: "assistant", content: "Done.", uid: "a3"},
		{id: 8, role: "user", content: "next", uid: "u2"},
	}
}

// The Nth model call's text row takes the Nth live text key, and the stored
// tool calls, in order across the turn's rows, take the live tool calls in
// order — tool-only rows and parallel calls included. Results follow their
// call's key.
func TestPositionAliasing(t *testing.T) {
	rows := positionRows()
	turn := liveTurn{user: 0, sent: " go ", texts: []string{"m1:0", "m3:0"}, tools: []string{"tc-1", "tc-2", "tc-3"}}
	got := keysOf(normalizeRows(rows, match(rows, []liveTurn{turn})))
	want := strings.Join([]string{
		"user user:u1",
		"agent m1:0", "tool tc-1", "tool tc-2",
		"tool_result tc-1", "tool_result tc-2",
		"tool tc-3", "tool_result tc-3",
		"agent m3:0",
		"user user:u2",
	}, "\n")
	if got != want {
		t.Fatalf("keys:\n%s\nwant:\n%s", got, want)
	}

	// No live child (or a turn whose user row isn't the text sent): every
	// row keyed natively.
	native := strings.Join([]string{
		"user user:u1",
		"agent a1", "tool call_a", "tool call_b",
		"tool_result call_a", "tool_result call_b",
		"tool call_c", "tool_result call_c",
		"agent a3",
		"user user:u2",
	}, "\n")
	if got := keysOf(normalizeRows(rows, match(rows, nil))); got != native {
		t.Fatalf("without a ledger:\n%s\nwant:\n%s", got, native)
	}
	turn.sent = "something else"
	if got := keysOf(normalizeRows(rows, match(rows, []liveTurn{turn}))); got != native {
		t.Fatalf("user row not the sent text:\n%s\nwant native keys", got)
	}
}

// A stored row the in-flight turn has nothing live for yet (hermes writes a
// tool-call row a few ms before the tool_call frame) is held back from
// incremental reads; once the turn ends it reads native and isn't held.
func TestPositionHoldBack(t *testing.T) {
	rows := positionRows()[:4]
	open := liveTurn{user: 0, sent: "go", texts: []string{"m1:0"}, tools: []string{"tc-1"}, open: true}
	if al := match(rows, []liveTurn{open}); al.hold != 1 {
		t.Fatalf("hold = %d, want 1 (the row with an unannounced second call)", al.hold)
	}
	open.open = false
	if al := match(rows, []liveTurn{open}); al.hold != len(rows) {
		t.Fatalf("closed turn hold = %d, want none", al.hold)
	}
}

// failed_turn rows (cancelled before any output, survey run2 row 20) are
// notices: never streamed, keyed by their own uid.
func TestFailedTurnIsNotice(t *testing.T) {
	p := buildStore(t, []fixtureSession{{id: surveySession, started: 1}}, surveyRows())
	var found bool
	for _, m := range transcriptOf(t, p, surveySession) {
		if strings.HasPrefix(m.Text, "Your request was not processed") {
			found = true
			if !m.Notice || !m.IsError || m.Key != "bab173e5aa" {
				t.Errorf("failed turn = %+v, want Notice+IsError keyed by its uid", m)
			}
		} else if m.Notice {
			t.Errorf("%s row %q marked Notice", m.Role, m.Text)
		}
	}
	if !found {
		t.Fatal("no failed-turn row")
	}
}

// openFixtureStore reopens a buildStore home's state.db for writing.
func openFixtureStore(t *testing.T, p *Provider) *sql.DB {
	t.Helper()
	db, err := openTestStore(filepath.Join(p.cfg.Home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The cursor is a message_uid, not a row id: hermes's load rewrite (insert
// the active rows again with fresh ids, then delete the old ones — survey
// run2.db.jsonl) leaves it pointing at the same message, and chained reads
// still add up to Transcript. A uid that's gone (compaction) is
// ErrCursorStale.
func TestTranscriptSinceAcrossRewriteAndCompaction(t *testing.T) {
	all := surveyRows()
	p := buildStore(t, []fixtureSession{{id: surveySession, started: 1}}, all[:6])
	ctx := context.Background()
	chained, cursor, err := p.TranscriptSince(ctx, surveySession, nil)
	if err != nil {
		t.Fatal(err)
	}

	db := openFixtureStore(t, p)
	// The load rewrite, then the next turns.
	var maxID int64
	if err := db.QueryRow("SELECT MAX(id) FROM messages").Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, tool_name, display_kind,
		display_metadata, message_uid, timestamp, active) SELECT session_id, role, content, tool_calls, tool_call_id, tool_name,
		display_kind, display_metadata, message_uid, timestamp, active FROM messages ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM messages WHERE id <= ?", maxID); err != nil {
		t.Fatal(err)
	}
	for _, r := range all[6:] {
		if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, display_kind, display_metadata, message_uid, timestamp, active)
			VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, 1)`, r.session, r.role, r.content, r.displayKind, r.displayMeta, r.uid, r.ts); err != nil {
			t.Fatal(err)
		}
	}
	more, cursor, err := p.TranscriptSince(ctx, surveySession, cursor)
	if err != nil {
		t.Fatalf("after the rewrite: %v", err)
	}
	chained = append(chained, more...)
	if got, want := keysOf(chained), keysOf(transcriptOf(t, p, surveySession)); got != want {
		t.Fatalf("chained:\n%s\nTranscript:\n%s", got, want)
	}
	if again, _, err := p.TranscriptSince(ctx, surveySession, cursor); err != nil || len(again) != 0 {
		t.Fatalf("caught up: %d rows, %v", len(again), err)
	}

	// Compaction drops the cursor's row from the projection.
	last := cursor.(hermesCursor).uid
	if _, err := db.Exec("DELETE FROM messages WHERE message_uid = ?", last); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.TranscriptSince(ctx, surveySession, cursor); !errors.Is(err, provider.ErrCursorStale) {
		t.Fatalf("cursor row gone: %v, want ErrCursorStale", err)
	}
}

// The copilot run (survey runc1, committed under testdata/captures) replayed
// through the binding: hermes's own wire lines, and its state.db rows
// inserted when the capture saw them. Every streamed item comes back from
// the store under its live key with its live text — the "\n\n" after the
// tool stripped, the tool result replaced by what hermes stored — and the
// same rows read with their own uids once the child is gone.
func TestReplaySurveyKeys(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := filepath.Abs(filepath.Join("testdata", "captures", "runc1"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(Config{Bin: exe, Home: t.TempDir(), Env: []string{"MADS_FAKE_HERMES=1", "MADS_FAKE_HERMES_REPLAY=" + prefix}})
	sess := startSession(t, p, provider.StartOptions{Keyed: true})
	prompts := []string{
		"Reply with exactly one line: Hello from hermes.",
		"Do this in order: first write one short sentence saying you will list the directory. Then call your terminal tool to run: ls. After the tool returns, write one short sentence about what files you found.",
		"Without using any tools, write a short answer about making tea: start with a markdown heading '## Plan', then one short paragraph, then a blank line, then a 3-item bullet list.",
		"Write the numbers from 1 to 400, one per line, no other text.",
		"In one short sentence: what did my previous message ask you to do?",
	}
	type item struct{ role, key, text string }
	var live []item
	var cursor provider.TranscriptCursor
	var chained []provider.Message
	settles := 0
	for i, prompt := range prompts {
		if err := sess.Send(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			if err := sess.Interrupt(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		for _, ev := range drainTurnWithin(t, sess, 15*time.Second) {
			role := map[provider.EventKind]string{provider.EventText: "agent", provider.EventToolUse: "tool", provider.EventToolResult: "tool_result"}[ev.Kind]
			if ev.Kind == provider.EventSettle {
				settles++
			}
			if role == "" {
				continue
			}
			if n := len(live); n > 0 && live[n-1].role == role && live[n-1].key == ev.Key {
				live[n-1].text += ev.Text
				continue
			}
			live = append(live, item{role, ev.Key, ev.Text})
		}
		rows, next, err := p.TranscriptSince(context.Background(), sess.Ref(), cursor)
		if err != nil {
			t.Fatalf("TranscriptSince after turn %d: %v", i, err)
		}
		chained, cursor = append(chained, rows...), next
	}
	if settles < len(prompts)+1 {
		t.Errorf("%d settle hints, want one per prompt response plus the tool call", settles)
	}

	if len(live) != 7 { // 5 model replies (turn 2 tool-first, then text), the tool call, its result
		t.Fatalf("live items %+v, want the capture's 7", live)
	}
	stored := transcriptOf(t, p, sess.Ref())
	if got, want := keysOf(chained), keysOf(stored); got != want {
		t.Fatalf("chained TranscriptSince:\n%s\n!= Transcript:\n%s", got, want)
	}
	byKey := map[string]provider.Message{}
	for _, m := range stored {
		byKey[m.Role+" "+m.Key] = m
	}
	for _, it := range live {
		m, ok := byKey[it.role+" "+it.key]
		if !ok {
			t.Errorf("streamed %s %q not stored under its key; stored:\n%s", it.role, it.key, keysOf(stored))
			continue
		}
		if it.role != "tool" && m.Text != it.text {
			t.Errorf("%s %q: streamed %.80q, stored %.80q", it.role, it.key, it.text, m.Text)
		}
	}
	if m := byKey["tool_result tc-e6e85af3f45d"]; m.Tool == nil || m.Tool.ID != "call_vUPzt6Ph1NWbTkTHFxaLsssR" {
		t.Errorf("live tool tc-e6e85af3f45d not matched to the stored call_vUPzt6Ph1NWbTkTHFxaLsssR: %+v", m)
	}
	if _, ok := byKey["user user:7278ac40edf248708c37803486abd6e2"]; !ok {
		t.Errorf("user row not keyed by its message_uid:\n%s", keysOf(stored))
	}

	// The child gone, the same rows read with hermes's own ids.
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	after := keysOf(transcriptOf(t, p, sess.Ref()))
	for _, want := range []string{"agent 83fc177cefa24b6e8475ca8da6ca7167", "tool call_vUPzt6Ph1NWbTkTHFxaLsssR", "tool_result call_vUPzt6Ph1NWbTkTHFxaLsssR"} {
		if !strings.Contains(after, want) {
			t.Errorf("after Close, no %q in:\n%s", want, after)
		}
	}
}

// Without the keyed opt-in (StartOptions.Keyed) a hermes session
// streams exactly as before C1: a tool keeps the engine's title as its
// name, and its result keeps the live text hermes streamed, not the row it
// stored. Only a keyed consumer gets both swapped for the stored ones.
func TestUnkeyedSessionStreamsAsBefore(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := filepath.Abs(filepath.Join("testdata", "captures", "runc1"))
	if err != nil {
		t.Fatal(err)
	}
	toolTurn := func(keyed bool) (name, title, result string) {
		p := New(Config{Bin: exe, Home: t.TempDir(), Env: []string{"MADS_FAKE_HERMES=1", "MADS_FAKE_HERMES_REPLAY=" + prefix}})
		sess := startSession(t, p, provider.StartOptions{Keyed: keyed})
		defer func() { _ = sess.Close() }()
		for i, prompt := range []string{ // the capture's own prompts: the stand-in stores them as sent
			"Reply with exactly one line: Hello from hermes.",
			"Do this in order: first write one short sentence saying you will list the directory. Then call your terminal tool to run: ls. After the tool returns, write one short sentence about what files you found.",
		} {
			if err := sess.Send(context.Background(), prompt); err != nil {
				t.Fatal(err)
			}
			for _, ev := range drainTurnWithin(t, sess, 15*time.Second) {
				if i == 0 {
					continue
				}
				switch ev.Kind {
				case provider.EventToolUse:
					name, title = ev.Tool.Name, ev.Tool.Title
				case provider.EventToolResult:
					result += ev.Text
				case provider.EventSettle:
					if !keyed {
						t.Fatal("EventSettle without Keyed")
					}
				}
			}
		}
		return name, title, result
	}
	name, title, result := toolTurn(false)
	kName, _, kResult := toolTurn(true)
	if name != title || !strings.Contains(name, ": ") {
		t.Errorf("unkeyed tool name %q, want the engine's title %q as before", name, title)
	}
	if kName == name || strings.Contains(kName, ": ") {
		t.Errorf("keyed tool name %q, want the stored name cut from %q", kName, name)
	}
	if result == kResult {
		t.Errorf("unkeyed and keyed results are both %.60q: the capture can't tell the swap apart", result)
	}
}

// hermes writes a turn's tool-call row a few ms before the tool_call frame.
// A full Transcript read in that gap must not serve the row under its own
// call id, because once the frame arrives the same row reads under the live
// key; no stored item may ever appear under two keys while the child lives.
func TestFullTranscriptHoldsBackTheGap(t *testing.T) {
	rows := surveyRows()[:5] // turn 1; turn 2's user row, tool-call row, tool row
	p := buildStore(t, []fixtureSession{{id: surveySession, started: 1}}, rows)
	led := p.live.add(surveySession)
	led.begin(1, rows[2].content) // turn 2 is in flight; no live item yet

	gap := transcriptOf(t, p, surveySession)
	if got := keysOf(gap); strings.Contains(got, "call_vUPzt6Ph1NWbTkTHFxaLsssR") {
		t.Fatalf("a full read in the gap served the tool-call row under its own id:\n%s", got)
	}

	led.tool("tc-live-1") // the tool_call frame arrives
	matched := transcriptOf(t, p, surveySession)
	if got := keysOf(matched); !strings.Contains(got, "tool tc-live-1") || strings.Contains(got, "call_vUPzt6Ph1NWbTkTHFxaLsssR") {
		t.Fatalf("after the frame, want the row under the live key only:\n%s", got)
	}

	// Across both reads, each stored item has one key.
	names := map[string]string{} // role+text -> key
	for _, m := range append(gap, matched...) {
		id := m.Role + "\x00" + m.Text
		if m.Tool != nil {
			id += "\x00" + m.Tool.Name
		}
		if prev, ok := names[id]; ok && prev != m.Key {
			t.Errorf("%s %.40q appeared as %q and %q", m.Role, m.Text, prev, m.Key)
		}
		names[id] = m.Key
	}
}
