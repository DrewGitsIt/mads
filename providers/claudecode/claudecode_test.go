package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/golden"
	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/provider"
)

func script() conformance.Script {
	return conformance.Script{
		Echo:           func(marker string) string { return "echo " + marker },
		Slow:           func() string { return "slow" },
		Recall:         func() string { return "recall" },
		AttemptWrite:   func() string { return "attempt-write" },
		AttemptCommand: func() string { return "attempt-command" },
		Unprompted:     func() string { return "unprompted" },
		Timeout:        10 * time.Second, // each probe spawns a real subprocess
	}
}

// The whole conformance suite runs against the adapter driving the scripted
// stand-in binary: process spawn, stream protocol, interrupt control
// requests, resume flags, transcript files — all real subprocess paths,
// no claude required.
func TestConformanceAgainstScriptedBinary(t *testing.T) {
	conformance.Run(t, testProvider(t), script())
}

// One-shot parsing is pinned to a REAL captured `claude -p --output-format
// json` result (claude-code 2.1.207) — if the wire format drifts, this is
// the test that says so.
func TestOneShotParsesCapturedResult(t *testing.T) {
	raw, err := os.ReadFile("testdata/oneshot-result.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := parseOneShot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "mk-fixture" {
		t.Errorf("text: %q", res.Text)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 42 {
		t.Errorf("usage: %+v", res.Usage)
	}
	if res.Usage.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("model from modelUsage: %q", res.Usage.Model)
	}
	if res.Usage.CostUSD != 0.0212739 {
		t.Errorf("cost: %v, want total_cost_usd from the fixture", res.Usage.CostUSD)
	}
	if res.Usage.CacheCreationInputTokens != 8827 || res.Usage.CacheReadInputTokens != 28169 {
		t.Errorf("cache usage: %+v, want cache_creation_input_tokens 8827 and cache_read_input_tokens 28169 from the fixture", res.Usage)
	}
	if res.Ref != "30fc99b6-4dce-4fd4-869d-4faa311b4ff7" {
		t.Errorf("ref: %q, want session_id from the fixture", res.Ref)
	}
	// Context occupancy comes from the LAST usage.iterations entry (the final
	// API call's prompt side), never the summed top-level aggregate; the
	// window from modelUsage[model].contextWindow.
	if want := 10 + 28169 + 8827; res.Usage.ContextTokens != want {
		t.Errorf("context tokens: %d, want %d from the fixture's last iteration", res.Usage.ContextTokens, want)
	}
	if res.Usage.ContextWindow != 200000 {
		t.Errorf("context window: %d, want 200000 from the fixture's modelUsage", res.Usage.ContextWindow)
	}
}

// Result lines from a scaled-window session key modelUsage with a bracketed
// suffix ("claude-opus-5[1m]"), and a turn that ran helper models lists
// several entries — an exact-key lookup misses both, dropping the
// runtime-reported window so a genuine 1M session gets judged against the
// static 200k table (~5× overstated occupancy).
func TestContextWindowResolvesSuffixedMultiModelUsage(t *testing.T) {
	raw := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s-1m","usage":{"input_tokens":12,"output_tokens":7,"iterations":[{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":400000,"cache_creation_input_tokens":0,"type":"message"}]},"modelUsage":{"claude-haiku-4-5-20251001":{"contextWindow":200000},"claude-opus-5[1m]":{"contextWindow":1000000}}}`)
	var l wireLine
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	if got := l.contextWindowFor("claude-opus-5"); got != 1000000 {
		t.Errorf("contextWindowFor(claude-opus-5) = %d, want 1000000 via the [1m]-suffixed key", got)
	}
	if got := l.contextWindowFor("claude-haiku-4-5-20251001"); got != 200000 {
		t.Errorf("contextWindowFor(exact key) = %d, want 200000", got)
	}
	if got := l.contextWindowFor("claude-sonnet-5"); got != 0 {
		t.Errorf("contextWindowFor(unlisted model in an ambiguous map) = %d, want 0", got)
	}

	// The streaming transport learns the model from the init line and must
	// backfill the window onto the turn-end usage the same way it backfills
	// the model — events() alone can't disambiguate a multi-model map.
	s := &streamSession{events: make(chan provider.Event, 8), announced: make(chan string, 1)}
	s.handleLine([]byte(`{"type":"system","subtype":"init","session_id":"s-1m","model":"claude-opus-5"}`))
	s.handleLine(raw)
	var turnEnd *provider.Event
drain:
	for {
		select {
		case ev := <-s.events:
			if ev.Kind == provider.EventTurnEnd {
				turnEnd = &ev
				break drain
			}
		default:
			break drain
		}
	}
	if turnEnd == nil || turnEnd.Usage == nil {
		t.Fatal("no turn-end usage from the result line")
	}
	if turnEnd.Usage.Model != "claude-opus-5" {
		t.Errorf("stream turn-end model: %q, want the init-line model", turnEnd.Usage.Model)
	}
	if turnEnd.Usage.ContextWindow != 1000000 {
		t.Errorf("stream turn-end context window: %d, want 1000000 via the [1m]-suffixed key", turnEnd.Usage.ContextWindow)
	}
}

// OnEvent switches OneShot to the stream-json wire format and fires as
// assistant text lines arrive; the final result is equivalent to the
// non-streaming path (same reply, concatenation of the streamed chunks).
func TestOneShotStreamsChunks(t *testing.T) {
	p := testProvider(t)
	var chunks []string
	res, err := p.OneShot(context.Background(), provider.OneShotRequest{
		Prompt: "echo hello there friend",
		OnEvent: func(ev provider.Event) {
			if ev.Kind == provider.EventText {
				chunks = append(chunks, ev.Text)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("want more than one chunk, got %v", chunks)
	}
	if got := strings.Join(chunks, ""); got != res.Text {
		t.Errorf("chunk concatenation %q != final Text %q", got, res.Text)
	}
	if !strings.Contains(res.Text, "hello there") {
		t.Errorf("text: %q", res.Text)
	}
}

// Rate limits and crashes surface identically through the streaming path —
// stderr is inspected the same way regardless of output format.
func TestOneShotStreamsRateLimitAndCrash(t *testing.T) {
	p := testProvider(t)
	noop := func(provider.Event) {}
	_, err := p.OneShot(context.Background(), provider.OneShotRequest{Prompt: "ratelimit", OnEvent: noop})
	var rle *provider.RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("want RateLimitError, got %v", err)
	}
	if _, err := p.OneShot(context.Background(), provider.OneShotRequest{Prompt: "crash", OnEvent: noop}); err == nil {
		t.Fatal("want an error for a scripted crash")
	}
}

// Tool use in a one-shot stream arrives as a normalized EventToolUse before
// the text/result — the same events() normalization the interactive path
// uses (TestStreamToolUseNormalization), just driven through OnEvent. An
// EventSessionRef fires first, as soon as the wire line carries session_id.
func TestOneShotStreamsToolUse(t *testing.T) {
	p := testProvider(t)
	var kinds []provider.EventKind
	var tool *provider.ToolUse
	res, err := p.OneShot(context.Background(), provider.OneShotRequest{
		Prompt: "edit api/login.go",
		OnEvent: func(ev provider.Event) {
			kinds = append(kinds, ev.Kind)
			if ev.Kind == provider.EventToolUse {
				tool = ev.Tool
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tool == nil || tool.Name != "Edit" || tool.Path != "api/login.go" {
		t.Errorf("tool event: %+v", tool)
	}
	if len(kinds) < 3 || kinds[0] != provider.EventSessionRef || kinds[1] != provider.EventToolUse {
		t.Errorf("want session ref then tool use before text, got %v", kinds)
	}
	if res.Text != "ok" {
		t.Errorf("text: %q", res.Text)
	}
}

// Transcript normalization is golden-pinned: the fixture mirrors the on-disk
// format (string and block user content, thinking blocks, tool results,
// sidechains, bookkeeping lines) and the golden holds exactly what the
// platform sees.
func TestTranscriptNormalizationGolden(t *testing.T) {
	f, err := os.Open("testdata/transcript.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	msgs, err := normalizeTranscript(f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "transcript.golden.json", append(got, '\n'))
}

// Regression for the "no input" tool-result card bug: a denied image read
// (is_error tool_result) followed by a successful bash call must each carry
// their tool_use's ID and the wire's is_error flag through to the
// normalized Message, so the frontend can pair and render them correctly.
// Harness-injected user lines (isMeta: scheduled prompt re-invocations,
// caveat preambles) and the CLI's slash-command bookkeeping lines
// (<command-name>/<local-command-stdout>, which carry NO isMeta) must not
// render as user messages — the human never typed them. Line shapes taken
// from a real 2.1.266 transcript. A tool_result riding an isMeta line must
// still flow.
func TestTranscriptSkipsMetaAndCommandLines(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>Caveat: generated by local commands</local-command-caveat>"},"timestamp":"2026-09-09T22:41:59Z"}`,
		`{"type":"user","message":{"role":"user","content":"<command-name>/compact</command-name>\n<command-message>compact</command-message>"},"timestamp":"2026-09-09T22:42:00Z"}`,
		`{"type":"user","message":{"role":"user","content":"<local-command-stdout>Compacted </local-command-stdout>"},"timestamp":"2026-09-09T22:42:01Z"}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"Fallback check: the resumed agent should have notified."},"timestamp":"2026-09-09T22:42:02Z"}`,
		`{"type":"user","message":{"role":"user","content":"a real typed message"},"timestamp":"2026-09-09T22:42:03Z"}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"ok"}]},"timestamp":"2026-09-09T22:42:04Z"}`,
	}, "\n")
	msgs, err := normalizeTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatal(err)
	}
	var users []string
	var results int
	for _, m := range msgs {
		switch m.Role {
		case "user":
			users = append(users, m.Text)
		case "tool_result":
			results++
		}
	}
	if len(users) != 1 || users[0] != "a real typed message" {
		t.Errorf("user messages = %q, want exactly the typed one", users)
	}
	if results != 1 {
		t.Errorf("tool_results = %d, want 1 (isMeta must not swallow results)", results)
	}
}

