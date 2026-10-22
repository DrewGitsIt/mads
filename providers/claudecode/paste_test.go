package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/linkwrap"
)

func TestUnpaste(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"no paste", "hello\nthere", "hello\nthere"},
		{"idle form", "\n\n<pasted_content id=\"012e\">\na\nb\nc\n</pasted_content id=\"012e\">\n", "a\nb\nc"},
		{"queued form", "<pasted_content id=\"dcb0\">\na\nb\n</pasted_content id=\"dcb0\">", "a\nb"},
		{"typed around it", "see <pasted_content id=\"1\">\nx\ny\n</pasted_content id=\"1\"> thanks", "see x\ny thanks"},
		{"two pastes", "<pasted_content id=\"1\">\nx\n</pasted_content id=\"1\"> and <pasted_content id=\"2\">\ny\n</pasted_content id=\"2\">", "x and y"},
		{"a close tag for another id", "<pasted_content id=\"1\">\nx\n</pasted_content id=\"2\">", "<pasted_content id=\"1\">\nx\n</pasted_content id=\"2\">"},
	} {
		if got := unpaste(tc.in); got != tc.want {
			t.Errorf("%s: unpaste(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// A linked session's message typed into claude's terminal (runner typeInto,
// provider.TypedMessage) is a paste, which claude-code 2.1.285 stores
// wrapped in <pasted_content> — idle as a user line, mid-turn as a
// queued_command prompt. Both read back as the message mads sent, so the
// wrapper still parses as one attributed block. A two-line paste is stored
// as is. Captured 2026-10-22 through ptySession.Send, real claude, haiku.
func TestPastedPTYTranscripts(t *testing.T) {
	type row struct{ role, text string }
	leader := contracts.Attribution{LinkID: "lnk_x", SourceName: "leader"}
	for _, tc := range []struct {
		fixture string
		inject  string
		want    []row
	}{
		{"transcript_paste_pty.jsonl",
			linkwrap.Wrap("Count the lines of my message body and reply with only that number.\nline two\nline three", leader),
			[]row{{"user", ""}, {"agent", "3"}, {"user", "Now reply with only the word done.\nThat is all."}, {"agent", "done"}}},
		{"transcript_paste_pty_midturn.jsonl",
			linkwrap.Wrap("When the sleep finishes, also say the word mango.\nline two\nline three", leader),
			[]row{{"user", "Run the Bash command `sleep 8 && echo done`, then say 'finished'."}, {"tool", ""}, {"tool_result", "done"}, {"user", ""}, {"agent", "Finished. Mango."}}},
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
			if len(msgs) != len(tc.want) {
				t.Fatalf("got %d messages %+v, want %d", len(msgs), msgs, len(tc.want))
			}
			for i, w := range tc.want {
				if w.text == "" && w.role == "user" {
					w.text = tc.inject
				}
				if msgs[i].Role != w.role || (w.role != "tool" && msgs[i].Text != w.text) {
					t.Errorf("message %d = %s %q, want %s %q", i, msgs[i].Role, msgs[i].Text, w.role, w.text)
				}
			}
			for _, m := range msgs {
				if m.Text != tc.inject {
					continue
				}
				segs, ok := linkwrap.Parse(m.Text)
				if !ok || len(segs) != 1 || segs[0].Block == nil || segs[0].Block.Attribution.SourceName != "leader" {
					t.Errorf("inject row parses as %+v (ok %v), want one block from leader", segs, ok)
				}
			}
		})
	}
}

// The locator finds a PTY session whose first message was a paste: in
// history.jsonl (the real lines, captured with the fixture above) the
// display is only a placeholder, its text kept under pastedContents.
func TestLocateFromHistoryPastedFirstMessage(t *testing.T) {
	first := linkwrap.Wrap("Count the lines of my message body and reply with only that number.\nline two\nline three", contracts.Attribution{LinkID: "lnk_x", SourceName: "leader"})
	spawned := time.UnixMilli(1792645242905).Add(-3 * time.Second)
	ref, ok := locateFromHistory(filepath.Join("testdata", "history-paste.jsonl"), "/private/tmp/mads-pty-capture/real-claude", spawned, first)
	if !ok || ref != "75e17e4c-7929-43e0-a579-863cd9b877c9" {
		t.Fatalf("locateFromHistory = (%q, %v), want the captured session", ref, ok)
	}
}

// Rung 2 compares the transcript's first user message unpasted too.
func TestLocateSessionRung2PastedFirstMessage(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	first := "<mads-message kind=\"message\" from=\"leader\">\na\nb\n</mads-message>"
	writeTranscriptFixtureMsg(t, dir, "proj", "sess-pasted", cwd, "\n\n<pasted_content id=\"9f\">\n"+first+"\n</pasted_content id=\"9f\">\n")

	p := &Provider{cfg: Config{HistoryFile: filepath.Join(dir, "nonexistent.jsonl"), TranscriptDir: dir}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if ref, ok := p.LocateSession(ctx, cwd, spawnedAt, first); !ok || ref != "sess-pasted" {
		t.Fatalf("LocateSession = (%q, %v), want (sess-pasted, true)", ref, ok)
	}
}
