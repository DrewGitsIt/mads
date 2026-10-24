package turndigest_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/drewkidwell/mads/pkg/golden"
	"github.com/drewkidwell/mads/pkg/turndigest"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
)

// The legacy digestSystemPrompt from services/router/internal/job — pinned
// here so a drift in turndigest's MonitorPush prompt (which must stay
// byte-identical for the behavior-preserving GenerateDigest refactor) fails
// loudly.
const legacyMonitorPrompt = "You compress one turn of an AI coding agent's " +
	"activity into a short digest for someone monitoring the session. Plain " +
	"prose, a few sentences at most: what was asked, what the agent did."

// countingBackend is a fastmodel.Backend that counts calls and records
// prompts, so tests can assert the compression step was (or wasn't) reached
// and what it was asked.
type countingBackend struct {
	reply string
	err   error

	mu      sync.Mutex
	calls   int
	systems []string
	prompts []string
}

func (b *countingBackend) Complete(ctx context.Context, system, prompt, model string) (string, provider.Usage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	b.systems = append(b.systems, system)
	b.prompts = append(b.prompts, prompt)
	if b.err != nil {
		return "", provider.Usage{}, b.err
	}
	return b.reply, provider.Usage{}, nil
}

func (b *countingBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func newDigester(backend fastmodel.Backend) *turndigest.Digester {
	return turndigest.New(fastmodel.NewWithBackend(backend, fastmodel.Config{}))
}

// An under-budget delta returns Source=verbatim with zero backend calls —
// the acceptance criterion's counting-fake check.
func TestDigestUnderBudgetIsVerbatimWithZeroBackendCalls(t *testing.T) {
	backend := &countingBackend{reply: "should never be produced"}
	d := newDigester(backend)

	res, err := d.Digest(context.Background(), turndigest.Request{
		Turns: []turndigest.Turn{{Number: 3, Entries: []turndigest.Entry{
			{Kind: turndigest.KindUser, Text: "hi"},
			{Kind: turndigest.KindAssistant, Text: "did stuff"},
		}}},
		Audience:     turndigest.MonitorPush,
		BudgetTokens: 4096,
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if res.Source != turndigest.SourceVerbatim {
		t.Errorf("Source = %q, want %q", res.Source, turndigest.SourceVerbatim)
	}
	if want := "user: hi\nassistant: did stuff\n"; res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
	if res.FromTurn != 3 || res.ToTurn != 3 {
		t.Errorf("anchors = %d..%d, want 3..3", res.FromTurn, res.ToTurn)
	}
	if got := backend.count(); got != 0 {
		t.Errorf("backend called %d times, want 0 (under budget must short-circuit)", got)
	}
}

// A request with no entries at all is empty verbatim, no model call.
func TestDigestNoEntriesIsEmptyVerbatim(t *testing.T) {
	backend := &countingBackend{reply: "nope"}
	d := newDigester(backend)

	res, err := d.Digest(context.Background(), turndigest.Request{BudgetTokens: 100})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if res.Text != "" || res.Source != turndigest.SourceVerbatim {
		t.Errorf("Result = %+v, want empty verbatim", res)
	}
	if res.FromTurn != 0 || res.ToTurn != 0 {
		t.Errorf("anchors = %d..%d, want 0..0", res.FromTurn, res.ToTurn)
	}
	if got := backend.count(); got != 0 {
		t.Errorf("backend called %d times, want 0", got)
	}
}

// The normalize pass: interim assistant narration between tool calls is
// dropped, tool calls tally into one "tools:" line (busiest first, no ×1),
// the terminal reply's adjacent streamed chunks rejoin, and an over-long reply
// keeps its head and tail around an explicit omission marker.
func TestNormalizeGoldenSingleTurn(t *testing.T) {
	entries := []turndigest.Entry{
		{Kind: turndigest.KindUser, Text: "fix the flaky test"},
		{Kind: turndigest.KindAssistant, Text: "let me look at the test first"},
	}
	for range 6 {
		entries = append(entries, turndigest.Entry{Kind: turndigest.KindTool, Tool: "Read", Path: "foo.go"})
	}
	entries = append(entries,
		turndigest.Entry{Kind: turndigest.KindAssistant, Text: "found it, editing"},
		turndigest.Entry{Kind: turndigest.KindTool, Tool: "Edit", Path: "bar.go"},
		turndigest.Entry{Kind: turndigest.KindTool, Tool: "Bash"},
		turndigest.Entry{Kind: turndigest.KindTool, Tool: "Bash"},
		turndigest.Entry{Kind: turndigest.KindAssistant, Text: "the test races on"},
		turndigest.Entry{Kind: turndigest.KindAssistant, Text: " the shared counter.\n"},
		turndigest.Entry{Kind: turndigest.KindAssistant,
			Text: strings.Repeat("h", 1500) + strings.Repeat("x", 3000) + strings.Repeat("t", 2500)},
	)

	d := newDigester(&countingBackend{})
	res, err := d.Digest(context.Background(), turndigest.Request{
		Turns:        []turndigest.Turn{{Number: 1, Entries: entries}},
		Audience:     turndigest.MonitorPush,
		BudgetTokens: 1 << 20, // never compress: the golden pins the normalized rendering
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if res.Source != turndigest.SourceVerbatim {
		t.Fatalf("Source = %q, want verbatim", res.Source)
	}
	if strings.Contains(res.Text, "let me look") || strings.Contains(res.Text, "found it") {
		t.Errorf("interim narration leaked into the rendering:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "[…3038 chars omitted…]") {
		t.Errorf("rendering missing the omission marker")
	}
	golden.Assert(t, "normalize_single_turn.txt", []byte(res.Text))
}

// A user message injected after the reply keeps that reply; a turn of
// only tool calls renders just its tally; a single call reads "1 call".
func TestNormalizeEdgeTurns(t *testing.T) {
	cases := []struct {
		name    string
		entries []turndigest.Entry
		want    string
	}{
		{"user after reply", []turndigest.Entry{
			{Kind: turndigest.KindUser, Text: "go"},
			{Kind: turndigest.KindAssistant, Text: "done"},
			{Kind: turndigest.KindUser, Text: "thanks"},
		}, "user: go\nuser: thanks\nassistant: done\n"},
		{"tools only", []turndigest.Entry{
			{Kind: turndigest.KindTool, Tool: "Bash"},
		}, "tools: 1 call (Bash)\n"},
		{"reply superseded by later run", []turndigest.Entry{
			{Kind: turndigest.KindAssistant, Text: "first"},
			{Kind: turndigest.KindTool, Tool: "Read"},
			{Kind: turndigest.KindAssistant, Text: "final"},
		}, "tools: 1 call (Read)\nassistant: final\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDigester(&countingBackend{})
			res, err := d.Digest(context.Background(), turndigest.Request{
				Turns: []turndigest.Turn{{Entries: tc.entries}}, BudgetTokens: 1 << 20,
			})
			if err != nil {
				t.Fatalf("Digest: %v", err)
			}
			if res.Text != tc.want {
				t.Errorf("Text = %q, want %q", res.Text, tc.want)
			}
		})
	}
}

// Assistant fragments are streamed chunks: the final reply rejoins them
// byte-for-byte, through the stored Render/Parse round trip, including a
// chunk that opens a new paragraph. Real fragments from a digest Haiku
// misread as "incomplete or fragmented" (2026-10-23).
func TestNormalizeRejoinsStreamedChunks(t *testing.T) {
	chunks := []string{"The offer v", "0 run is done: 300", " calls, no failures.",
		"\n\nMost of its ext", "ra marks are lines I", " coded as another type."}
	entries := []turndigest.Entry{{Kind: turndigest.KindTool, Tool: "Bash"}}
	for _, c := range chunks {
		entries = append(entries, turndigest.Entry{Kind: turndigest.KindAssistant, Text: c})
	}
	stored := turndigest.Parse(turndigest.Render(entries)) // as session.turn_ended carries it

	d := newDigester(&countingBackend{})
	res, err := d.Digest(context.Background(), turndigest.Request{
		Turns: []turndigest.Turn{{Entries: stored}}, BudgetTokens: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	want := "tools: 1 call (Bash)\nassistant: The offer v0 run is done: 300 calls, no failures." +
		"\n\nMost of its extra marks are lines I coded as another type.\n"
	if res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
}

// Over the total prompt cap, the oldest turns drop first behind a note;
// the newest turn always survives.
func TestRenderDropsOldestTurnsOverPromptCap(t *testing.T) {
	big := strings.Repeat("z", 3900) // under the per-entry cap: kept whole
	var turns []turndigest.Turn
	for i := 1; i <= 5; i++ {
		turns = append(turns, turndigest.Turn{Number: i, Entries: []turndigest.Entry{
			{Kind: turndigest.KindUser, Text: big},
			{Kind: turndigest.KindAssistant, Text: big},
		}})
	}
	backend := &countingBackend{reply: "DIGEST: digest"}
	d := newDigester(backend)
	if _, err := d.Digest(context.Background(), turndigest.Request{Turns: turns}); err != nil {
		t.Fatalf("Digest: %v", err)
	}
	prompt := backend.prompts[0]
	if len(prompt) > 24000+96 { // + the <transcript> wrapper
		t.Errorf("prompt is %d bytes, want ≲ the 24000-byte cap", len(prompt))
	}
	if !strings.HasPrefix(prompt, "<transcript>\n[2 earlier turns omitted]\n— turn 3 —\n") {
		t.Errorf("prompt head = %q, want the omission note then turn 3", prompt[:60])
	}
	if !strings.Contains(prompt, "— turn 5 —") {
		t.Errorf("newest turn missing from prompt")
	}

	// A lone oversized turn is never dropped.
	huge := []turndigest.Turn{{Entries: []turndigest.Entry{
		{Kind: turndigest.KindUser, Text: big}, {Kind: turndigest.KindUser, Text: big},
		{Kind: turndigest.KindUser, Text: big}, {Kind: turndigest.KindUser, Text: big},
		{Kind: turndigest.KindUser, Text: big}, {Kind: turndigest.KindUser, Text: big},
		{Kind: turndigest.KindUser, Text: big},
	}}}
	if _, err := d.Digest(context.Background(), turndigest.Request{Turns: huge}); err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if strings.Contains(backend.prompts[1], "omitted]") {
		t.Errorf("a single turn must survive the cap whole")
	}
}

// Multi-turn requests render each numbered turn under a "— turn N —"
// header; a single-turn request (above) has none.
func TestNormalizeGoldenMultiTurn(t *testing.T) {
	d := newDigester(&countingBackend{})
	res, err := d.Digest(context.Background(), turndigest.Request{
		Turns: []turndigest.Turn{
			{Number: 4, Entries: []turndigest.Entry{
				{Kind: turndigest.KindUser, Text: "add a retry"},
				{Kind: turndigest.KindTool, Tool: "Edit", Path: "client.go"},
			}},
			{Number: 5, Entries: []turndigest.Entry{
				{Kind: turndigest.KindAssistant, Text: "retry added with backoff"},
				{Kind: turndigest.KindTool, Tool: "Bash"},
			}},
		},
		Audience:     turndigest.LeaderCatchup,
		BudgetTokens: 1 << 20,
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if res.FromTurn != 4 || res.ToTurn != 5 {
		t.Errorf("anchors = %d..%d, want 4..5", res.FromTurn, res.ToTurn)
	}
	golden.Assert(t, "normalize_multi_turn.txt", []byte(res.Text))
}

// Over budget: exactly one backend call, Source=compressed, and the system
// prompt carries the audience prompt plus the prompt-side length target.
func TestDigestOverBudgetCompresses(t *testing.T) {
	backend := &countingBackend{reply: "DIGEST: short"}
	d := newDigester(backend)

	res, err := d.Digest(context.Background(), turndigest.Request{
		Turns: []turndigest.Turn{{Entries: []turndigest.Entry{
			{Kind: turndigest.KindAssistant, Text: strings.Repeat("long output ", 50)},
		}}},
		Audience:     turndigest.MonitorPush,
		BudgetTokens: 10,
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if res.Source != turndigest.SourceCompressed {
		t.Errorf("Source = %q, want %q", res.Source, turndigest.SourceCompressed)
	}
	if res.Text != "short" {
		t.Errorf("Text = %q, want the backend reply", res.Text)
	}
	if got := backend.count(); got != 1 {
		t.Fatalf("backend called %d times, want 1", got)
	}
	if !strings.Contains(backend.systems[0], "Reply in at most ~10 tokens.") {
		t.Errorf("system prompt %q missing the length target", backend.systems[0])
	}
	if !strings.HasPrefix(backend.systems[0], legacyMonitorPrompt) {
		t.Errorf("system prompt %q does not start with the MonitorPush prompt", backend.systems[0])
	}
}

// GenerateDigest's call shape (MonitorPush, budget 0, no subject): the
// system prompt is the monitor prompt plus the transcript framing and the
// digest marker, the body arrives wrapped in <transcript> tags, and the
// reply passes through untruncated with its marker stripped.
func TestDigestBudgetZeroPromptShape(t *testing.T) {
	backend := &countingBackend{reply: "DIGEST: compressed digest"}
	d := newDigester(backend)
	reply := strings.Repeat("did stuff ", 70)

	res, err := d.Digest(context.Background(), turndigest.Request{
		Turns: []turndigest.Turn{{Entries: []turndigest.Entry{
			{Kind: turndigest.KindUser, Text: "hi"},
			{Kind: turndigest.KindAssistant, Text: reply},
		}}},
		Audience: turndigest.MonitorPush,
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if res.Source != turndigest.SourceCompressed || res.Text != "compressed digest" {
		t.Errorf("Result = %+v, want the compressed reply without its marker", res)
	}
	if got := backend.count(); got != 1 {
		t.Fatalf("backend called %d times, want 1 (budget 0 always compresses)", got)
	}
	sys := backend.systems[0]
	if !strings.HasPrefix(sys, legacyMonitorPrompt) {
		t.Errorf("system prompt %q does not start with the monitor prompt", sys)
	}
	for _, want := range []string{"not addressed to you", "<transcript>", `"DIGEST: "`} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt %q missing %q", sys, want)
		}
	}
	if want := "<transcript>\nuser: hi\nassistant: " + reply + "\n</transcript>"; backend.prompts[0] != want {
		t.Errorf("prompt body = %q, want %q", backend.prompts[0], want)
	}
}

// A reply without the DIGEST: marker — the model answering the transcript
// instead of digesting it — is rejected: empty Text, nothing to push.
func TestDigestRejectsReplyWithoutMarker(t *testing.T) {
	backend := &countingBackend{reply: "I don't have a clear activity summary to digest here. Could you provide more?"}
	res, err := newDigester(backend).Digest(context.Background(), turndigest.Request{
		Turns: []turndigest.Turn{{Number: 7, Entries: []turndigest.Entry{
			{Kind: turndigest.KindAssistant, Text: strings.Repeat("long output ", 60)},
		}}},
		Audience: turndigest.MonitorPush,
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if !res.Rejected || res.Text != "" || res.Actionable {
		t.Errorf("Result = %+v, want Rejected with empty Text", res)
	}
	if res.FromTurn != 7 || res.ToTurn != 7 {
		t.Errorf("anchors = %d..%d, want 7..7", res.FromTurn, res.ToTurn)
	}
}

// A short turn goes out verbatim with no model call, even at budget 0.
func TestDigestShortTurnIsVerbatim(t *testing.T) {
	backend := &countingBackend{reply: "DIGEST: never"}
	res, err := newDigester(backend).Digest(context.Background(), turndigest.Request{
		Turns: []turndigest.Turn{{Entries: []turndigest.Entry{
			{Kind: turndigest.KindAssistant, Text: "Offer v0 is already scored."},
		}}},
		Audience: turndigest.MonitorPush,
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if res.Source != turndigest.SourceVerbatim || res.Text != "assistant: Offer v0 is already scored.\n" {
		t.Errorf("Result = %+v, want the rendering verbatim", res)
	}
	if got := backend.count(); got != 0 {
		t.Errorf("backend called %d times, want 0", got)
	}
}

// A reply blowing past ~2× the budget is hard-truncated as the backstop.
func TestDigestTruncatesRunawayReply(t *testing.T) {
	backend := &countingBackend{reply: "DIGEST: " + strings.Repeat("y", 200)}
	d := newDigester(backend)

	res, err := d.Digest(context.Background(), turndigest.Request{
		Turns: []turndigest.Turn{{Entries: []turndigest.Entry{
			{Kind: turndigest.KindAssistant, Text: strings.Repeat("long output ", 50)},
		}}},
		BudgetTokens: 10,
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if want := strings.Repeat("y", 40) + "…"; res.Text != want {
		t.Errorf("Text = %q, want the reply truncated to ~budget*4 bytes", res.Text)
	}
	if res.Source != turndigest.SourceCompressed {
		t.Errorf("Source = %q, want compressed", res.Source)
	}
}

// A skippable backend degrades to truncated verbatim with a nil error —
// empty with a zero budget — while non-skippable errors propagate.
func TestDigestSkippableBackendDegrades(t *testing.T) {
	turns := []turndigest.Turn{{Entries: []turndigest.Entry{
		{Kind: turndigest.KindAssistant, Text: strings.Repeat("long output ", 50)},
	}}}
	rendering := "assistant: " + strings.Repeat("long output ", 50) + "\n"

	t.Run("budget > 0: truncated verbatim", func(t *testing.T) {
		d := newDigester(&countingBackend{err: fastmodel.ErrBackendUnconfigured})
		res, err := d.Digest(context.Background(), turndigest.Request{Turns: turns, BudgetTokens: 10})
		if err != nil {
			t.Fatalf("Digest: %v", err)
		}
		if res.Source != turndigest.SourceVerbatim {
			t.Errorf("Source = %q, want verbatim", res.Source)
		}
		if want := rendering[:40] + "…"; res.Text != want {
			t.Errorf("Text = %q, want %q", res.Text, want)
		}
	})

	t.Run("budget 0: empty", func(t *testing.T) {
		d := newDigester(&countingBackend{err: fastmodel.ErrBackendUnconfigured})
		res, err := d.Digest(context.Background(), turndigest.Request{Turns: turns})
		if err != nil {
			t.Fatalf("Digest: %v", err)
		}
		if res.Text != "" || res.Source != turndigest.SourceVerbatim {
			t.Errorf("Result = %+v, want empty verbatim (nothing fits a zero budget)", res)
		}
	})

	t.Run("non-skippable error propagates", func(t *testing.T) {
		boom := errors.New("transport down")
		d := newDigester(&countingBackend{err: boom})
		if _, err := d.Digest(context.Background(), turndigest.Request{Turns: turns}); !errors.Is(err, boom) {
			t.Errorf("Digest error = %v, want %v", err, boom)
		}
	})
}

// Parse is Render's inverse for single-line texts; multiline text folds
// back via continuation lines.
func TestParseRenderRoundTrip(t *testing.T) {
	entries := []turndigest.Entry{
		{Kind: turndigest.KindUser, Text: "fix the bug"},
		{Kind: turndigest.KindAssistant, Text: "on it"},
		{Kind: turndigest.KindTool, Tool: "Read", Path: "foo.go"},
		{Kind: turndigest.KindTool, Tool: "Bash"},
		{Kind: turndigest.KindAssistant, Text: "done:\n- fixed\n- tested"},
	}
	got := turndigest.Parse(turndigest.Render(entries))
	if !reflect.DeepEqual(got, entries) {
		t.Errorf("Parse(Render(entries)) = %+v, want %+v", got, entries)
	}
}