func TestTranscriptNormalizationDenialGolden(t *testing.T) {
	f, err := os.Open("testdata/transcript_denial.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	msgs, err := normalizeTranscript(f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "transcript_denial.golden.json", append(got, '\n'))

	var readResult, bashResult *provider.Message
	for i := range msgs {
		m := &msgs[i]
		if m.Role != "tool_result" || m.Tool == nil {
			continue
		}
		switch m.Tool.ID {
		case "toolu_11":
			readResult = m
		case "toolu_12":
			bashResult = m
		}
	}
	if readResult == nil {
		t.Fatal("no tool_result correlated to toolu_11 (the denied Read)")
	}
	if !readResult.IsError {
		t.Error("denied image read: IsError should be true")
	}
	if bashResult == nil {
		t.Fatal("no tool_result correlated to toolu_12 (the Bash call)")
	}
	if bashResult.IsError {
		t.Error("successful bash call: IsError should be false")
	}
}

// Live-wire counterpart to TestTranscriptNormalizationDenialGolden: the same
// real captured denial fixture, but parsed as wireLine.events() sees it
// during a live session — proving a permission-denied tool_result surfaces
// as EventToolResult on the live stream too, rather than being silently
// dropped until the next transcript refetch (or never, if the process is
// truly hung awaiting an unanswerable prompt). Concrete fix for "chat mode
// ... never prompted ... only visible after switching to terminal".
func TestWireEventsSurfacesToolResultDenial(t *testing.T) {
	f, err := os.Open("testdata/transcript_denial.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var toolResults []provider.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l wireLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		for _, ev := range l.events(false, false) {
			if ev.Kind == provider.EventToolResult {
				toolResults = append(toolResults, ev)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	if len(toolResults) != 2 {
		t.Fatalf("got %d tool_result events, want 2: %+v", len(toolResults), toolResults)
	}
	denied := toolResults[0]
	if denied.Tool == nil || denied.Tool.ID != "toolu_11" {
		t.Errorf("first tool_result: %+v, want toolu_11", denied)
	}
	if !denied.IsError {
		t.Error("denied image read: IsError should be true, was silently dropped")
	}
	if denied.Text != "permission denied: image reads require approval" {
		t.Errorf("denied text: %q", denied.Text)
	}

	ok2 := toolResults[1]
	if ok2.Tool == nil || ok2.Tool.ID != "toolu_12" {
		t.Errorf("second tool_result: %+v, want toolu_12", ok2)
	}
	if ok2.IsError {
		t.Error("successful bash call: IsError should be false")
	}
}

// Tool uses stream through as normalized events with the touched path
// extracted — the §5 tool_use attribution signal.
func TestStreamToolUseNormalization(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "edit api/login.go"); err != nil {
		t.Fatal(err)
	}
	var kinds []provider.EventKind
	var tool *provider.ToolUse
	deadline := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			kinds = append(kinds, ev.Kind)
			if ev.Kind == provider.EventToolUse {
				tool = ev.Tool
			}
			if ev.Kind == provider.EventTurnEnd {
				if ev.Usage == nil || ev.Usage.Model != "fake-sonnet" {
					t.Errorf("turn end usage should carry the init-line model: %+v", ev.Usage)
				}
				done = true
			}
		case <-deadline:
			t.Fatalf("no turn end; saw %v", kinds)
		}
	}
	if tool == nil || tool.Name != "Edit" || tool.Path != "api/login.go" {
		t.Errorf("tool event: %+v", tool)
	}
}

// A process death mid-turn closes the event stream with no turn end — the
// supervision signal.
func TestCrashMidTurnClosesWithoutTurnEnd(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "crash"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return // closed without turn end: correct
			}
			if ev.Kind == provider.EventTurnEnd {
				t.Fatal("crash produced a clean turn end")
			}
		case <-deadline:
			t.Fatal("stream never closed after crash")
		}
	}
}

// Rate limits surface as the typed error (§11.9), detected from CLI output.
func TestRateLimitSurfaces(t *testing.T) {
	_, err := testProvider(t).OneShot(context.Background(), provider.OneShotRequest{Prompt: "ratelimit"})
	var rle *provider.RateLimitError
	if !errors.As(err, &rle) || !strings.Contains(rle.Message, "Rate limit reached") {
		t.Fatalf("want RateLimitError, got %v", err)
	}
	if rateLimit("some unrelated failure") != nil {
		t.Error("false positive rate-limit detection")
	}
	// The Wave-4 phrasing: "session limit" on a result line with a 429 —
	// either signal alone must classify, and the message is the human story.
	sessionLimit := `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"You've hit your session limit · resets 3am (America/New_York)"}`
	rle = nil
	if got := rateLimit(sessionLimit); got == nil {
		t.Error("session-limit result line not classified as a rate limit")
	} else if !strings.Contains(got.Message, "session limit") {
		t.Errorf("message = %q, want the result text", got.Message)
	}
}

