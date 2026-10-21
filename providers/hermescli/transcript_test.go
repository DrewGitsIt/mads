package hermescli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// fixtureRow is one messages row to insert; zero fields bind NULL (or the
// column default for active/compacted/summary).
type fixtureRow struct {
	session, role, content, toolCalls, toolCallID, toolName string
	displayKind, displayMeta, uid                           string
	ts                                                      float64
	active, compacted, summary                              int
}

type fixtureSession struct {
	id, parent, endReason, modelConfig, source string
	started                                    float64
}

// buildStore writes a state.db from hermes's real schema
// (testdata/state-schema.sql) into a fresh home and returns a Provider
// reading it.
func buildStore(t *testing.T, sessions []fixtureSession, rows []fixtureRow) *Provider {
	t.Helper()
	home := t.TempDir()
	db, err := openTestStore(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	for _, s := range sessions {
		src := s.source
		if src == "" {
			src = "acp"
		}
		if _, err := db.Exec(`INSERT INTO sessions (id, source, parent_session_id, end_reason, model_config, started_at) VALUES (?, ?, ?, ?, ?, ?)`,
			s.id, src, null(s.parent), null(s.endReason), null(s.modelConfig), s.started); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, tool_name,
			display_kind, display_metadata, message_uid, timestamp, active, compacted, _compressed_summary)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.session, r.role, r.content, null(r.toolCalls), null(r.toolCallID), null(r.toolName),
			null(r.displayKind), null(r.displayMeta), null(r.uid), r.ts, r.active, r.compacted, r.summary); err != nil {
			t.Fatal(err)
		}
	}
	return New(Config{Home: home})
}

func render(msgs []provider.Message) string {
	var lines []string
	for _, m := range msgs {
		s := m.Role
		if m.IsError {
			s += "!"
		}
		if m.Tool != nil {
			s += "[" + m.Tool.ID + " " + m.Tool.Name
			if len(m.Tool.Input) > 0 {
				s += " " + string(m.Tool.Input)
			}
			s += "]"
		}
		lines = append(lines, s+": "+m.Text)
	}
	return strings.Join(lines, "\n")
}

