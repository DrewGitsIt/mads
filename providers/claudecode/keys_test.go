package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// The keys fixtures are the 2026-10-20 survey's real captures (claude-code
// 2.1.285, haiku), cut down and scrubbed: testdata/keys-<run>.stdout.jsonl
// is the process's stdout, keys-<run>.jsonl its session JSONL.
//   - main: a plain reply; text, Bash `ls`, text; a markdown answer; a
//     WebSearch call. Sends without a stdin uuid.
//   - intr: an interrupt mid-text; an interrupt mid-tool; a mid-turn send
//     ("Also say kiwi.", a queued_command); a recall. Every send carries a
//     stdin uuid aaaaaaaa-0000-4000-8000-00000000000N.
//   - kill1 (JSONL only): a process killed mid-text and resumed, which
//     writes the synthetic "No response requested.".

// replayKeyed runs a captured stdout through a SettleHints streamSession,
// with mads's sends and interrupts where the capture had them.
func replayKeyed(t *testing.T, run string, settle bool) []provider.Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "keys-"+run+".stdout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := &streamSession{events: make(chan provider.Event, 4096), announced: make(chan string, 1), stdin: discardWriteCloser{}, settle: settle}
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		l := parseWireLine(t, ln)
		switch {
		case l.Type == "user" && l.IsReplay && l.Origin == nil:
			_ = s.Send(context.Background(), "replayed")
		case l.Type == "control_response":
			_ = s.Interrupt(context.Background()) // the capture's only control requests
		}
		s.handleLine([]byte(ln))
	}
	close(s.events)
	var evs []provider.Event
	for ev := range s.events {
		evs = append(evs, ev)
	}
	return evs
}