// turnText sends prompt and collects the turn's text until its end.
func turnText(t *testing.T, sess provider.Session, prompt string) string {
	t.Helper()
	if err := sess.Send(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	var text string
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			if ev.Kind == provider.EventText {
				text += ev.Text
			}
			if ev.Kind == provider.EventTurnEnd {
				return text
			}
		case <-deadline:
			t.Fatalf("no turn end for %q", prompt)
		}
	}
}

// A read_only session's attempted Write and Bash both refuse — the stand-in
// only emits tool_use when the tool isn't in --disallowedTools, so a
// lingering EventToolUse means the flag didn't land.
func TestTrustReadOnlyDisallowsTools(t *testing.T) {
	for _, prompt := range []string{"attempt-write", "attempt-command"} {
		p := testProvider(t)
		sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Trust: provider.TrustReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.Send(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
		deadline := time.After(10 * time.Second)
		for done := false; !done; {
			select {
			case ev, ok := <-sess.Events():
				if !ok {
					t.Fatal("stream closed before turn end")
				}
				if ev.Kind == provider.EventToolUse {
					t.Errorf("read_only: tool use event observed for %q (name=%s)", prompt, ev.Tool.Name)
				}
				if ev.Kind == provider.EventTurnEnd {
					done = true
				}
			case <-deadline:
				t.Fatalf("read_only: no turn end for %q", prompt)
			}
		}
		_ = sess.Close()
	}
}

// TestTrustFlags pins every trust level's launch flags on the structured
// transport, with and without an asker: limited is claude's manual mode
// with Bash still in the palette (it asks instead), read_only adds the hard
// --disallowedTools set, full bypasses; the host prompt channel
// (--permission-prompt-tool stdio) is wired exactly when an asker is set
// and the mode can ask at all. The stand-in's "permission-echo" dialect
// reads back the parsed flags.
func TestTrustFlags(t *testing.T) {
	asker := func(context.Context, provider.PermissionAsk) provider.PermissionDecision {
		return provider.PermissionDecision{}
	}
	cases := []struct {
		name  string
		trust provider.Trust
		asker provider.PermissionAsker
		want  string
	}{
		{"limited, asker", provider.TrustLimited, asker, "permission_mode=manual permission_prompt_tool=stdio disallowed="},
		{"limited, no asker", provider.TrustLimited, nil, "permission_mode=manual permission_prompt_tool= disallowed="},
		{"read_only, asker", provider.TrustReadOnly, asker, "permission_mode=manual permission_prompt_tool=stdio disallowed=Bash,Write,Edit,NotebookEdit"},
		// Auto-review is claude's auto mode; its escalations (a run of
		// classifier blocks) ride the same host channel limited's asks do.
		{"auto_review, asker", provider.TrustAutoReview, asker, "permission_mode=auto permission_prompt_tool=stdio disallowed="},
		{"auto_review, no asker", provider.TrustAutoReview, nil, "permission_mode=auto permission_prompt_tool= disallowed="},
		{"full, asker", provider.TrustFull, asker, "permission_mode=bypassPermissions permission_prompt_tool= disallowed="},
		{"zero value", "", nil, "permission_mode=bypassPermissions permission_prompt_tool= disallowed="},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := testProvider(t)
			sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Trust: c.trust, Permissions: c.asker})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sess.Close() }()
			if got := turnText(t, sess, "permission-echo"); got != c.want {
				t.Errorf("flags = %q, want %q", got, c.want)
			}
		})
	}
}

// ptyPermissionEcho runs the "permission-echo" dialect on a terminal
// session and returns the echoed line.
func ptyPermissionEcho(t *testing.T, opts provider.StartOptions) string {
	t.Helper()
	p := testProvider(t)
	opts.Mode = provider.ModePTY
	sess, err := p.Start(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	pty, ok := sess.(provider.PTYSession)
	if !ok {
		t.Fatal("session doesn't implement PTYSession")
	}
	if err := sess.Send(context.Background(), "permission-echo"); err != nil {
		t.Fatal(err)
	}
	out := make(chan string, 1)
	go func() {
		var acc strings.Builder
		buf := make([]byte, 8192)
		for {
			n, err := pty.Terminal().Read(buf)
			acc.Write(buf[:n])
			if strings.Contains(acc.String(), "disallowed=") || err != nil {
				out <- acc.String()
				return
			}
		}
	}()
	select {
	case text := <-out:
		return text
	case <-time.After(10 * time.Second):
		t.Fatal("permission-echo marker not seen in terminal output")
		return ""
	}
}

// TestTrustFullBypassesPermissionsPTY is the terminal-transport counterpart
// — Drew expects Auto to mean no prompts in terminal mode too.
func TestTrustFullBypassesPermissionsPTY(t *testing.T) {
	text := ptyPermissionEcho(t, provider.StartOptions{Trust: provider.TrustFull})
	if !strings.Contains(text, "permission_mode=bypassPermissions") {
		t.Errorf("permission mode not observed: %q", text)
	}
}

// TestTrustLimitedPTYAsksNatively: on the terminal a human answers claude's
// own prompt, so limited is just manual mode — never the stdio host
// channel, even when the Runner's base options carry an asker.
func TestTrustLimitedPTYAsksNatively(t *testing.T) {
	asker := func(context.Context, provider.PermissionAsk) provider.PermissionDecision {
		return provider.PermissionDecision{}
	}
	text := ptyPermissionEcho(t, provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker})
	if !strings.Contains(text, "permission_mode=manual permission_prompt_tool= ") {
		t.Errorf("want manual mode without the host channel, got %q", text)
	}
}

// TestTrustAutoReviewPTYAsksNatively: auto mode on the terminal, with
// claude's own UI answering whatever the classifier escalates.
func TestTrustAutoReviewPTYAsksNatively(t *testing.T) {
	asker := func(context.Context, provider.PermissionAsk) provider.PermissionDecision {
		return provider.PermissionDecision{}
	}
	text := ptyPermissionEcho(t, provider.StartOptions{Trust: provider.TrustAutoReview, Permissions: asker})
	if !strings.Contains(text, "permission_mode=auto permission_prompt_tool= ") {
		t.Errorf("want auto mode without the host channel, got %q", text)
	}
}

// TestPathScopeSettingsReachLaunch proves a persona allowlist reaches the
// launched process as a --settings file in the nested "hooks" shape, and
// that a session without one passes no --settings at all. The stand-in's
// "settings-echo" dialect reads back the file the adapter wrote.
func TestPathScopeSettingsReachLaunch(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, Trust: provider.TrustLimited, PersonaAllowedPaths: []string{"src/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := turnText(t, sess, "settings-echo")
	_ = sess.Close()
	if !strings.HasPrefix(text, `settings={"hooks":{"PreToolUse":[{"matcher":"Write|Edit|MultiEdit|NotebookEdit"`) {
		t.Errorf("want the nested path-scope hook, got %q", text)
	}

	p = testProvider(t)
	sess, err = p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Trust: provider.TrustLimited})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if text := turnText(t, sess, "settings-echo"); text != "settings=none" {
		t.Errorf("no allowlist: want no --settings, got %q", text)
	}
}

