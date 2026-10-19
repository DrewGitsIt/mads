package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func user(text string) contracts.TranscriptMessage {
	return contracts.TranscriptMessage{Role: "user", Text: text}
}

func agent(text string) contracts.TranscriptMessage {
	return contracts.TranscriptMessage{Role: "agent", Text: text}
}

func toolCall(id, name, input string) contracts.TranscriptMessage {
	return contracts.TranscriptMessage{Role: "tool", Tool: &contracts.ToolUse{ID: id, Name: name, Input: json.RawMessage(input), Status: "completed", Path: "/abs/x"}}
}

func toolResult(id, text string, isError bool) contracts.TranscriptMessage {
	m := contracts.TranscriptMessage{Role: "tool_result", Text: text, IsError: isError}
	if id != "" {
		m.Tool = &contracts.ToolUse{ID: id}
	}
	return m
}

func injected(text, source string, digest bool) contracts.TranscriptMessage {
	return contracts.TranscriptMessage{Role: "user", Text: text,
		Attribution: &contracts.Attribution{LinkID: "lnk_1", SourceName: source, DigestAttributed: digest}}
}

// TestSplitTurns pins the turn model: each user message (human or link-
// injected) starts a turn, entries before the first one fold into turn 1,
// and a compaction stays inside the turn it fell in.
func TestSplitTurns(t *testing.T) {
	msgs := []contracts.TranscriptMessage{
		{Role: "notification", Text: "background task done"},
		agent("leftover"),
		user("first"),
		agent("one"),
		{Role: "compact", Compact: &contracts.CompactMarker{Trigger: "auto"}, Text: "summary carried forward"},
		agent("still one"),
		injected("from the leader", "Leader", false),
		agent("two"),
		user("third"),
	}
	turns := splitTurns(msgs)
	if len(turns) != 3 {
		t.Fatalf("got %d turns, want 3: %+v", len(turns), turns)
	}
	if len(turns[0]) != 6 || turns[0][0].Role != "notification" || turns[0][4].Role != "compact" {
		t.Errorf("turn 1 = %+v, want the pre-user entries, the first message, and the compaction", turns[0])
	}
	if turns[1][0].Attribution == nil || len(turns[1]) != 2 {
		t.Errorf("turn 2 = %+v, want the injected message and its reply", turns[1])
	}
	if len(turns[2]) != 1 || turns[2][0].Text != "third" {
		t.Errorf("turn 3 = %+v", turns[2])
	}

	if got := splitTurns(nil); len(got) != 0 {
		t.Errorf("empty transcript: %d turns, want 0", len(got))
	}
	if got := splitTurns([]contracts.TranscriptMessage{agent("hi")}); len(got) != 1 {
		t.Errorf("no user message: %d turns, want 1", len(got))
	}

	rendered := renderTurn(1, turns[0])
	if !strings.Contains(rendered, "notification: background task done\n") ||
		!strings.Contains(rendered, "[context compacted]\n") || strings.Contains(rendered, "summary carried forward") {
		t.Errorf("turn 1 rendering:\n%s", rendered)
	}
}

func numberedTurns(n int) [][]contracts.TranscriptMessage {
	var msgs []contracts.TranscriptMessage
	for i := 1; i <= n; i++ {
		msgs = append(msgs, user(fmt.Sprintf("message %d", i)), agent(fmt.Sprintf("reply %d", i)))
	}
	return splitTurns(msgs)
}

