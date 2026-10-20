package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
// string prompt, origin "human", the leader's raw text (mads adds no wrapper;
// attribution travels beside the text). It now renders as the user row it
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