// orchestrateMCPRef is the exact descriptor shape
// services/session/internal/runner/capability.go's setupOrchestrate
// produces for the L2c leader capability (docs/LINKED-SESSIONS-DESIGN.md
// item 5) — proving this specific shape survives translation into a real
// claude launch, not an arbitrary one. The grant-gating logic that produces
// it is proven separately, against providers/fake, in
// services/session/internal/runner/orchestrate_test.go.
func orchestrateMCPRef() provider.MCPServerRef {
	return provider.MCPServerRef{
		Name:    "orchestrate-sess-123",
		Command: "mads internal orchestrate-mcp",
		Env:     []string{"MADS_ROUTER_URL=http://localhost:9999", "MADS_SESSION_ID=sess-123"},
	}
}

// StartOptions.MCPServers threads into a real --mcp-config file and
// --strict-mcp-config flag on the structured transport. The stand-in's
// "mcp-echo" dialect reads back the file claude itself would have loaded and
// echoes it, so this proves the exact bytes the adapter wrote reached the
// launched process, not just that some flag was passed.
func TestStartStreamMCPServersMatchesOrchestrateShape(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{
		Mode:       provider.ModeACP,
		MCPServers: []provider.MCPServerRef{orchestrateMCPRef()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "mcp-echo"); err != nil {
		t.Fatal(err)
	}
	var text string
	deadline := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			if ev.Kind == provider.EventText {
				text += ev.Text
			}
			if ev.Kind == provider.EventTurnEnd {
				done = true
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}
	assertOrchestrateMCPEcho(t, text)
}

// The terminal transport wires the identical --mcp-config/--strict-mcp-config
// args and file — the task's "BOTH transports" requirement.
// TestPTYSessionHasNoSelfDiscovery documents the current split of
// responsibility: claude prints no session id on the terminal transport, so
// a fresh ptySession's Ref() starts and stays empty, and the session no
// longer implements provider.RefAnnouncer — discovery is the Runner's job
// now, driven by calling Provider.LocateSession (locate.go, see
// TestLocateSession* in locate_test.go for the content-correlation coverage
// this superseded).
func TestPTYSessionHasNoSelfDiscovery(t *testing.T) {
	p := testProvider(t)
	cwd := t.TempDir()
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModePTY, Cwd: cwd})
	if err != nil {
		t.Fatalf("start pty: %v", err)
	}
	defer func() { _ = sess.Close() }()

	pty, ok := sess.(provider.PTYSession)
	if !ok {
		t.Fatal("session does not implement PTYSession")
	}
	go func() { _, _ = io.Copy(io.Discard, pty.Terminal()) }()

	if sess.Ref() != "" {
		t.Fatalf("Ref() = %q right after Start, want empty (fresh pty session, undiscovered)", sess.Ref())
	}
	if err := sess.Send(context.Background(), "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if sess.Ref() != "" {
		t.Fatalf("Ref() = %q after a turn, want empty: ptySession no longer self-discovers", sess.Ref())
	}
	if _, ok := sess.(provider.RefAnnouncer); ok {
		t.Fatal("ptySession implements RefAnnouncer, want false: discovery moved to the Runner+SessionLocator")
	}
}

// TestPTYSessionSetRefWriteBack proves the Runner's SessionLocator backstop
// (services/session/internal/runner's persistProviderRef) has somewhere to
// write a discovered ref back to: without this, TestPTYSessionHasNoSelfDiscovery
// above stays true forever for a live process, leaving Transcript() and
// SwitchTransport's resume handle permanently blank/empty even after
// discovery succeeds.
func TestPTYSessionSetRefWriteBack(t *testing.T) {
	s := &ptySession{}
	if s.Ref() != "" {
		t.Fatalf("Ref() = %q before SetRef, want empty", s.Ref())
	}
	s.SetRef("discovered-ref")
	if got := s.Ref(); got != "discovered-ref" {
		t.Fatalf("Ref() = %q after SetRef, want %q", got, "discovered-ref")
	}
	// A second SetRef (e.g. a stale/duplicate backstop result) must never
	// clobber the first-discovered ref.
	s.SetRef("other-ref")
	if got := s.Ref(); got != "discovered-ref" {
		t.Fatalf("Ref() = %q after a second SetRef, want unchanged %q", got, "discovered-ref")
	}
}

// TestStreamSessionSetRefWriteBack is streamSession's counterpart to
// TestPTYSessionSetRefWriteBack: the ACP backstop's own target scenario is a
// wire announcement that never arrives at all, in which case handleLine
// never learns s.ref on its own and SetRef is the only way it's ever set.
func TestStreamSessionSetRefWriteBack(t *testing.T) {
	s := &streamSession{}
	if s.Ref() != "" {
		t.Fatalf("Ref() = %q before SetRef, want empty", s.Ref())
	}
	s.SetRef("discovered-ref")
	if got := s.Ref(); got != "discovered-ref" {
		t.Fatalf("Ref() = %q after SetRef, want %q", got, "discovered-ref")
	}
	// A ref the wire already delivered (e.g. handleLine won the race) must
	// never be overwritten by a later backstop result.
	s2 := &streamSession{ref: "wire-ref"}
	s2.SetRef("backstop-ref")
	if got := s2.Ref(); got != "wire-ref" {
		t.Fatalf("Ref() = %q after SetRef raced a known wire ref, want unchanged %q", got, "wire-ref")
	}
}

func TestStartPTYMCPServersMatchesOrchestrateShape(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{
		Mode:       provider.ModePTY,
		MCPServers: []provider.MCPServerRef{orchestrateMCPRef()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	pty, ok := sess.(provider.PTYSession)
	if !ok {
		t.Fatal("session doesn't implement PTYSession")
	}
	if err := sess.Send(context.Background(), "mcp-echo"); err != nil {
		t.Fatal(err)
	}
	out := make(chan string, 1)
	go func() {
		var acc strings.Builder
		buf := make([]byte, 8192)
		for {
			n, err := pty.Terminal().Read(buf)
			acc.Write(buf[:n])
			if strings.Contains(acc.String(), "mcp_config_err=") || err != nil {
				out <- acc.String()
				return
			}
		}
	}()
	select {
	case text := <-out:
		assertOrchestrateMCPEcho(t, text)
	case <-time.After(10 * time.Second):
		t.Fatal("mcp-echo marker not seen in terminal output")
	}
}

// assertOrchestrateMCPEcho checks the "mcp_config_err=%v strict=%v
// mcp_config=%s" line the fakeclaude "mcp-echo" dialect produces
// (fakeclaude_test.go) against orchestrateMCPRef's exact shape. Command is
// checked against whatever exec.LookPath("mads") resolves to right now
// rather than a hardcoded value — writeMCPConfig does the same lookup, and
// whether a "mads" binary happens to be on this machine's PATH shouldn't
// change what "correct" means.
func assertOrchestrateMCPEcho(t *testing.T, text string) {
	t.Helper()
	if !strings.Contains(text, "mcp_config_err=<nil>") {
		t.Fatalf("mcp config file was not readable: %q", text)
	}
	if !strings.Contains(text, "strict=true") {
		t.Fatalf("--strict-mcp-config not observed: %q", text)
	}
	i := strings.Index(text, "mcp_config=")
	if i < 0 {
		t.Fatalf("no mcp_config payload: %q", text)
	}
	// Decode (not Unmarshal): the PTY transport's echo is followed by more
	// terminal output (a trailing "\r\n> " prompt), which Unmarshal rejects
	// as trailing garbage but Decode simply stops after the JSON value.
	var cfg mcpConfigFile
	dec := json.NewDecoder(strings.NewReader(text[i+len("mcp_config="):]))
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("mcp_config payload isn't the written file: %v (%q)", err, text)
	}
	entry, ok := cfg.MCPServers["orchestrate-sess-123"]
	if !ok {
		t.Fatalf("orchestrate-sess-123 server missing from config: %+v", cfg)
	}
	wantCmd := "mads"
	if resolved, err := exec.LookPath("mads"); err == nil {
		wantCmd = resolved
	}
	if entry.Command != wantCmd {
		t.Errorf("command: %q, want %q", entry.Command, wantCmd)
	}
	if len(entry.Args) != 2 || entry.Args[0] != "internal" || entry.Args[1] != "orchestrate-mcp" {
		t.Errorf("args: %v", entry.Args)
	}
	want := map[string]string{"MADS_ROUTER_URL": "http://localhost:9999", "MADS_SESSION_ID": "sess-123"}
	if len(entry.Env) != len(want) {
		t.Errorf("env: %v, want %v", entry.Env, want)
	}
	for k, v := range want {
		if entry.Env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, entry.Env[k], v)
		}
	}
}