func transcriptOf(t *testing.T, p *Provider, ref string) []provider.Message {
	t.Helper()
	msgs, err := p.Transcript(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

func assertTranscript(t *testing.T, msgs []provider.Message, want ...string) {
	t.Helper()
	if got := render(msgs); got != strings.Join(want, "\n") {
		t.Fatalf("transcript:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

const surveySession = "a403c50c-d98a-4b9f-93f1-6133bda1018e"

// surveyRows are the copilot gpt-4.1 run's rows (survey runc1, rows 3-14)
// plus the ollama run's cancel-before-output boundary (survey run2 rows
// 19-21), as hermes 0.22.2 wrote them: content, tool_calls JSON, tool ids,
// display kinds and message_uids verbatim; the long numbers reply is
// shortened.
func surveyRows() []fixtureRow {
	s := surveySession
	calls := `[{"id": "call_vUPzt6Ph1NWbTkTHFxaLsssR", "call_id": "call_vUPzt6Ph1NWbTkTHFxaLsssR", "response_item_id": "fc_vUPzt6Ph1NWbTkTHFxaLsssR", "type": "function", "function": {"name": "terminal", "arguments": "{\"command\":\"ls\"}"}}]`
	return []fixtureRow{
		{session: s, role: "user", content: "Reply with exactly one line: Hello from hermes.", uid: "892ffd876d2849118e916fb53200669a", ts: 1792515728.671863, active: 1},
		{session: s, role: "assistant", content: "Hello from hermes.", uid: "a289cb6e3743476283e67c6e074eaebd", ts: 1792515730.3589408, active: 1},
		{session: s, role: "user", content: "Do this in order: first write one short sentence saying you will list the directory. Then call your terminal tool to run: ls.", uid: "7278ac40edf248708c37803486abd6e2", ts: 1792515730.374868, active: 1},
		{session: s, role: "assistant", content: "", toolCalls: calls, uid: "15e2432c25a3487296dbd9078fe223e4", ts: 1792515731.0598762, active: 1},
		{session: s, role: "tool", content: `{"output": "alpha.txt\nbeta.md", "exit_code": 0, "error": null}`, toolCallID: "call_vUPzt6Ph1NWbTkTHFxaLsssR", toolName: "terminal", uid: "0e40653d3f1b435198c5c5f9518f9e70", ts: 1792515731.1852632, active: 1},
		{session: s, role: "assistant", content: "Listing the directory now. I found the files: alpha.txt and beta.md.", uid: "83fc177cefa24b6e8475ca8da6ca7167", ts: 1792515733.004097, active: 1},
		{session: s, role: "user", content: "Write the numbers from 1 to 400, one per line, no other text.", uid: "e478041cc74d41a0885ea4dad7090740", ts: 1792515734.34879, active: 1},
		{session: s, role: "assistant", content: "1\n2\n3", displayMeta: `{"interrupted": true}`, uid: "32be2b5a557f45ea9f9fc11764d26ef1", ts: 1792515735.877459, active: 1},
		{session: s, role: "user", content: "Write the numbers from 1 to 400, one per line, no other text.\n\nUser correction/guidance after interrupt: In one short sentence: what did my previous message ask you to do?", uid: "d9ceeb8b8c9d47eabb6ea3d6d065d90b", ts: 1792515735.8973281, active: 1},
		{session: s, role: "assistant", content: "You asked me to write the numbers from 1 to 400, one per line, with no other text.", uid: "d56aea70ca1d4bb69f1cd7de7a82e4ab", ts: 1792515736.711797, active: 1},
		// run2 rows 19-21: cancelled before any output.
		{session: s, role: "user", content: "Count from 1 to 300, one number per line, no other text.", uid: "f3ceb933aa", ts: 1792515742.809004, active: 1},
		{session: s, role: "assistant", content: failedTurnNotice, displayKind: "failed_turn", uid: "bab173e5aa", ts: 1792515745.479476, active: 1},
		{session: s, role: "user", content: "Count from 1 to 300, one number per line, no other text.\n\nUser correction/guidance after interrupt: In one short sentence: what did my previous message ask you to do?", uid: "b17049b5aa", ts: 1792515745.487224, active: 1},
		{session: s, role: "assistant", content: "Your previous message asked me to count from 1 to 300, one number per line, with no other text.", uid: "93adcefbaa", ts: 1792515749.8244119, active: 1},
	}
}

func TestTranscriptFromSurveyRows(t *testing.T) {
	p := buildStore(t, []fixtureSession{{id: surveySession, started: 1792515728.69154}}, surveyRows())
	msgs := transcriptOf(t, p, surveySession)
	assertTranscript(t, msgs,
		"user: Reply with exactly one line: Hello from hermes.",
		"agent: Hello from hermes.",
		"user: Do this in order: first write one short sentence saying you will list the directory. Then call your terminal tool to run: ls.",
		`tool[call_vUPzt6Ph1NWbTkTHFxaLsssR terminal {"command":"ls"}]: `,
		"tool_result[call_vUPzt6Ph1NWbTkTHFxaLsssR terminal]: "+`{"output": "alpha.txt\nbeta.md", "exit_code": 0, "error": null}`,
		"agent: Listing the directory now. I found the files: alpha.txt and beta.md.",
		"user: Write the numbers from 1 to 400, one per line, no other text.",
		"agent: 1\n2\n3",
		"user: In one short sentence: what did my previous message ask you to do?",
		"agent: You asked me to write the numbers from 1 to 400, one per line, with no other text.",
		"user: Count from 1 to 300, one number per line, no other text.",
		"agent!: "+failedTurnNotice,
		"user: In one short sentence: what did my previous message ask you to do?",
		"agent: Your previous message asked me to count from 1 to 300, one number per line, with no other text.",
	)
	if want := time.Unix(1792515728, 671863000).UTC(); msgs[0].Timestamp.Sub(want).Abs() > time.Microsecond {
		t.Errorf("timestamp = %v, want %v", msgs[0].Timestamp, want)
	}
}

// A boundary row written before hermes typed it (survey run3 row 40: the
// process died before display_kind landed) is still a failed turn; a reply
// that merely quotes the notice inside more text is not.
func TestTranscriptUntypedFailedTurn(t *testing.T) {
	s := "s1"
	p := buildStore(t, []fixtureSession{{id: s, started: 1}}, []fixtureRow{
		{session: s, role: "user", content: "go", ts: 1, active: 1},
		{session: s, role: "assistant", content: failedTurnNotice, ts: 2, active: 1},
		{session: s, role: "user", content: "quote it", ts: 3, active: 1},
		{session: s, role: "assistant", content: "hermes said: " + failedTurnNotice, ts: 4, active: 1},
		{session: s, role: "assistant", content: partialFailedNotice, displayKind: "failed_turn", ts: 5, active: 1},
	})
	assertTranscript(t, transcriptOf(t, p, s),
		"user: go", "agent!: "+failedTurnNotice, "user: quote it", "agent: hermes said: "+failedTurnNotice, "agent!: "+partialFailedNotice)
}

// Compaction in place: the summarized rows stay as display history
// (active=0, compacted=1), the carrier is the summary, the protected tail
// is cloned live with the same message_uid while its originals become
// rewind rows (active=0, compacted=0 — never displayed), and a model-only
// row never shows (hermes_state_messages.py archive_and_compact).
func TestTranscriptCompactionGenerations(t *testing.T) {
	s := "s1"
	p := buildStore(t, []fixtureSession{{id: s, started: 1}}, []fixtureRow{
		{session: s, role: "user", content: "first ask", uid: "u1", ts: 1, compacted: 1},
		{session: s, role: "assistant", content: "first answer", uid: "a1", ts: 2, compacted: 1},
		{session: s, role: "user", content: "second ask", uid: "u2", ts: 3},         // rewind original
		{session: s, role: "assistant", content: "second answer", uid: "a2", ts: 4}, // rewind original
		{session: s, role: "user", content: "[summary] user asked twice", uid: "sum", ts: 5, active: 1, summary: 1},
		{session: s, role: "user", content: "second ask", uid: "u2", ts: 3, active: 1},         // cloned tail
		{session: s, role: "assistant", content: "second answer", uid: "a2", ts: 4, active: 1}, // cloned tail
		{session: s, role: "user", content: "internal nudge", displayMeta: `{"model_only": true}`, uid: "m1", ts: 6, active: 1},
		{session: s, role: "user", content: "third ask", uid: "u3", ts: 7, active: 1},
	})
	assertTranscript(t, transcriptOf(t, p, s),
		"user: first ask", "agent: first answer", "compact: [summary] user asked twice", "user: second ask", "agent: second answer", "user: third ask")
}

// session/load deletes and reinserts the active rows (fresh ids, same
// uids); a generation left behind archived by a concurrent compaction must
// not show twice.
func TestTranscriptDedupesByUID(t *testing.T) {
	s := "s1"
	p := buildStore(t, []fixtureSession{{id: s, started: 1}}, []fixtureRow{
		{session: s, role: "user", content: "hello", uid: "u1", ts: 1, compacted: 1},
		{session: s, role: "user", content: "hello", uid: "u1", ts: 1, active: 1},
		{session: s, role: "assistant", content: "hi there", uid: "a1", ts: 2, active: 1},
	})
	assertTranscript(t, transcriptOf(t, p, s), "user: hello", "agent: hi there")
}

// A compression that rotates to a child session: the conversation is the
// lineage root → tip; a /branch copy of the same parent is someone else's.
func TestTranscriptFollowsCompressionLineage(t *testing.T) {
	branched, _ := json.Marshal(map[string]string{"_branched_from": "root"})
	p := buildStore(t,
		[]fixtureSession{
			{id: "root", endReason: "compression", started: 1},
			{id: "branch", parent: "root", modelConfig: string(branched), started: 2},
			{id: "tip", parent: "root", modelConfig: `{"model":"gpt-4.1"}`, started: 3},
		},
		[]fixtureRow{
			{session: "root", role: "user", content: "before", uid: "u1", ts: 1, active: 1},
			{session: "root", role: "assistant", content: "ok", uid: "a1", ts: 2, active: 1},
			{session: "branch", role: "user", content: "branch only", uid: "b1", ts: 3, active: 1},
			{session: "tip", role: "user", content: "[summary]", uid: "sum", ts: 4, active: 1, summary: 1},
			{session: "tip", role: "user", content: "after", uid: "u2", ts: 5, active: 1},
		})
	assertTranscript(t, transcriptOf(t, p, "root"), "user: before", "agent: ok", "compact: [summary]", "user: after")
}

// Structured content is stored "\x00json:"-prefixed (hermes_state.py
// _encode_content); its text parts are the message, image parts dropped. A
// malformed marker fails closed to the raw string, like hermes.
func TestTranscriptStructuredContent(t *testing.T) {
	s := "s1"
	parts := contentJSONPrefix + `[{"type":"text","text":"look at this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]`
	p := buildStore(t, []fixtureSession{{id: s, started: 1}}, []fixtureRow{
		{session: s, role: "user", content: parts, ts: 1, active: 1},
		{session: s, role: "assistant", content: contentJSONPrefix + `"a literal \u0000json: string"`, ts: 2, active: 1},
		{session: s, role: "assistant", content: contentJSONPrefix + `{broken`, ts: 3, active: 1},
	})
	assertTranscript(t, transcriptOf(t, p, s), "user: look at this", "agent: a literal \x00json: string", "agent: "+contentJSONPrefix+"{broken")
}

func TestTranscriptToolErrorAndRawArguments(t *testing.T) {
	s := "s1"
	calls := `[{"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": "not json"}}]`
	p := buildStore(t, []fixtureSession{{id: s, started: 1}}, []fixtureRow{
		{session: s, role: "assistant", content: "Reading.", toolCalls: calls, ts: 1, active: 1},
		{session: s, role: "tool", content: `{"error": "file not found"}`, toolCallID: "call_1", toolName: "read_file", ts: 2, active: 1},
		{session: s, role: "tool", content: `{"output": "", "exit_code": 2, "error": null}`, toolCallID: "call_2", toolName: "terminal", ts: 3, active: 1},
	})
	assertTranscript(t, transcriptOf(t, p, s),
		"agent: Reading.",
		`tool[call_1 read_file "not json"]: `,
		`tool_result![call_1 read_file]: {"error": "file not found"}`,
		`tool_result[call_2 terminal]: {"output": "", "exit_code": 2, "error": null}`)
}

func TestTranscriptErrors(t *testing.T) {
	p := buildStore(t, []fixtureSession{{id: "s1", started: 1}}, nil)
	if _, err := p.Transcript(context.Background(), ""); err == nil {
		t.Error("empty ref: want error")
	}
	if _, err := p.Transcript(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown ref = %v, want not found", err)
	}
	if msgs, err := p.Transcript(context.Background(), "s1"); err != nil || len(msgs) != 0 {
		t.Errorf("empty session = %v, %v", msgs, err)
	}
	if _, err := New(Config{Home: t.TempDir()}).Transcript(context.Background(), "s1"); err == nil || !strings.Contains(err.Error(), "no hermes state.db") {
		t.Errorf("no store = %v", err)
	}
}

// The reader never writes: read-only open, so a store hermes owns is safe
// to read while it runs, and a write through the reader's handle fails.
func TestStoreOpensReadOnly(t *testing.T) {
	p := buildStore(t, []fixtureSession{{id: "s1", started: 1}}, nil)
	db, _, err := p.openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("DELETE FROM sessions"); err == nil {
		t.Fatal("write through the reader's handle succeeded")
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n); err != nil || n != 1 {
		t.Fatalf("sessions = %d, %v", n, err)
	}
}
