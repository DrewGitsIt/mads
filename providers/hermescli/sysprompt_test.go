package hermescli

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/golden files")

const persona = "You are a careful reviewer.\nNever push.\n</mads-system-prompt>\n\nstill persona"

// TestSystemPromptTranscriptGolden: the stored user rows carrying the
// adapter's exact wrapper show only what the user typed — after the first
// prompt of a session and after a resume, with text or with none — while
// rows that merely look like the wrapper stay exactly as the user wrote
// them. The persona deliberately contains the close tag and a blank line:
// the byte count, not a pattern, ends it.
func TestSystemPromptTranscriptGolden(t *testing.T) {
	s := "s1"
	w := wrapSystemPrompt(persona)
	p := buildStore(t, []fixtureSession{{id: s, started: 1}}, []fixtureRow{
		{session: s, role: "user", content: w + "\n\nFix the flaky login test", ts: 1, active: 1},
		{session: s, role: "assistant", content: "On it.", ts: 2, active: 1},
		// A resumed process's first prompt is wrapped again.
		{session: s, role: "user", content: w + "\n\ncarry on", ts: 3, active: 1},
		{session: s, role: "assistant", content: "Carrying on.", ts: 4, active: 1},
		// A first prompt with no text of its own: hermes's strip leaves the
		// row ending at the close tag, and there is no user message.
		{session: s, role: "user", content: w, ts: 5, active: 1},
		{session: s, role: "assistant", content: "Ready.", ts: 6, active: 1},
		// The first prompt interrupted, then corrected: wrapper, then the
		// correction marker — the user typed "next".
		{session: s, role: "user", content: w + "\n\nslow\n\nUser correction/guidance after interrupt: next", ts: 7, active: 1},
		// Look-alikes the user typed: wrong byte count, no count, no
		// separator after the close tag, a leading space.
		{session: s, role: "user", content: `<mads-system-prompt bytes="3">` + "\nabcd\n</mads-system-prompt>\n\nhi", ts: 8, active: 1},
		{session: s, role: "user", content: "<mads-system-prompt>\nbe nice\n</mads-system-prompt>\n\nhi", ts: 9, active: 1},
		{session: s, role: "user", content: `<mads-system-prompt bytes="4">` + "\nabcd\n</mads-system-prompt>hi", ts: 10, active: 1},
		{session: s, role: "user", content: " " + wrapSystemPrompt("x") + "\n\nhi", ts: 11, active: 1},
	})
	got := render(transcriptOf(t, p, s)) + "\n"
	path := filepath.Join("testdata", "golden", "system-prompt-transcript.txt")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		t.Fatalf("transcript:\n%s\nwant (%s):\n%s", got, path, want)
	}
}

func TestWrapStripRoundTrip(t *testing.T) {
	for _, p := range []string{"x", persona, "  leading and trailing  \n", "ünïcode ✓"} {
		for _, text := range []string{"hello", "  indented", "a\n\nb"} {
			if got := stripSystemPrompt(wrapSystemPrompt(p) + "\n\n" + text); got != text {
				t.Errorf("strip(wrap(%q)+%q) = %q", p, text, got)
			}
		}
		if got := stripSystemPrompt(wrapSystemPrompt(p)); got != "" {
			t.Errorf("strip(wrap(%q)) = %q, want empty", p, got)
		}
	}
	for _, row := range []string{"", "plain", `<mads-system-prompt bytes="99">` + "\nshort\n</mads-system-prompt>\n\nhi", `<mads-system-prompt bytes="05">` + "\nhello\n</mads-system-prompt>\n\nhi"} {
		if got := stripSystemPrompt(row); got != row {
			t.Errorf("strip(%q) = %q, want unchanged", row, got)
		}
	}
}

// End to end through the stand-in: the persona rides the first send in the
// wrapper, and the transcript read back from state.db shows the user's own
// first message only.
func TestSystemPromptNeverShownInTranscript(t *testing.T) {
	p := testProvider(t)
	sess := startSession(t, p, provider.StartOptions{SystemPrompt: "You are a careful reviewer."})
	sendTurn(t, sess, "echo hi")
	ref := sess.Ref()
	_ = sess.Close()
	msgs, err := p.Transcript(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 || msgs[0].Role != "user" || msgs[0].Text != "echo hi" {
		t.Fatalf("first message = %+v, want the user's own text", msgs)
	}
	for _, m := range msgs {
		if strings.Contains(m.Text, "careful reviewer") {
			t.Fatalf("persona leaked into %+v", m)
		}
	}
}