func TestRegistersCleanly(t *testing.T) {
	if err := provider.NewRegistry().Register(New(Config{})); err != nil {
		t.Fatal(err)
	}
}

// Models probes the binary rather than returning a pinned list: the catalog
// the harness reports is shaped by its own version, the settings cascade's
// availableModels allowlist and the account's entitlement, none of which
// this adapter can know.
func TestModelsDiscoversFromBinary(t *testing.T) {
	models, err := testProvider(t).Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	var got []string
	for _, m := range models {
		got = append(got, m.ID)
	}
	want := []string{"default", "opus", "fake-sonnet-9"}
	if !slices.Equal(got, want) {
		t.Errorf("model IDs = %v, want %v (discovery order preserved)", got, want)
	}
	if models[0].Name != "Default (recommended)" {
		t.Errorf("Name = %q, want the harness's own displayName", models[0].Name)
	}
	if !models[0].Default || models[1].Default || models[2].Default {
		t.Errorf("Default should mark exactly the \"default\" entry, got %+v", models)
	}
}

// The ID must be the harness's selection value, never the concrete model an
// alias currently resolves to — substituting "fake-opus-9" for "opus" would
// freeze the picker to whatever the alias meant at discovery time and
// silently defeat the point of tracking the latest model.
func TestToModelsKeepsAliasNotResolvedModel(t *testing.T) {
	got := toModels([]wireModel{
		{Value: "opus", ResolvedModel: "fake-opus-9", DisplayName: "Fake Opus 9"},
		{Value: "bare", ResolvedModel: "bare"},
	})
	if got[0].ID != "opus" {
		t.Errorf("ID = %q, want the alias %q", got[0].ID, "opus")
	}
	// A catalog entry with no displayName still needs a label the UI can show.
	if got[1].Name != "bare" {
		t.Errorf("Name = %q, want fallback to the value", got[1].Name)
	}
}

// Config.Models pins the list for operators and tests, and must short-circuit
// the probe entirely — proven here by a Bin that could not possibly run.
func TestModelsConfigOverrideSkipsProbe(t *testing.T) {
	pinned := []provider.Model{{ID: "pinned-1", Name: "Pinned", Default: true}}
	p := New(Config{Bin: filepath.Join(t.TempDir(), "does-not-exist"), Models: pinned})
	got, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if !slices.Equal(got, pinned) {
		t.Errorf("Models = %v, want the configured pin %v", got, pinned)
	}
}

// A probe that cannot run is an expected runtime condition, not a crash and
// not a silent fall back to a stale hardcoded list: reads.go turns
// ErrDiscoveryUnavailable into an empty picker carrying the detail.
func TestModelsDiscoveryUnavailable(t *testing.T) {
	p := New(Config{Bin: filepath.Join(t.TempDir(), "does-not-exist")})
	if _, err := p.Models(context.Background()); !errors.Is(err, provider.ErrDiscoveryUnavailable) {
		t.Fatalf("Models error = %v, want provider.ErrDiscoveryUnavailable", err)
	}
}

// TestRealClaudeSmoke exercises the real binary. Opt-in: it spends real
// tokens. MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run Smoke
func TestRealClaudeSmoke(t *testing.T) {
	if os.Getenv("MADS_CLAUDE_SMOKE") != "1" {
		t.Skip("set MADS_CLAUDE_SMOKE=1 (spends tokens) to run against the real claude binary")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("no claude binary on PATH")
	}
	p := New(Config{ExtraArgs: []string{"--dangerously-skip-permissions"}})
	res, err := p.OneShot(context.Background(), provider.OneShotRequest{
		Prompt: "reply with exactly: mk-smoke", Model: "haiku", Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "mk-smoke") || res.Usage.OutputTokens == 0 {
		t.Fatalf("smoke result: %+v", res)
	}
}

// TestRealClaudeMCPSmoke proves a real claude launch actually sees the L2c
// orchestrate server's tools via --mcp-config, by asking it to list them.
// pkg/orchestratemcp.New only constructs a client lazily — MADS_ROUTER_URL
// is dereferenced on a tool *call*, not on tool *listing* — so a real
// Router isn't needed for this smoke to prove the wiring. Opt-in: it spends
// real tokens. MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run MCPSmoke
func TestRealClaudeMCPSmoke(t *testing.T) {
	if os.Getenv("MADS_CLAUDE_SMOKE") != "1" {
		t.Skip("set MADS_CLAUDE_SMOKE=1 (spends tokens) to run against the real claude binary")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("no claude binary on PATH")
	}
	binDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(binDir, "mads"), "github.com/drewkidwell/mads/cmd/mads")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mads: %v\n%s", err, out)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := New(Config{ExtraArgs: []string{"--dangerously-skip-permissions"}})
	sess, err := p.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP,
		Cwd:  t.TempDir(),
		MCPServers: []provider.MCPServerRef{{
			Name:    "orchestrate-smoke",
			Command: "mads internal orchestrate-mcp",
			Env:     []string{"MADS_ROUTER_URL=http://localhost:1", "MADS_SESSION_ID=smoke"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(context.Background(), "What MCP tools do you have available? List their exact names."); err != nil {
		t.Fatal(err)
	}
	var text string
	deadline := time.After(60 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			if ev.Kind == provider.EventText {
				text += ev.Text
			}
			if ev.Kind == provider.EventTurnEnd {
				done = true
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}
	if !strings.Contains(text, "list_links") {
		t.Fatalf("claude did not report the orchestrate server's list_links tool: %q", text)
	}
}

// TestRealClaudeMCPHotSetSmoke proves SetMCPServers against the real binary,
// mid-conversation: turn 1 sees no MCP tools, the orchestrate server is
// mounted with mcp_set_servers, and turn 2 on the same session lists its
// tools. No restart: the session ref is unchanged. Opt-in: it spends real
// tokens (two short haiku turns).
// MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run MCPHotSetSmoke
func TestRealClaudeMCPHotSetSmoke(t *testing.T) {
	if os.Getenv("MADS_CLAUDE_SMOKE") != "1" {
		t.Skip("set MADS_CLAUDE_SMOKE=1 (spends tokens) to run against the real claude binary")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("no claude binary on PATH")
	}
	binDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(binDir, "mads"), "github.com/drewkidwell/mads/cmd/mads")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mads: %v\n%s", err, out)
	}

	p := New(Config{ExtraArgs: []string{"--dangerously-skip-permissions"}})
	sess, err := p.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, Cwd: t.TempDir(), Model: "haiku",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	turn := func(prompt string) string {
		t.Helper()
		if err := sess.Send(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
		var text string
		deadline := time.After(90 * time.Second)
		for {
			select {
			case ev, ok := <-sess.Events():
				if !ok {
					t.Fatal("stream closed before turn end")
				}
				if ev.Kind == provider.EventText {
					text += ev.Text
				}
				if ev.Kind == provider.EventTurnEnd {
					return text
				}
			case <-deadline:
				t.Fatal("no turn end")
			}
		}
	}
	const ask = "Without calling any tool, list the exact names of every tool you have whose name starts with mcp__, comma-separated. If there are none, reply NONE."
	if first := turn(ask); strings.Contains(first, "list_links") {
		t.Fatalf("turn 1 already sees orchestrate tools: %q", first)
	}
	ref := sess.Ref()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sess.(provider.MCPHotSetter).SetMCPServers(ctx, []provider.MCPServerRef{{
		Name:    "orchestrate-smoke",
		Command: filepath.Join(binDir, "mads") + " internal orchestrate-mcp",
		Env:     []string{"MADS_ROUTER_URL=http://localhost:1", "MADS_SESSION_ID=smoke"},
	}}); err != nil {
		t.Fatalf("SetMCPServers: %v", err)
	}

	if second := turn("Check again now. " + ask); !strings.Contains(second, "list_links") {
		t.Fatalf("turn 2 after the hot set doesn't see list_links: %q", second)
	}
	if sess.Ref() != ref {
		t.Errorf("session ref changed %q → %q; the hot set restarted the conversation", ref, sess.Ref())
	}
}

// TestTranscriptSkipsReappendedEntries: when the CLI compacts it can append
// verbatim copies of earlier entries — same uuid — after the newest ones
// (observed on a real 2.x transcript: 622 lines). Rendered naively, old
// conversation reappears below the latest turn; only the first occurrence
// of a uuid counts. Lines without a uuid are never deduped.
func TestTranscriptSkipsReappendedEntries(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"first question"},"timestamp":"2026-09-28T03:44:10Z"}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first answer"}]},"timestamp":"2026-09-28T03:44:20Z"}`,
		`{"uuid":"u2","type":"user","message":{"role":"user","content":"latest question"},"timestamp":"2026-09-29T21:52:23Z"}`,
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"first question"},"timestamp":"2026-09-28T03:44:10Z"}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first answer"}]},"timestamp":"2026-09-28T03:44:20Z"}`,
		`{"type":"user","message":{"role":"user","content":"no uuid"}}`,
		`{"type":"user","message":{"role":"user","content":"no uuid"}}`,
	}, "\n")
	msgs, err := normalizeTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.Text)
	}
	want := []string{"first question", "first answer", "latest question", "no uuid", "no uuid"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("messages = %q, want %q", got, want)
	}
}

