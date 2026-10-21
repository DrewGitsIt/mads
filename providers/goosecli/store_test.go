package goosecli

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/providers/provider"
)

// captureRow is one run.store.jsonl line: a messages row goose inserted.
type captureRow struct {
	RowID     int64           `json:"rowid"`
	MessageID string          `json:"message_id"`
	Role      string          `json:"role"`
	Created   int64           `json:"created"`
	Content   json.RawMessage `json:"content"`
	Metadata  json.RawMessage `json:"metadata"`
}

func loadCaptureRows(t *testing.T) []captureRow {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "captures", "keys", "run.store.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var rows []captureRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var r captureRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	return rows
}

// storeFixture is a provider whose GOOSE_PATH_ROOT holds a sessions.db
// with goose's schema and one session, ready for rows.
type storeFixture struct {
	p   *Provider
	db  *sql.DB
	ref string
}

func newStoreFixture(t *testing.T, ref string, extraEnv ...string) *storeFixture {
	t.Helper()
	root := t.TempDir()
	db, err := openFakeStore(filepath.Join(root, "data", "sessions", "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("INSERT INTO sessions (id, working_dir) VALUES (?, '/tmp/mads-goose-survey/work1')", ref); err != nil {
		t.Fatal(err)
	}
	return &storeFixture{p: testProvider(t, append([]string{"GOOSE_PATH_ROOT=" + root}, extraEnv...)...), db: db, ref: ref}
}

func (f *storeFixture) insert(t *testing.T, rows ...captureRow) {
	t.Helper()
	for _, r := range rows {
		if _, err := f.db.Exec(`INSERT INTO messages (message_id, session_id, role, content_json, created_timestamp, metadata_json)
			VALUES (?, ?, ?, ?, ?, ?)`, r.MessageID, f.ref, r.Role, string(r.Content), r.Created, string(r.Metadata)); err != nil {
			t.Fatal(err)
		}
	}
}

func keyed(msgs []provider.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Role+" "+m.Key)
	}
	return out
}

