package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
}

// OnChunk switches OneShot to the stream-json wire format and fires as
// assistant text lines arrive; the final result is equivalent to the
// non-streaming path (same reply, concatenation of the streamed chunks).
func TestOneShotStreamsChunks(t *testing.T) {
	p := testProvider(t)
	var chunks []string
	res, err := p.OneShot(context.Background(), provider.OneShotRequest{
		Prompt: "echo hello there friend",
		OnChunk: func(text string) {
			chunks = append(chunks, text)
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
	noop := func(string) {}
	_, err := p.OneShot(context.Background(), provider.OneShotRequest{Prompt: "ratelimit", OnChunk: noop})
	var rle *provider.RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("want RateLimitError, got %v", err)
	}
	if _, err := p.OneShot(context.Background(), provider.OneShotRequest{Prompt: "crash", OnChunk: noop}); err == nil {
		t.Fatal("want an error for a scripted crash")
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

// A read_only session's attempted Write and a limited session's attempted
// Bash both refuse — the stand-in only emits tool_use when the tool isn't in
// --disallowedTools, so a lingering EventToolUse means the flag didn't land.
func TestTrustDisallowsTools(t *testing.T) {
	cases := []struct {
		trust  provider.Trust
		prompt string
	}{
		{provider.TrustLimited, "attempt-command"},
		{provider.TrustReadOnly, "attempt-write"},
		{provider.TrustReadOnly, "attempt-command"},
	}
	for _, c := range cases {
		p := testProvider(t)
		sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Trust: c.trust})
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.Send(context.Background(), c.prompt); err != nil {
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
					t.Errorf("trust %s: tool use event observed for %q (name=%s)", c.trust, c.prompt, ev.Tool.Name)
				}
				if ev.Kind == provider.EventTurnEnd {
					done = true
				}
			case <-deadline:
				t.Fatalf("trust %s: no turn end for %q", c.trust, c.prompt)
			}
		}
		_ = sess.Close()
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
// TestPTYSessionDiscoversRefFromTranscript proves BehaviorRefAnnounce's PTY
// path end to end: a fresh terminal session has no ref at Start, but once
// the scripted stand-in writes its transcript (mimicking real timing — see
// fcTerminal) and a second turn proves the file is still actively growing,
// discovery announces it and Ref() reflects the same value.
func TestPTYSessionDiscoversRefFromTranscript(t *testing.T) {
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
	// Let the discovery loop's first tick (750ms) observe the file at its
	// post-first-turn size before a second turn grows it further — proof
	// the candidate is actively being written, not a stale leftover.
	time.Sleep(900 * time.Millisecond)
	if err := sess.Send(context.Background(), "echo bye"); err != nil {
		t.Fatalf("send: %v", err)
	}

	announcer, ok := sess.(provider.RefAnnouncer)
	if !ok {
		t.Fatal("ptySession does not implement RefAnnouncer")
	}
	select {
	case ref, ok := <-announcer.RefAnnounced():
		if !ok || ref == "" {
			t.Fatal("RefAnnounced closed with no ref, want a discovered ref")
		}
		if got := sess.Ref(); got != ref {
			t.Fatalf("Ref() = %q after announcement, want %q", got, ref)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for pty ref discovery")
	}
}

// TestPTYSessionAmbiguousDiscoveryNeverAnnounces proves the decline path: a
// second candidate transcript with the same cwd, planted alongside the
// session's own real one, must make discovery decline rather than guess —
// a wrong ref would silently resume someone else's conversation, which is
// worse than no ref.
func TestPTYSessionAmbiguousDiscoveryNeverAnnounces(t *testing.T) {
	p := testProvider(t)
	cwd := t.TempDir()
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModePTY, Cwd: cwd})
	if err != nil {
		t.Fatalf("start pty: %v", err)
	}

	pty, ok := sess.(provider.PTYSession)
	if !ok {
		t.Fatal("session does not implement PTYSession")
	}
	go func() { _, _ = io.Copy(io.Discard, pty.Terminal()) }()

	if err := sess.Send(context.Background(), "echo hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // let the real transcript's first write land first
	writeTranscriptFixture(t, p.cfg.TranscriptDir, "proj-collision", "planted-collision", cwd)

	announcer := sess.(provider.RefAnnouncer)
	select {
	case ref, ok := <-announcer.RefAnnounced():
		t.Fatalf("RefAnnounced fired (ok=%v ref=%q) despite an ambiguous pair of candidates", ok, ref)
	case <-time.After(2 * time.Second):
		// expected: discovery must keep declining while both candidates exist.
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case ref, ok := <-announcer.RefAnnounced():
		if ok && ref != "" {
			t.Fatalf("RefAnnounced delivered %q after Close, want a closed empty channel", ref)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RefAnnounced never closed after Close")
	}
	if got := sess.Ref(); got != "" {
		t.Fatalf("Ref() = %q, want empty: ambiguous discovery must never guess", got)
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

// Guards against DefaultModels regressing to bare family names (e.g.
// "Claude Sonnet" instead of "Claude Sonnet 5") — the UI shows this Name
// verbatim and must never display an ambiguous, unversioned model identity.
func TestDefaultModelsAreVersionQualified(t *testing.T) {
	digit := regexp.MustCompile(`[0-9]`)
	for _, m := range DefaultModels {
		if !digit.MatchString(m.Name) {
			t.Errorf("model %q name %q has no version number", m.ID, m.Name)
		}
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