// TestTranscriptCompactionMarker: a compaction reads as one "compact" entry
// where it happened — metadata from the boundary line, text from the summary
// line after it — and the summary ("This session is being continued…") is
// never a user message. Line shapes from a real 2.x transcript.
func TestTranscriptCompactionMarker(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"before"},"timestamp":"2026-09-30T02:50:00Z"}`,
		`{"uuid":"b1","type":"system","subtype":"compact_boundary","content":"Conversation compacted","isMeta":false,"compactMetadata":{"trigger":"manual","preTokens":258267,"postTokens":14428,"durationMs":131618},"timestamp":"2026-09-30T02:59:42Z"}`,
		`{"uuid":"s1","parentUuid":"b1","type":"user","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued from a previous conversation."},"timestamp":"2026-09-30T02:59:42Z"}`,
		`{"uuid":"c1","type":"user","message":{"role":"user","content":"<command-name>/compact</command-name>"},"timestamp":"2026-09-30T02:57:31Z"}`,
		`{"uuid":"u2","type":"user","message":{"role":"user","content":"after"},"timestamp":"2026-09-30T03:00:10Z"}`,
		// An orphan summary (no boundary before it) still isn't a user message.
		`{"uuid":"s2","type":"user","isCompactSummary":true,"message":{"role":"user","content":[{"type":"text","text":"older summary"}]},"timestamp":"2026-09-30T04:00:00Z"}`,
	}, "\n")
	msgs, err := normalizeTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role+":"+m.Text)
	}
	want := []string{"user:before", "compact:This session is being continued from a previous conversation.", "user:after", "compact:older summary"}
	if strings.Join(roles, "|") != strings.Join(want, "|") {
		t.Fatalf("messages = %q, want %q", roles, want)
	}
	c := msgs[1].Compact
	if c == nil || c.Trigger != "manual" || c.PreTokens != 258267 || c.PostTokens != 14428 || c.DurationMS != 131618 {
		t.Errorf("compact marker = %+v", c)
	}
	if msgs[1].Timestamp.IsZero() {
		t.Error("compact marker lost its timestamp")
	}
}

// discardWriteCloser stands in for the CLI's stdin.
type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

func drainTurnEnds(s *streamSession) []provider.Event {
	var ends []provider.Event
	for {
		select {
		case ev := <-s.events:
			if ev.Kind == provider.EventTurnEnd {
				ends = append(ends, ev)
			}
		default:
			return ends
		}
	}
}

// A background task that finishes after the user's turn has ended makes the
// CLI run an unprompted turn, closed only by a task-notification result. That
// line must end the turn, or the runner (which saw the turn's tool calls and
// went "working") never goes idle again: live on ses_8321cf360ff691ca,
// 2026-09-30, "Working… 540s" on a finished session.
func TestStreamSessionLateNotificationEndsUnpromptedTurn(t *testing.T) {
	s := &streamSession{events: make(chan provider.Event, 16), announced: make(chan string, 1), stdin: discardWriteCloser{}}
	if err := s.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	s.handleLine([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"started it in the background"}]}}`))
	s.handleLine([]byte(`{"type":"result","subtype":"success","total_cost_usd":1.5,"usage":{"input_tokens":1,"cache_read_input_tokens":1000}}`))
	if n := len(drainTurnEnds(s)); n != 1 {
		t.Fatalf("user turn: %d turn ends, want 1", n)
	}

	// The background task finishes; the CLI wakes the model on its own.
	s.handleLine([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_late","name":"Bash","input":{"command":"cat out"}}]}}`))
	s.handleLine([]byte(`{"type":"result","subtype":"success","origin":{"kind":"task-notification"},"total_cost_usd":1.9,"usage":{"input_tokens":1,"cache_read_input_tokens":2000}}`))
	ends := drainTurnEnds(s)
	if len(ends) != 1 {
		t.Fatalf("unprompted turn: %d turn ends, want 1", len(ends))
	}
	if u := ends[0].Usage; u == nil || u.CacheReadInputTokens != 2000 || u.CostUSD != 0 {
		t.Errorf("unprompted turn usage = %+v, want its occupancy and no repeated cumulative cost", u)
	}
}

// The shape the notification suppression exists for: a continuation whose
// result follows the turn's own with no output of its own past it (as in
// testdata/stream-task-subagent.jsonl), or one that lands while the user's
// turn is still open. Neither is a second turn.
func TestStreamSessionNotificationWithoutUnpromptedOutputIsNoTurn(t *testing.T) {
	s := &streamSession{events: make(chan provider.Event, 16), announced: make(chan string, 1), stdin: discardWriteCloser{}}
	if err := s.Send(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	s.handleLine([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}`))
	s.handleLine([]byte(`{"type":"result","subtype":"success","origin":{"kind":"task-notification"}}`))
	if n := len(drainTurnEnds(s)); n != 0 {
		t.Fatalf("notification inside the open turn: %d turn ends, want 0", n)
	}
	s.handleLine([]byte(`{"type":"result","subtype":"success"}`))
	s.handleLine([]byte(`{"type":"result","subtype":"success","origin":{"kind":"task-notification"}}`))
	if n := len(drainTurnEnds(s)); n != 1 {
		t.Fatalf("turn then bare notification: %d turn ends, want exactly the turn's own", n)
	}
}

// streamFixtureEvents replays a fixture through a streamSession's handleLine,
// the stateful path the live transport runs (fixtureEvents is per-line).
func streamFixtureEvents(t *testing.T, name string) []provider.Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	s := &streamSession{events: make(chan provider.Event, 256), announced: make(chan string, 1)}
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(ln) != "" {
			s.handleLine([]byte(ln))
		}
	}
	var evs []provider.Event
	for {
		select {
		case ev := <-s.events:
			evs = append(evs, ev)
		default:
			return evs
		}
	}
}

// A run_in_background Bash call gets the same task_* lifecycle lines a
// subagent does (only task_started says task_type "local_bash"). None of
// them may become EventTask, or the UI shows the command as a running
// subagent: on live sessions 84 of 94 persisted "subagent" tasks were Bash.
// The call itself still comes through as an ordinary tool.
func TestStreamSessionBackgroundBashIsNoTask(t *testing.T) {
	var bash bool
	for _, ev := range streamFixtureEvents(t, "stream-task-bash.jsonl") {
		if ev.Kind == provider.EventTask {
			t.Errorf("background Bash produced EventTask: %+v", ev.Task)
		}
		if ev.Kind == provider.EventToolUse && ev.Tool != nil && ev.Tool.Name == "Bash" {
			bash = true
		}
	}
	if !bash {
		t.Error("background Bash tool_use missing")
	}
}

// The filter is keyed by task_type, so a subagent's lifecycle still comes
// through whole — including the phases that carry no task_type of their own.
func TestStreamSessionSubagentTaskKept(t *testing.T) {
	var phases []string
	for _, ev := range streamFixtureEvents(t, taskFixture) {
		if ev.Kind == provider.EventTask {
			phases = append(phases, ev.Task.Phase)
		}
	}
	if got := strings.Join(phases, ","); got != "started,progress,updated,notification" {
		t.Errorf("subagent task phases = %s, want all four", got)
	}
}

// After a restart claude-code reports the background tasks an earlier
// process left behind, by id alone — no task_started in this process says
// which are subagents. The subagent file decides: an orphaned Monitor or
// Bash call has none and stays out, a subagent has one and comes through.
// A task this process saw start never needs the file (it can trail the
// first line).
func TestStreamSessionOrphanedTasksNeedAgentFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "-proj", "ses-ref-1", "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-a01b0c447099882d2.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New(Config{TranscriptDir: root})
	checks := map[string]int{}
	s := &streamSession{
		events: make(chan provider.Event, 256), announced: make(chan string, 1), ref: "ses-ref-1",
		hasAgentFile: func(ref, id string) bool { checks[id]++; return p.hasDelegateFile(ref, id) },
	}
	for _, ln := range []string{
		`{"type":"system","subtype":"task_notification","task_id":"b28lo19d8","tool_use_id":"toolu_1","status":"stopped","summary":"Orphaned by a previous Claude Code process exit"}`,
		`{"type":"system","subtype":"task_updated","task_id":"b28lo19d8","patch":{"status":"stopped"}}`,
		`{"type":"system","subtype":"task_progress","task_id":"a01b0c447099882d2","tool_use_id":"toolu_2","description":"Running tests"}`,
		`{"type":"system","subtype":"task_notification","task_id":"a01b0c447099882d2","tool_use_id":"toolu_2","status":"stopped"}`,
		`{"type":"system","subtype":"task_started","task_id":"a9","tool_use_id":"toolu_3","task_type":"local_agent","description":"new"}`,
		`{"type":"system","subtype":"task_progress","task_id":"a9","tool_use_id":"toolu_3","description":"step"}`,
	} {
		s.handleLine([]byte(ln))
	}
	var got []string
	for len(s.events) > 0 {
		if ev := <-s.events; ev.Kind == provider.EventTask {
			got = append(got, ev.Task.TaskID+":"+ev.Task.Phase)
		}
	}
	want := "a01b0c447099882d2:progress,a01b0c447099882d2:notification,a9:started,a9:progress"
	if g := strings.Join(got, ","); g != want {
		t.Errorf("tasks = %s\nwant %s", g, want)
	}
	if checks["b28lo19d8"] != 1 || checks["a01b0c447099882d2"] != 1 || checks["a9"] != 0 {
		t.Errorf("file checks = %v, want one per unfamiliar id and none for a9", checks)
	}
}

// Background processes that aren't subagents come through as the running
// list, live: a Monitor watch and a run_in_background Bash both report
// task_type "local_bash" (testdata/stream-task-monitor.jsonl, claude-code
// 2.1.285). Each enters with its starting call and leaves when the roster
// drops it, keeping its first-seen clock across the change between.
func TestStreamSessionBackgroundList(t *testing.T) {
	var got []string
	var monitorStart time.Time
	for _, ev := range streamFixtureEvents(t, "stream-task-monitor.jsonl") {
		if ev.Kind != provider.EventBackground {
			continue
		}
		var ids []string
		for _, b := range ev.Background {
			if b.StartedAt.IsZero() {
				t.Errorf("%s has no start time", b.TaskID)
			}
			if b.TaskID == "bisir4ftf" {
				if monitorStart.IsZero() {
					monitorStart = b.StartedAt
				} else if !b.StartedAt.Equal(monitorStart) {
					t.Errorf("monitor clock restarted: %v then %v", monitorStart, b.StartedAt)
				}
			}
			ids = append(ids, b.TaskID+"@"+b.ToolUseID)
		}
		got = append(got, "["+strings.Join(ids, " ")+"]")
	}
	want := "[bisir4ftf@]," +
		"[bisir4ftf@toolu_01Yc82wUcCqUAjD8qJb9NRQp]," +
		"[bisir4ftf@toolu_01Yc82wUcCqUAjD8qJb9NRQp bratvqqsq@]," +
		"[bisir4ftf@toolu_01Yc82wUcCqUAjD8qJb9NRQp bratvqqsq@toolu_01SiRgsByfdBLjWndxgHUAdS]," +
		"[bisir4ftf@toolu_01Yc82wUcCqUAjD8qJb9NRQp]," +
		"[]"
	if g := strings.Join(got, ","); g != want {
		t.Errorf("background lists =\n%s\nwant\n%s", g, want)
	}

	// A subagent is never a background process.
	for _, ev := range streamFixtureEvents(t, taskFixture) {
		if ev.Kind == provider.EventBackground {
			t.Errorf("subagent capture produced a background list: %+v", ev.Background)
		}
	}
}

// background_tasks_changed comes through as the subagent-only roster, in
// stream order and never nil — the emptied roster is the evidence. Its
// position matters to consumers: it drops the task BEFORE task_updated and
// task_notification say how the task ended (both captures show it).
func TestStreamSessionTaskRoster(t *testing.T) {
	var got []string
	for _, ev := range streamFixtureEvents(t, taskFixture) {
		switch ev.Kind {
		case provider.EventTaskRoster:
			if ev.TaskRoster == nil {
				t.Fatal("roster must never be nil")
			}
			got = append(got, "roster["+strings.Join(ev.TaskRoster, " ")+"]")
		case provider.EventTask:
			got = append(got, ev.Task.Phase)
		}
	}
	want := "roster[a7f5f61655a886928],started,progress,roster[],updated,notification"
	if s := strings.Join(got, ","); s != want {
		t.Errorf("roster/lifecycle order = %s\nwant %s", s, want)
	}

	// A background Bash task is not a subagent: its rosters come through
	// empty rather than naming it.
	for _, ev := range streamFixtureEvents(t, "stream-task-bash.jsonl") {
		if ev.Kind == provider.EventTaskRoster && len(ev.TaskRoster) != 0 {
			t.Errorf("bash task leaked into the roster: %v", ev.TaskRoster)
		}
	}
}

// The CLI's <task-notification> user line (a background task finished) is
// the harness talking to the model. It must not come back as a user message
// full of raw XML; it becomes one "notification" entry carrying the summary.
func TestTranscriptTaskNotification(t *testing.T) {
	content := "<task-notification>\n<task-id>bs10rzv0r</task-id>\n<status>completed</status>\n" +
		"<summary>Background command \"make ui &amp;&amp; echo ok &gt; /tmp/x\" completed (exit code 0)</summary>\n</task-notification>"
	line, _ := json.Marshal(map[string]any{
		"uuid": "n1", "type": "user", "origin": map[string]string{"kind": "task-notification"},
		"message": map[string]any{"role": "user", "content": content}, "timestamp": "2026-09-30T04:57:12Z",
	})
	bare, _ := json.Marshal(map[string]any{
		"uuid": "n2", "type": "user", "origin": map[string]string{"kind": "task-notification"},
		"message": map[string]any{"role": "user", "content": "<task-notification><status>failed</status></task-notification>"},
	})
	msgs, err := normalizeTranscript(strings.NewReader(string(line) + "\n" + string(bare)))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v, want 2 notification entries", msgs)
	}
	if m := msgs[0]; m.Role != "notification" || m.Text != `Background command "make ui && echo ok > /tmp/x" completed (exit code 0)` || m.Timestamp.IsZero() {
		t.Errorf("notification = %+v", m)
	}
	if m := msgs[1]; m.Role != "notification" || m.Text != "Background task failed" {
		t.Errorf("summary-less notification = %+v", m)
	}
}

// A screenshot pasted into claude (or loaded by Read) is one JSONL line of
// base64 that can outgrow any fixed scanner cap — that one line used to fail
// the whole transcript, so the conversation wouldn't load at all.
func TestNormalizeTranscriptReadsAnOversizedImageLine(t *testing.T) {
	image := strings.Repeat("A", 20<<20)
	jsonl := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"before"},"timestamp":"2026-10-06T10:00:00Z"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + image + `"}},{"type":"text","text":"look at this"}]},"timestamp":"2026-10-06T10:00:01Z"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"after"}]},"timestamp":"2026-10-06T10:00:02Z"}`,
	}, "\n")
	msgs, err := normalizeTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, m := range msgs {
		texts = append(texts, m.Role+":"+m.Text)
	}
	want := []string{"user:before", "user:look at this", "agent:after"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", texts, want)
	}
}

// A /compact turn (testdata/stream-compact.jsonl: claude-code 2.1.285, haiku,
// two short turns then /compact) closes with a result whose usage counts no
// tokens. Its turn end must carry what the compaction left behind — the
// compact_boundary's post_tokens — or the dock's rim reads 0 tokens used.
func TestStreamSessionCompactTurnReportsPostTokens(t *testing.T) {
	var ends []provider.Event
	for _, ev := range streamFixtureEvents(t, "stream-compact.jsonl") {
		if ev.Kind == provider.EventTurnEnd {
			ends = append(ends, ev)
		}
	}
	if len(ends) != 3 {
		t.Fatalf("turn ends = %d, want 3", len(ends))
	}
	if u := ends[1].Usage; u == nil || u.ContextTokens != 22486 {
		t.Errorf("ordinary turn's usage = %+v, want its own last-call occupancy 22486", u)
	}
	u := ends[2].Usage
	if u == nil || u.ContextTokens != 2214 || u.ContextWindow != 200000 {
		t.Fatalf("compact turn's usage = %+v, want context 2214 of 200000", u)
	}
}

// A compaction inside a working turn (auto-compaction) is followed by more
// model calls; the turn's own last-call figure stands, and the boundary's
// figure doesn't leak into a later turn.
func TestStreamSessionCompactionMidTurnKeepsTurnUsage(t *testing.T) {
	s := &streamSession{events: make(chan provider.Event, 16), announced: make(chan string, 1)}
	s.handleLine([]byte(`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto","post_tokens":9000}}`))
	s.handleLine([]byte(`{"type":"result","subtype":"success","usage":{"input_tokens":5,"iterations":[{"input_tokens":5,"cache_read_input_tokens":12000}]}}`))
	s.handleLine([]byte(`{"type":"result","subtype":"success","usage":{"input_tokens":0,"iterations":[]}}`))
	ends := drainTurnEnds(s)
	if len(ends) != 2 {
		t.Fatalf("turn ends = %d, want 2", len(ends))
	}
	if got := ends[0].Usage.ContextTokens; got != 12005 {
		t.Errorf("mid-turn compaction: context = %d, want the turn's own 12005", got)
	}
	if got := ends[1].Usage.ContextTokens; got != 0 {
		t.Errorf("later empty turn: context = %d, want 0 (no compaction of its own)", got)
	}
}

// Each of a subagent's API calls surfaces as EventSubagentUsage, tagged with
// its spawning call: its own model (here not the parent's) and context. The
// window comes from the latest result line's modelUsage — a subagent's own
// lines never say — so the capture's first-turn calls have none.
func TestStreamSessionSubagentUsage(t *testing.T) {
	var got []string
	for _, ev := range streamFixtureEvents(t, taskFixture) {
		if ev.Kind != provider.EventSubagentUsage {
			continue
		}
		if ev.ParentToolUseID == "" || ev.Usage == nil {
			t.Fatalf("untagged or empty usage event: %+v", ev)
		}
		got = append(got, fmt.Sprintf("%s:%d/%d", ev.Usage.Model, ev.Usage.ContextTokens, ev.Usage.ContextWindow))
	}
	if len(got) == 0 || got[0] != "claude-haiku-4-5-20251001:10802/0" {
		t.Fatalf("subagent usage = %v", got)
	}

	s := &streamSession{events: make(chan provider.Event, 16), announced: make(chan string, 1)}
	s.handleLine([]byte(`{"type":"result","subtype":"success","usage":{"input_tokens":1},"modelUsage":{"claude-opus-5[1m]":{"contextWindow":1000000}}}`))
	s.handleLine([]byte(`{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"cache_read_input_tokens":270000}}}`))
	s.handleLine([]byte(`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"main"}],"usage":{"input_tokens":5}}}`))
	close(s.events)
	n := 0
	for ev := range s.events {
		if ev.Kind != provider.EventSubagentUsage {
			continue
		}
		n++
		if ev.ParentToolUseID != "toolu_1" || ev.Usage.ContextTokens != 270005 || ev.Usage.ContextWindow != 1000000 {
			t.Errorf("subagent usage after a result = %+v usage %+v", ev, ev.Usage)
		}
	}
	if n != 1 {
		t.Errorf("%d subagent usage events, want 1 (the main agent's call isn't one)", n)
	}
}