// TestRenderTurnsSliceSlicing covers count alone, from_turn + count,
// clamping at either end, and from_turn past the last turn.
func TestRenderTurnsSliceSlicing(t *testing.T) {
	turns := numberedTurns(5)
	cases := []struct {
		name            string
		count, fromTurn int
		wantHeader      string
		wantTurns       []int
		wantNote        string
	}{
		{"default is the last turn", 0, 0, "S — turn 5 of 5\n", []int{5}, ""},
		{"count alone", 2, 0, "S — turns 4–5 of 5\n", []int{4, 5}, ""},
		{"count past the start clamps", 9, 0, "S — turns 1–5 of 5\n", []int{1, 2, 3, 4, 5}, "(asked for the last 9; it has 5)"},
		{"from_turn with count", 2, 2, "S — turns 2–3 of 5\n", []int{2, 3}, ""},
		{"from_turn with count past the end clamps", 4, 4, "S — turns 4–5 of 5\n", []int{4, 5}, "(asked for turns 4–7; it has 5)"},
		{"from_turn alone is one turn", 0, 3, "S — turn 3 of 5\n", []int{3}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := renderTurnsSlice("S", turns, tc.count, tc.fromTurn)
			if !ok {
				t.Fatalf("ok = false: %s", got)
			}
			if !strings.HasPrefix(got, tc.wantHeader) {
				t.Errorf("header:\n%s\nwant prefix %q", got, tc.wantHeader)
			}
			if tc.wantNote != "" && !strings.Contains(got, tc.wantNote) {
				t.Errorf("missing note %q:\n%s", tc.wantNote, got)
			}
			for n := 1; n <= 5; n++ {
				in := strings.Contains(got, fmt.Sprintf("## Turn %d · user\nmessage %d\n", n, n))
				want := false
				for _, w := range tc.wantTurns {
					want = want || w == n
				}
				if in != want {
					t.Errorf("turn %d present = %v, want %v:\n%s", n, in, want, got)
				}
			}
		})
	}

	got, ok := renderTurnsSlice("S", turns, 1, 6)
	if ok || got != "S has 5 turns; from_turn 6 is past the end. Omit from_turn to read the last turns." {
		t.Errorf("from_turn past the end = %q, ok %v", got, ok)
	}
	if got, ok := renderTurnsSlice("S", nil, 1, 0); !ok || got != "S has no turns yet." {
		t.Errorf("no turns = %q, ok %v", got, ok)
	}
}

// TestRenderTurnToolPairing proves a tool call renders with its compact
// input and its paired output — by id, or positionally when the result has
// none — with truncation markers giving the true size, errors marked, and
// no statuses or paths.
func TestRenderTurnToolPairing(t *testing.T) {
	longOut := strings.Repeat("x", 18430)
	longIn := `{"command":"` + strings.Repeat("y", 1200) + `"}`
	msgs := []contracts.TranscriptMessage{
		user("go"),
		toolCall("t1", "Bash", "{\n  \"command\": \"ls\"\n}"),
		toolCall("t2", "Read", `{"file_path":"/a"}`),
		toolResult("t2", "file a", false),
		toolResult("t1", "one\ntwo", false),
		toolCall("t3", "Bash", longIn),
		toolResult("t3", longOut, true),
		toolCall("", "Edit", `{"file_path":"/b"}`),
		toolResult("", "edited", false),
		toolResult("t9", "stray", false),
	}
	got := renderTurn(1, msgs)
	want := "## Turn 1 · user\ngo\n" +
		"tool Bash {\"command\":\"ls\"}\n  → one\n    two\n" +
		"tool Read {\"file_path\":\"/a\"}\n  → file a\n"
	if !strings.HasPrefix(got, want) {
		t.Errorf("got:\n%s\nwant prefix:\n%s", got, want)
	}
	if !strings.Contains(got, "…(truncated, 1,214 chars)\n") {
		t.Errorf("input truncation marker missing:\n%s", got)
	}
	if !strings.Contains(got, "  → error: "+strings.Repeat("x", turnsToolOutputMax)+"…(truncated, 18,430 chars)\n") {
		t.Errorf("output truncation/error marker missing")
	}
	if !strings.Contains(got, "tool Edit {\"file_path\":\"/b\"}\n  → edited\n") {
		t.Errorf("positional pairing missing:\n%s", got)
	}
	if !strings.HasSuffix(got, "  → stray\n") {
		t.Errorf("unpaired result should render on its own:\n%s", got)
	}
	for _, noise := range []string{"completed", "/abs/x", "t1", "t2"} {
		if strings.Contains(got, noise) {
			t.Errorf("rendering leaks %q:\n%s", noise, got)
		}
	}
}

// TestRenderTurnAgentFragments proves consecutive agent entries (streaming
// fragments some transports record separately) read as one reply.
func TestRenderTurnAgentFragments(t *testing.T) {
	got := renderTurn(3, []contracts.TranscriptMessage{user("status?"), agent("The last lab"), agent("eling pass finished.")})
	if got != "## Turn 3 · user\nstatus?\nagent: The last labeling pass finished.\n" {
		t.Errorf("got %q", got)
	}
}

