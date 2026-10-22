package claudecode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// The human's mid-turn send, as claude-code 2.1.285 records it
// (testdata/transcript_queued.jsonl, real lines from a stream-json session:
// a message sent while `sleep 5` ran). It exists only as a queued_command
// attachment whose prompt is an array of blocks, written when the model
// picks it up at the next tool result; it renders as the user's message in
// that place.
func TestQueuedPromptTranscript(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "transcript_queued.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	msgs, err := normalizeTranscript(f)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ role, text string }
	var got []row
	for _, m := range msgs {
		got = append(got, row{m.Role, m.Text})
	}
	want := []row{
		{"user", "Run the Bash command `sleep 5 && echo done`, then say 'finished'."},
		{"tool", ""},
		{"tool_result", "done"},
		{"user", "Also, after that, say the word banana."},
		{"agent", "finished\n\nbanana"},
	}
	if len(got) != len(want) {
		t.Fatalf("messages =\n%+v\nwant\n%+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if msgs[3].Timestamp.IsZero() {
		t.Error("mid-turn send lost its timestamp")
	}
}

// promptText takes both shapes claude writes, and a prompt of an
// unexpected shape costs neither reader the line.
func TestPromptTextShapes(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"string", `"also check the logs"`, "also check the logs"},
		{"blocks", `[{"type":"text","text":"one"},{"type":"image","source":{}},{"type":"text","text":"two"}]`, "one\ntwo"},
		{"null", `null`, ""},
		{"object", `{"unexpected":true}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p promptText
			if err := json.Unmarshal([]byte(tc.raw), &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if string(p) != tc.want {
				t.Errorf("prompt = %q, want %q", p, tc.want)
			}
			line := `{"type":"attachment","attachment":{"type":"queued_command","commandMode":"prompt","prompt":` + tc.raw + `}}`
			var tl transcriptLine
			if err := json.Unmarshal([]byte(line), &tl); err != nil {
				t.Errorf("transcriptLine: %v", err)
			}
			var el endingLine
			if err := json.Unmarshal([]byte(line), &el); err != nil {
				t.Errorf("endingLine: %v", err)
			}
		})
	}
}

// Only a "prompt"-mode queued command that isn't a peer message is the
// human's: task notifications and slash-command bookkeeping stay out.
func TestQueuedPromptSkips(t *testing.T) {
	for _, a := range []*transcriptAttachment{
		nil,
		{Type: "queued_command", CommandMode: "task-notification", Prompt: "<task-notification>…</task-notification>"},
		{Type: "queued_command", CommandMode: "prompt", Prompt: "  \n"},
		{Type: "queued_command", CommandMode: "prompt", Prompt: "<command-name>/compact</command-name>"},
		{Type: "edited_text_file", CommandMode: "prompt", Prompt: "x"},
	} {
		if got := queuedPrompt(a); got != "" {
			t.Errorf("queuedPrompt(%+v) = %q, want empty", a, got)
		}
	}
}

// A linked-session inject (a leader's message_session), captured on 2.1.285
// driven the way the runner writes it. Chat mode: the runner queues an inject
// that arrives mid-turn (runner.Inject) and sends it after the turn's result,
// so claude records an ordinary user line, attributed by its turn ordinal as
// before. PTY mode: the runner has no turn signal and types the inject
// straight into the terminal, so mid-turn claude records a queued_command —
// string prompt, origin "human", the leader's raw text (captured before mads
// wrapped injects in pkg/linkwrap; TestPastedPTYTranscripts has the wrapped,
// pasted form). It now renders as the user row it
// is, like the same inject typed while idle, instead of vanishing.
func TestInjectTranscripts(t *testing.T) {
	type row struct{ role, text string }
	leader := "Leader here: when the sleep finishes, also say the word mango."
	for _, tc := range []struct {
		fixture string
		want    []row
	}{
		{"transcript_inject_stream.jsonl", []row{
			{"user", "Run the Bash command `sleep 5 && echo done`, then say 'finished'."},
			{"tool", ""},
			{"tool_result", "done"},
			{"agent", "Finished."},
			{"user", leader},
			{"agent", "Mango."},
		}},
		{"transcript_inject_pty.jsonl", []row{
			{"user", "Run the Bash command `sleep 8 && echo done`, then say 'finished'."},
			{"tool", ""},
			{"tool_result", "done"},
			{"user", leader},
			{"agent", "Finished. Mango."},
		}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			msgs, err := normalizeTranscript(f)
			if err != nil {
				t.Fatal(err)
			}
			var got []row
			for _, m := range msgs {
				if m.Inbound != nil {
					t.Errorf("%q read as inbound; an inject is the session's own user turn", m.Text)
				}
				got = append(got, row{m.Role, m.Text})
			}
			if len(got) != len(tc.want) {
				t.Fatalf("messages =\n%+v\nwant\n%+v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("message %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// A background task's notice is written as a user line when it arrives
// idle and as a "task-notification" queued_command attachment when it
// arrives mid-turn; both render as the same "notification" row, keyed by
// the line's uuid. testdata/transcript_task_notices.jsonl is real lines,
// trimmed: one subagent (a0b39524a6827d682) that notified again and again
// under one task id, in both forms, some notices with a tool-use id and
// some without, then two Monitor events from another session (no tool-use
// id either). Three lines are constructed, with zero uuids: a real
// notice's block repeated in the other form (twice) and a tool-use-less
// block repeated verbatim. Each distinct notice shows once, in order;
// the repeats don't, and a chained read keys and dedupes like a full one.
func TestTaskNoticeTranscript(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "transcript_task_notices.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	const agent = `Agent "Build synthesis eval harness" finished`
	const monitor = `Monitor event: "watching /tmp/bgmon/watched.txt for first line"`
	want := []struct{ key, text string }{
		{"afae394a-5829-49fb-a6be-c3eeb8f6114e", agent}, // user line, toolu_01KRhF…
		{"fff4f878-117e-412d-b05a-c6b9e2988fe1", agent}, // attachment, no tool-use id
		{"a4cab899-9fe3-4338-bb48-4599ccb36c8b", agent}, // user line, toolu_01ACrS…
		{"5111c4b9-7f1e-43b1-8ca7-cb2273c906ae", agent}, // attachment, toolu_0147UG…
		{"e23deb57-729d-4f39-8317-b1c00ea32110", agent}, // attachment, toolu_01BwBh…
		{"451690fc-c99b-492a-add6-488783eac9ff", agent}, // user line, no tool-use id
		{"a4d6abc4-eb87-4d9a-a3a5-3b4834c9d008", monitor},
		{"2fa8dbc8-64eb-4270-9cb1-3a879bd6fa8e", monitor},
	}
	check := func(name string, msgs []provider.Message) {
		t.Helper()
		if len(msgs) != len(want) {
			for _, m := range msgs {
				t.Logf("%s %s %q", m.Role, m.Key, m.Text)
			}
			t.Fatalf("%s: %d rows, want %d", name, len(msgs), len(want))
		}
		for i, w := range want {
			m := msgs[i]
			if m.Role != "notification" || !m.Notice || m.Key != w.key || m.Text != w.text || m.Timestamp.IsZero() {
				t.Errorf("%s row %d = {%s notice=%v key=%s %q ts=%v}, want notification %s %q", name, i, m.Role, m.Notice, m.Key, m.Text, m.Timestamp, w.key, w.text)
			}
		}
	}

	full, err := normalizeTranscript(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	check("Transcript", full)
	keyed, err := normalizeKeyed(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	check("KeyedTranscript", keyed)

	// Split anywhere, the second half read from a clone of the first's
	// state (TranscriptSince's cursor): the same rows, the same keys.
	lines := bytes.SplitAfter(raw, []byte("\n"))
	for k := range lines {
		st := newTranscriptState(false)
		var msgs []provider.Message
		for _, l := range lines[:k] {
			msgs = st.add(l, msgs)
		}
		next := st.clone()
		for _, l := range lines[k:] {
			msgs = next.add(l, msgs)
		}
		check(fmt.Sprintf("split at line %d", k), msgs)
	}
}

// The dedupe identity: task id plus tool-use id when the block has one,
// otherwise the whole block, so a re-notified task's distinct notices all
// show.
func TestNoticeIdentity(t *testing.T) {
	block := func(inner string) string { return "<task-notification>\n" + inner + "\n</task-notification>" }
	a1 := block("<task-id>a1</task-id>\n<tool-use-id>toolu_1</tool-use-id>\n<result>first</result>")
	a1again := block("<task-id>a1</task-id>\n<tool-use-id>toolu_1</tool-use-id>\n<result>said differently</result>")
	a1other := block("<task-id>a1</task-id>\n<tool-use-id>toolu_2</tool-use-id>\n<result>first</result>")
	b1 := block("<task-id>b1</task-id>\n<tool-use-id>toolu_1</tool-use-id>")
	m1 := block("<task-id>m1</task-id>\n<event>changed</event>")
	m2 := block("<task-id>m1</task-id>\n<event>expired</event>")
	if noticeIdentity(a1) != noticeIdentity(a1again) {
		t.Error("same (task id, tool-use id) read as two notices")
	}
	for _, pair := range [][2]string{{a1, a1other}, {a1, b1}, {m1, m2}} {
		if noticeIdentity(pair[0]) == noticeIdentity(pair[1]) {
			t.Errorf("distinct notices share an identity:\n%s\n%s", pair[0], pair[1])
		}
	}
	if noticeIdentity(m1) != noticeIdentity("  "+m1+"\n") {
		t.Error("a tool-use-less block's identity depends on surrounding whitespace")
	}
}