// The survey session's store rows read back with the keys acprpc derives
// live from the same session's wire (acprpc keys_test.go
// TestReplayGooseCaptureKeys spells out the same strings): the hidden
// <turn-context> rows are gone, the pre-tool text, the tool call and its
// result are keyed as streamed, and the cancelled reply's stored partial
// keeps its live key.
func TestStoreKeysMatchCapture(t *testing.T) {
	f := newStoreFixture(t, "20261020_1")
	f.insert(t, loadCaptureRows(t)...)
	msgs, err := f.p.Transcript(context.Background(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"user user:msg_3346d3c7-408d-43f6-a8e6-42a2b8e0d42e",
		"agent chatcmpl-904@1792515348:0",
		"user user:msg_5e19f580-c942-4e4f-beb8-895a6fca4763",
		"agent chatcmpl-103@1792515354:0",
		"tool call_6464carb",
		"tool_result call_6464carb",
		"agent chatcmpl-928@1792515355:0",
		"user user:msg_7815a619-becd-4421-934c-0bcb2dd91997",
		"agent chatcmpl-17@1792515372:0",
		"user user:msg_6995a04a-39f3-4e37-9b9e-ee7e00e08f27",
		"agent chatcmpl-287@1792515379:0",
		"user user:msg_4667cafa-80d4-4a8a-8625-52042d5fa745",
		"agent chatcmpl-925@1792515387:0",
	}
	if got := keyed(msgs); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("keys:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	texts := map[string]string{
		"chatcmpl-103@1792515354:0": "I'm about to list the files in the current directory.",
		"call_6464carb":             "alpha.txt\nbeta.txt\nsub",
		"chatcmpl-287@1792515379:0": "1",
		"chatcmpl-925@1792515387:0": "READY",
	}
	for _, m := range msgs {
		if strings.Contains(m.Text, "<turn-context>") {
			t.Errorf("turn-context row leaked: %+v", m)
		}
		if want, ok := texts[m.Key]; ok && m.Role != "tool" && m.Text != want {
			t.Errorf("%s %s text %q, want the streamed %q", m.Role, m.Key, m.Text, want)
		}
	}
}

// ollama's ids repeat within a session; the created stamp keeps the keys
// apart. Thinking isn't counted in the text's run, as live.
func TestStoreKeysCollisionsAndRuns(t *testing.T) {
	f := newStoreFixture(t, "20261021_1")
	visible := json.RawMessage(`{"userVisible":true,"agentVisible":true}`)
	f.insert(t,
		captureRow{MessageID: "msg_u1", Role: "user", Created: 100, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`), Metadata: visible},
		captureRow{MessageID: "chatcmpl-7", Role: "assistant", Created: 101, Content: json.RawMessage(`[{"type":"text","text":"one"}]`), Metadata: visible},
		captureRow{MessageID: "msg_u2", Role: "user", Created: 110, Content: json.RawMessage(`[{"type":"text","text":"again"}]`), Metadata: visible},
		captureRow{MessageID: "chatcmpl-7", Role: "assistant", Created: 111, Content: json.RawMessage(`[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"two"}]`), Metadata: visible},
	)
	msgs, err := f.p.Transcript(context.Background(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	want := "user user:msg_u1|agent chatcmpl-7@101:0|user user:msg_u2|agent chatcmpl-7@111:0"
	if got := strings.Join(keyed(msgs), "|"); got != want {
		t.Fatalf("keys %s, want %s", got, want)
	}
}

// Reading the store as it grows: chained TranscriptSince calls add up to
// Transcript, whatever the read points; a rewrite under the cursor
// (goose's replace_conversation: delete and reinsert) is ErrCursorStale.
func TestStoreTranscriptSince(t *testing.T) {
	rows := loadCaptureRows(t)
	f := newStoreFixture(t, "20261020_1")
	ctx := context.Background()
	var chained []provider.Message
	var cursor provider.TranscriptCursor
	for _, cut := range []int{1, 2, 5, 8, 9, 13, len(rows)} {
		have := 0
		if c, ok := cursor.(storeCursor); ok {
			have = int(c.N)
		}
		f.insert(t, rows[have:cut]...)
		got, next, err := f.p.TranscriptSince(ctx, f.ref, cursor)
		if err != nil {
			t.Fatalf("TranscriptSince after %d rows: %v", cut, err)
		}
		chained, cursor = append(chained, got...), next
	}
	full, err := f.p.Transcript(ctx, f.ref)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := keyed(chained), keyed(full); strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("chained:\n%s\nTranscript:\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	if more, _, err := f.p.TranscriptSince(ctx, f.ref, cursor); err != nil || len(more) != 0 {
		t.Fatalf("caught-up read = %d rows, %v; want none", len(more), err)
	}

	// compaction rewrites the session: same messages, new row ids.
	if _, err := f.db.Exec("DELETE FROM messages WHERE session_id = ?", f.ref); err != nil {
		t.Fatal(err)
	}
	f.insert(t, rows...)
	if _, _, err := f.p.TranscriptSince(ctx, f.ref, cursor); !errors.Is(err, provider.ErrCursorStale) {
		t.Fatalf("after a rewrite: %v, want ErrCursorStale", err)
	}
	if again, _, err := f.p.TranscriptSince(ctx, f.ref, nil); err != nil || len(again) != len(full) {
		t.Fatalf("re-read from nil: %d rows, %v; want %d", len(again), err, len(full))
	}
}

// A store whose schema isn't the one the reader knows (a column renamed,
// as a goose release outside the preflight window might) is not read:
// Transcript falls back to `goose session export`, TranscriptSince
// declines with ErrUnsupported. The export carries the same ids, so the
// keys survive the fallback.
func TestStoreSchemaMismatchFallsBackToExport(t *testing.T) {
	root := t.TempDir()
	db, err := openFakeStore(filepath.Join(root, "data", "sessions", "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE messages RENAME COLUMN content_json TO content"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	exportDir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("testdata", "captures", "oneshot-export.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc exportDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exportDir, doc.ID+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	p := testProvider(t, "GOOSE_PATH_ROOT="+root, "MADS_FAKE_EXPORT_DIR="+exportDir)

	msgs, err := p.Transcript(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(msgs) == 0 || msgs[len(msgs)-1].Key == "" {
		t.Fatalf("export fallback gave %d rows, last %+v; want keyed rows", len(msgs), msgs)
	}
	for _, m := range msgs {
		if m.Key == "" {
			t.Errorf("unkeyed row from the export: %+v", m)
		}
	}
	if _, _, err := p.TranscriptSince(context.Background(), doc.ID, nil); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("TranscriptSince on an unknown schema: %v, want ErrUnsupported", err)
	}
}

// Where the spawned goose keeps sessions.db (paths.rs Paths::data_dir).
func TestStorePath(t *testing.T) {
	for _, tc := range []struct {
		env  []string
		want string
	}{
		{[]string{"GOOSE_PATH_ROOT=/srv/g", "HOME=/home/u"}, "/srv/g/data/sessions/sessions.db"},
		{[]string{"GOOSE_PATH_ROOT=rel/root", "XDG_DATA_HOME=/xdg", "HOME=/home/u"}, "/xdg/goose/sessions/sessions.db"},
		{[]string{"GOOSE_PATH_ROOT=", "XDG_DATA_HOME=relative", "HOME=/home/u"}, "/home/u/.local/share/goose/sessions/sessions.db"},
	} {
		p := New(Config{Env: tc.env, EnvMode: agentenv.ModeIsolated})
		if got := p.storePath(); got != tc.want {
			t.Errorf("env %v: %s, want %s", tc.env, got, tc.want)
		}
	}
}