// TestTurnSourceLabel pins the attribution wording to the conversation
// pane's chips.
func TestTurnSourceLabel(t *testing.T) {
	cases := []struct {
		m    contracts.TranscriptMessage
		want string
	}{
		{user("hi"), "user"},
		{injected("do it", "SOM-DST Classifier Primer", false), "from Leader · SOM-DST Classifier Primer"},
		{injected("it did it", "payments", true), "digest · payments"},
		{contracts.TranscriptMessage{Role: "user", Attribution: &contracts.Attribution{SourceName: "pipeline"}}, "from pipeline"},
		{contracts.TranscriptMessage{Role: "user", Attribution: &contracts.Attribution{}}, "from mads"},
	}
	for _, tc := range cases {
		if got := turnSourceLabel(tc.m); got != tc.want {
			t.Errorf("label = %q, want %q", got, tc.want)
		}
	}
}

// TestRenderTurnsSliceCapDropsOldest proves the whole-response cap drops
// the oldest turns in the slice first and says so, and that a single turn
// over the cap is cut rather than dropped.
func TestRenderTurnsSliceCapDropsOldest(t *testing.T) {
	big := strings.Repeat("z", turnsResponseMax/4)
	var msgs []contracts.TranscriptMessage
	for i := 1; i <= 10; i++ {
		msgs = append(msgs, user(fmt.Sprintf("m%d", i)), agent(big))
	}
	got, ok := renderTurnsSlice("S", splitTurns(msgs), 10, 0)
	if !ok {
		t.Fatal(got)
	}
	if !strings.HasPrefix(got, "S — turns 8–10 of 10\n(turns 1–7 omitted to fit; ask with a smaller count)\n") {
		t.Errorf("header: %q", got[:120])
	}
	if strings.Contains(got, "## Turn 7 ") || !strings.Contains(got, "## Turn 8 ") {
		t.Errorf("wrong turns kept")
	}
	if len([]rune(got)) > turnsResponseMax+200 {
		t.Errorf("response %d chars, cap %d", len([]rune(got)), turnsResponseMax)
	}

	huge := splitTurns([]contracts.TranscriptMessage{user("m"), agent(strings.Repeat("q", turnsResponseMax*2))})
	got, _ = renderTurnsSlice("S", huge, 1, 0)
	if !strings.Contains(got, "(turn 1 cut to fit)") || !strings.Contains(got, "chars)") || len([]rune(got)) > turnsResponseMax+200 {
		t.Errorf("oversized single turn not cut: %d chars", len([]rune(got)))
	}
}

// TestRenderTurnsSliceSample is the realistic two-turn fixture the report
// quotes: a human message with a shell call, then a leader's injection.
func TestRenderTurnsSliceSample(t *testing.T) {
	msgs := []contracts.TranscriptMessage{
		user("How many labeled rows do we have?"),
		agent("Let me count them."),
		toolCall("toolu_1", "Bash", `{"command":"wc -l data/review/*.jsonl","description":"Count labeled rows"}`),
		toolResult("toolu_1", "  412 data/review/batch1.jsonl\n  388 data/review/batch2.jsonl\n  800 total", false),
		agent("800 labeled rows across two batches."),
		injected("Run the classifier on batch2 and report accuracy.", "SOM-DST Classifier Primer", false),
		toolCall("toolu_2", "Bash", `{"command":"python eval.py --batch 2"}`),
		toolResult("toolu_2", "Traceback (most recent call last):\nModuleNotFoundError: No module named 'sklearn'", true),
		agent("The eval failed: sklearn isn't installed. Want me to add it?"),
	}
	got, ok := renderTurnsSlice("Labeler", splitTurns(msgs), 2, 0)
	want := `Labeler — turns 1–2 of 2

## Turn 1 · user
How many labeled rows do we have?
agent: Let me count them.
tool Bash {"command":"wc -l data/review/*.jsonl","description":"Count labeled rows"}
  →   412 data/review/batch1.jsonl
      388 data/review/batch2.jsonl
      800 total
agent: 800 labeled rows across two batches.

## Turn 2 · from Leader · SOM-DST Classifier Primer
Run the classifier on batch2 and report accuracy.
tool Bash {"command":"python eval.py --batch 2"}
  → error: Traceback (most recent call last):
    ModuleNotFoundError: No module named 'sklearn'
agent: The eval failed: sklearn isn't installed. Want me to add it?
`
	if !ok || got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCommaInt(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 18430: "18,430", 1234567: "1,234,567"} {
		if got := commaInt(n); got != want {
			t.Errorf("commaInt(%d) = %q, want %q", n, got, want)
		}
	}
}