func storedRows(t *testing.T, run string) []provider.Message {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "keys-"+run+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	msgs, err := normalizeTranscript(f)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

type keyedItem struct {
	role, key, text string
}

// liveItems folds a replay's events into items the way the conformance
// probe does, failing on an event without a key or a text key that
// reappears after another item started.
func liveItems(t *testing.T, evs []provider.Event) []keyedItem {
	t.Helper()
	var items []keyedItem
	index := map[string]int{}
	last := ""
	for _, ev := range evs {
		var role string
		switch ev.Kind {
		case provider.EventText:
			role = "agent"
		case provider.EventThinking:
			if ev.Text == "" {
				continue
			}
			role = "thinking"
		case provider.EventToolUse:
			role = "tool"
		case provider.EventToolResult:
			role = "tool_result"
		default:
			continue
		}
		if ev.Key == "" {
			t.Fatalf("%s event without a key: %+v", ev.Kind, ev)
		}
		id := role + "\x00" + ev.Key
		if pos, ok := index[id]; ok {
			if last != id && role != "tool_result" {
				t.Fatalf("%s key %q reappears after %q started", role, ev.Key, last)
			}
			items[pos].text += ev.Text
		} else {
			index[id] = len(items)
			items = append(items, keyedItem{role, ev.Key, ev.Text})
		}
		last = id
	}
	return items
}

// TestCapturedKeysLiveEqualStored: on real captures, every streamed item
// is stored under the same key with the same text, every stored row was
// streamed, and both list them in the same order. The interrupted text
// (intr's first turn) may be cut at a different point on each side.
func TestCapturedKeysLiveEqualStored(t *testing.T) {
	for _, run := range []string{"main", "intr"} {
		t.Run(run, func(t *testing.T) {
			live := liveItems(t, replayKeyed(t, run, false))
			stored := map[string]provider.Message{}
			var storedOrder []string
			for _, m := range storedRows(t, run) {
				switch m.Role {
				case "agent", "tool", "tool_result":
					if m.Key == "" {
						t.Fatalf("stored %s row without a key: %+v", m.Role, m)
					}
					stored[m.Role+"\x00"+m.Key] = m
					storedOrder = append(storedOrder, m.Role+"\x00"+m.Key)
				}
			}
			var liveOrder []string
			for _, it := range live {
				if it.role == "thinking" {
					continue
				}
				id := it.role + "\x00" + it.key
				liveOrder = append(liveOrder, id)
				m, ok := stored[id]
				if !ok {
					t.Errorf("streamed %s %q never stored", it.role, it.key)
					continue
				}
				if it.role != "tool" && m.Text != it.text && !strings.HasPrefix(m.Text, it.text) {
					t.Errorf("%s %q: streamed %.80q, stored %.80q", it.role, it.key, it.text, m.Text)
				}
			}
			if strings.Join(liveOrder, "|") != strings.Join(storedOrder, "|") {
				t.Errorf("order differs:\nlive   %q\nstored %q", liveOrder, storedOrder)
			}
			if len(live) < 4 {
				t.Fatalf("only %d live items: the fixture didn't replay", len(live))
			}
		})
	}
}

// Every message in the captures opens with a signature-only thinking block
// (no row, no live event), so each text block is index 1 of its message:
// the count has to include the block nothing shows.
func TestSignatureOnlyThinkingCountsTowardIndex(t *testing.T) {
	for _, m := range storedRows(t, "main") {
		if m.Role == "agent" && !strings.HasSuffix(m.Key, ":1") {
			t.Errorf("agent row key %q, want index 1 (after the signature-only thinking block)", m.Key)
		}
	}
	for _, ev := range replayKeyed(t, "main", false) {
		if ev.Kind == provider.EventThinking && ev.Text != "" {
			t.Errorf("signature-only thinking surfaced: %+v", ev)
		}
	}
}

// Claude re-appends whole stretches of history under the same uuids; read
// once or with the copy, the rows and their keys are the same.
func TestTranscriptKeysSurviveReappendedHistory(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "keys-main.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(strings.TrimSpace(string(raw)), "\n")
	doubled := string(raw) + strings.Join(lines[:len(lines)*2/3], "") + "\n"
	once, err := normalizeTranscript(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	twice, err := normalizeTranscript(strings.NewReader(doubled))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rowIDs(twice), rowIDs(once); got != want {
		t.Errorf("re-appended history changed the rows:\n got %s\nwant %s", got, want)
	}
}

func rowIDs(ms []provider.Message) string {
	var b strings.Builder
	for _, m := range ms {
		b.WriteString(m.Role + ":" + m.Key + ":" + m.Text + "\n")
	}
	return b.String()
}

// The intr capture's stored user side: sends keyed by their stdin uuid,
// the mid-turn send by its queued_command source_uuid, and claude's two
// interrupt markers as notices keyed by their own line uuid.
func TestTranscriptUserKeys(t *testing.T) {
	const pre = "user:aaaaaaaa-0000-4000-8000-00000000000"
	var users, notices []provider.Message
	for _, m := range storedRows(t, "intr") {
		if m.Role != "user" {
			continue
		}
		if m.Notice {
			notices = append(notices, m)
		} else {
			users = append(users, m)
		}
	}
	want := []struct{ key, text string }{
		{pre + "1", "Write a 500-word story"},
		{pre + "2", "Run the Bash command `sleep 20"},
		{pre + "3", "Run the Bash command `sleep 6"},
		{pre + "4", "Also say kiwi."},
		{pre + "5", "In one line"},
	}
	if len(users) != len(want) {
		t.Fatalf("got %d user rows, want %d: %+v", len(users), len(want), users)
	}
	for i, w := range want {
		if users[i].Key != w.key || !strings.HasPrefix(users[i].Text, w.text) {
			t.Errorf("user row %d = %q %.40q, want %q %q", i, users[i].Key, users[i].Text, w.key, w.text)
		}
	}
	wantMarkers := map[string]string{
		"af8ba638-ecad-43ed-b29b-dd3e18c37534": "[Request interrupted by user]",
		"0ba11e3e-8c40-44d4-8ca9-a62e29627cc9": "[Request interrupted by user for tool use]",
	}
	if len(notices) != len(wantMarkers) {
		t.Fatalf("got %d notices, want the 2 interrupt markers: %+v", len(notices), notices)
	}
	for _, n := range notices {
		if wantMarkers[n.Key] != n.Text {
			t.Errorf("notice %q %q: want an interrupt marker keyed by its line uuid", n.Key, n.Text)
		}
	}

	// A row mads didn't send keys by its own line uuid.
	for _, m := range storedRows(t, "main") {
		if m.Role == "user" && (!strings.HasPrefix(m.Key, "user:") || len(m.Key) != len("user:")+36) {
			t.Errorf("main user row key %q, want user:<line uuid>", m.Key)
		}
	}
}

// The resume after a kill writes a synthetic "No response requested."
// under a UUID message id: a notice, keyed by its line uuid, role kept.
func TestTranscriptNoResponseRequestedIsNotice(t *testing.T) {
	var found []provider.Message
	for _, m := range storedRows(t, "kill1") {
		if m.Text == "No response requested." {
			found = append(found, m)
		}
		if m.Notice && m.Role != "notification" && m.Text != "No response requested." {
			t.Errorf("unexpected notice %+v", m)
		}
	}
	if len(found) != 2 {
		t.Fatalf("want the capture's 2 synthetic rows, got %+v", found)
	}
	for _, m := range found {
		if !m.Notice || m.Role != "agent" || len(m.Key) != 36 || strings.Contains(m.Key, ":") {
			t.Errorf("synthetic row %+v: want Notice, role agent, key = its line uuid", m)
		}
	}
	if found[0].Key != "5e9287bf-adae-49e1-a53c-4e75cea28bfb" {
		t.Errorf("first synthetic row key %q, want its line uuid", found[0].Key)
	}
}

// SendKeyed puts the id on the stdin line as its top-level uuid; Send
// sends no uuid at all, exactly as before.
func TestSendKeyedWritesStdinUUID(t *testing.T) {
	for _, c := range []struct {
		keyed bool
		want  string
	}{{true, "11111111-2222-4333-8444-555555555555"}, {false, ""}} {
		var buf bytes.Buffer
		s := &streamSession{stdin: nopWriteCloser{&buf}}
		var err error
		if c.keyed {
			err = s.SendKeyed(context.Background(), "hi", c.want)
		} else {
			err = s.Send(context.Background(), "hi")
		}
		if err != nil {
			t.Fatal(err)
		}
		var line map[string]json.RawMessage
		if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		var got string
		if raw, ok := line["uuid"]; ok {
			_ = json.Unmarshal(raw, &got)
			if !c.keyed {
				t.Errorf("plain Send wrote a uuid: %s", buf.String())
			}
		}
		if got != c.want {
			t.Errorf("stdin uuid %q, want %q", got, c.want)
		}
	}
}

// Settle hints only when asked, at the points claude's JSONL catches up:
// after each main-agent message_stop, after each tool result (keyed by
// the tool), and just before each turn end.
func TestSettleHints(t *testing.T) {
	if n := len(ofKind(replayKeyed(t, "main", false), provider.EventSettle)); n != 0 {
		t.Fatalf("%d settle hints on a session that didn't ask", n)
	}
	evs := replayKeyed(t, "main", true)
	raw, _ := os.ReadFile(filepath.Join("testdata", "keys-main.stdout.jsonl"))
	stops := strings.Count(string(raw), `"type":"message_stop"`)
	ends := len(ofKind(evs, provider.EventTurnEnd))
	results := len(ofKind(evs, provider.EventToolResult))
	if got, want := len(ofKind(evs, provider.EventSettle)), stops+ends+results; got != want {
		t.Errorf("%d settle hints, want %d (%d message_stop + %d tool results + %d turn ends)", got, want, stops, results, ends)
	}
	for i, ev := range evs {
		if ev.Kind == provider.EventTurnEnd && (i == 0 || evs[i-1].Kind != provider.EventSettle) {
			t.Errorf("turn end at %d not preceded by a settle hint", i)
		}
		if ev.Kind == provider.EventToolResult && (i+1 >= len(evs) || evs[i+1].Kind != provider.EventSettle || evs[i+1].Key != ev.Key) {
			t.Errorf("tool result %q not followed by its settle hint", ev.Key)
		}
	}
}

// An interrupt's result line (is_error, error_during_execution, no text)
// closes the turn as interrupted with no error beside it; the same line
// with no interrupt asked for is still an error.
func TestInterruptResultIsNotAnError(t *testing.T) {
	evs := replayKeyed(t, "intr", false)
	if errs := ofKind(evs, provider.EventError); len(errs) != 0 {
		t.Errorf("interrupted turns surfaced errors: %+v", errs)
	}
	var interrupted int
	for _, ev := range ofKind(evs, provider.EventTurnEnd) {
		if ev.Interrupted {
			interrupted++
		}
	}
	if interrupted != 2 {
		t.Errorf("%d interrupted turn ends, want 2", interrupted)
	}
	l := parseWireLine(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s"}`)
	if got := ofKind(l.events(false, true), provider.EventError); len(got) != 1 {
		t.Errorf("unasked error_during_execution: %d errors, want 1", len(got))
	}
}

// TranscriptSince read after every append (half-written lines included)
// adds up to Transcript at each point, through re-appended history and a
// compaction; a cursor over a file that shrank or was replaced is stale.
func TestTranscriptSinceChainsToTranscript(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "keys-intr.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(strings.TrimSpace(string(raw)), "\n")
	lines[len(lines)-1] += "\n"
	// A compaction (boundary, then its summary) and a re-appended stretch.
	lines = append(lines,
		`{"type":"system","subtype":"compact_boundary","uuid":"cb-1","isSidechain":false,"compactMetadata":{"trigger":"manual","preTokens":9000,"postTokens":900}}`+"\n",
		`{"type":"user","uuid":"cs-1","isSidechain":false,"isCompactSummary":true,"message":{"role":"user","content":"Summary of the story so far."}}`+"\n",
	)
	lines = append(lines, lines[:12]...)

	dir := t.TempDir()
	p := New(Config{TranscriptDir: dir, HistoryFile: filepath.Join(dir, "h.jsonl")})
	path := filepath.Join(dir, "proj", "ref-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	ctx := context.Background()
	var (
		cursor  provider.TranscriptCursor
		chained []provider.Message
	)
	read := func() {
		rows, next, err := p.TranscriptSince(ctx, "ref-1", cursor)
		if err != nil {
			t.Fatalf("TranscriptSince: %v", err)
		}
		chained, cursor = append(chained, rows...), next
	}
	for i, ln := range lines {
		half := len(ln) / 2
		if _, err := f.WriteString(ln[:half]); err != nil {
			t.Fatal(err)
		}
		read() // a half-written line waits
		if _, err := f.WriteString(ln[half:]); err != nil {
			t.Fatal(err)
		}
		read()
		full, err := p.Transcript(ctx, "ref-1")
		if err != nil {
			t.Fatal(err)
		}
		heldBoundary := strings.Contains(ln, `"compact_boundary"`)
		if got, want := rowIDs(chained), rowIDs(full); got != want && !heldBoundary {
			t.Fatalf("after line %d chained != Transcript:\n got %s\nwant %s", i, got, want)
		}
	}
	if !strings.Contains(rowIDs(chained), "compact:cb-1:Summary of the story so far.") {
		t.Errorf("compaction row missing or split: %s", rowIDs(chained))
	}

	// Reading again from the same cursor gives nothing new, and an old
	// cursor still reads the same rows: a read never moves a cursor.
	if rows, _, err := p.TranscriptSince(ctx, "ref-1", cursor); err != nil || len(rows) != 0 {
		t.Errorf("re-read at the end: %d rows, %v", len(rows), err)
	}

	// Shrunk: stale.
	if err := os.Truncate(path, 10); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.TranscriptSince(ctx, "ref-1", cursor); !errors.Is(err, provider.ErrCursorStale) {
		t.Errorf("shrunk file: err %v, want ErrCursorStale", err)
	}
	// Replaced by a longer file: stale too.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(lines[0], 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.TranscriptSince(ctx, "ref-1", cursor); !errors.Is(err, provider.ErrCursorStale) {
		t.Errorf("replaced file: err %v, want ErrCursorStale", err)
	}
	if _, _, err := p.TranscriptSince(ctx, "ref-1", 42); !errors.Is(err, provider.ErrCursorStale) {
		t.Errorf("foreign cursor: err %v, want ErrCursorStale", err)
	}
}

// An old cursor stays usable: reading from it twice gives the same rows.
func TestTranscriptSinceCursorIsAValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj", "ref-2.jsonl")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	raw, _ := os.ReadFile(filepath.Join("testdata", "keys-main.jsonl"))
	lines := strings.SplitAfter(string(raw), "\n")
	if err := os.WriteFile(path, []byte(strings.Join(lines[:10], "")), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New(Config{TranscriptDir: dir, HistoryFile: filepath.Join(dir, "h.jsonl")})
	ctx := context.Background()
	_, mid, err := p.TranscriptSince(ctx, "ref-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o644); err != nil { // same inode: rewritten in place
		t.Fatal(err)
	}
	a, _, err := p.TranscriptSince(ctx, "ref-2", mid)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := p.TranscriptSince(ctx, "ref-2", mid)
	if err != nil {
		t.Fatal(err)
	}
	if rowIDs(a) == "" || rowIDs(a) != rowIDs(b) {
		t.Errorf("two reads from one cursor differ:\n%s\n---\n%s", rowIDs(a), rowIDs(b))
	}
}
