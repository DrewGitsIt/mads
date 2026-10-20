package codexcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/provider"
)

func TestRegistersCleanly(t *testing.T) {
	if err := provider.NewRegistry().Register(New(Config{})); err != nil {
		t.Fatal(err)
	}
}

func TestMatrixValidates(t *testing.T) {
	if err := New(Config{}).Matrix().Validate(); err != nil {
		t.Fatal(err)
	}
}

func script() conformance.Script {
	return conformance.Script{
		Echo:           func(marker string) string { return "echo " + marker },
		Slow:           func() string { return "slow" },
		Recall:         func() string { return "recall" },
		AttemptWrite:   func() string { return "attempt-write" },
		AttemptCommand: func() string { return "attempt-shell" },
		Timeout:        10 * time.Second, // each probe spawns a real subprocess
	}
}

// The whole conformance suite against the adapter driving the scripted
// stand-in: echo, interrupt mid-stream, OneShot/stream/resume over a
// thread, cross-process resume through the shared store, synchronous ref,
// transcript through a spawned app-server's thread/items/list, model/list,
// usage, both trust rows (limited declines the unanswered ask, read_only's
// sandbox fails the write), auto_review's limited fallback, MCP servers on
// Start, and the compaction rows — with no codex binary.
func TestConformanceAgainstScriptedBinary(t *testing.T) {
	conformance.Run(t, fakeProvider(t), script())
}

func TestToolIntentsNoStance(t *testing.T) { conformance.CheckToolIntents(t, New(Config{})) }

func startSession(t *testing.T, p *Provider, opts provider.StartOptions) *session {
	t.Helper()
	opts.Mode = provider.ModeACP
	if opts.Cwd == "" {
		opts.Cwd = t.TempDir()
	}
	sess, err := p.Start(context.Background(), opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess.(*session)
}

// drainTurn collects events through EventTurnEnd.
func drainTurn(t *testing.T, sess provider.Session) []provider.Event {
	t.Helper()
	var evs []provider.Event
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("events closed before turn end; got %+v", evs)
			}
			evs = append(evs, ev)
			if ev.Kind == provider.EventTurnEnd {
				return evs
			}
		case <-deadline:
			t.Fatalf("no turn end; got %+v", evs)
		}
	}
}

func sendTurn(t *testing.T, sess provider.Session, prompt string) []provider.Event {
	t.Helper()
	if err := sess.Send(context.Background(), prompt); err != nil {
		t.Fatalf("Send(%q): %v", prompt, err)
	}
	return drainTurn(t, sess)
}

func textOf(evs []provider.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		if ev.Kind == provider.EventText {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

func kinds(evs []provider.Event) []provider.EventKind {
	var out []provider.EventKind
	for _, ev := range evs {
		out = append(out, ev.Kind)
	}
	return out
}

func TestRefKnownAtStartReturn(t *testing.T) {
	s := startSession(t, fakeProvider(t), provider.StartOptions{})
	if s.Ref() == "" {
		t.Fatal("Ref() empty at Start-return")
	}
}

func TestThinkingAndCommentary(t *testing.T) {
	s := startSession(t, fakeProvider(t), provider.StartOptions{})
	evs := sendTurn(t, s, "think")
	var thinking []string
	for _, ev := range evs {
		if ev.Kind == provider.EventThinking {
			thinking = append(thinking, ev.Text)
		}
	}
	if len(thinking) != 1 || thinking[0] != "**Planning**" {
		t.Errorf("thinking = %q, want the one reasoning summary", thinking)
	}
	// Commentary then final answer, two items back to back: a paragraph
	// break between them, never "Checking.Done.".
	if got := textOf(evs); got != "Checking.\n\nDone." {
		t.Errorf("text = %q", got)
	}
}

func TestTextWithoutDeltasStillArrives(t *testing.T) {
	s := startSession(t, fakeProvider(t), provider.StartOptions{})
	if got := textOf(sendTurn(t, s, "echo-nodelta mk-x")); got != "echo: mk-x" {
		t.Errorf("text = %q, want the completed item's text", got)
	}
}

func TestWebSearchAnnouncedWithItsQuery(t *testing.T) {
	s := startSession(t, fakeProvider(t), provider.StartOptions{})
	evs := sendTurn(t, s, "web")
	var use, res *provider.Event
	for i := range evs {
		switch evs[i].Kind {
		case provider.EventToolUse:
			use = &evs[i]
		case provider.EventToolResult:
			res = &evs[i]
		}
	}
	if use == nil || use.Tool.Title != "go stable version" || use.Tool.Kind != provider.ToolKindSearch {
		t.Fatalf("tool use = %+v, want a search titled by its query", use)
	}
	if res == nil || res.Tool.ID != use.Tool.ID || !strings.Contains(res.Text, "https://go.dev/doc/devel/release") {
		t.Fatalf("tool result = %+v", res)
	}
}

func TestUsageIsPerTurn(t *testing.T) {
	s := startSession(t, fakeProvider(t), provider.StartOptions{})
	for range 2 {
		evs := sendTurn(t, s, "echo mk-u")
		end := evs[len(evs)-1]
		u := end.Usage
		if u == nil {
			t.Fatal("turn end without usage")
		}
		// The fake's call: input 1200 of which 400 cached, output 7 — per
		// turn, even though codex's totals keep growing.
		if u.InputTokens != 800 || u.CacheReadInputTokens != 400 || u.OutputTokens != 7 || u.ContextTokens != 1207 || u.ContextWindow != 258400 {
			t.Fatalf("usage = %+v", *u)
		}
		if u.Model != "gpt-5.6-terra" {
			t.Errorf("model = %q", u.Model)
		}
	}
}

func TestSendMidTurnSteers(t *testing.T) {
	s := startSession(t, fakeProvider(t), provider.StartOptions{})
	if err := s.Send(context.Background(), "slow"); err != nil {
		t.Fatal(err)
	}
	// Wait for streaming, then send again: the second message joins the
	// running turn instead of queueing a new one.
	if ev := <-s.Events(); ev.Kind != provider.EventText {
		t.Fatalf("first event %v", ev.Kind)
	}
	if err := s.Send(context.Background(), "change course mk-steer"); err != nil {
		t.Fatalf("mid-turn Send: %v", err)
	}
	evs := drainTurn(t, s)
	if got := textOf(evs); !strings.Contains(got, "steered: change course mk-steer") {
		t.Errorf("text %q lacks the steered reply", got)
	}
	// Exactly one turn end, and the session is idle after it.
	select {
	case ev := <-s.Events():
		t.Fatalf("event after the steered turn's end: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
	if got := textOf(sendTurn(t, s, "echo mk-after")); got != "echo: mk-after" {
		t.Errorf("next turn = %q", got)
	}
}

func TestInterruptEndsTurnInterrupted(t *testing.T) {
	s := startSession(t, fakeProvider(t), provider.StartOptions{})
	if err := s.Send(context.Background(), "slow"); err != nil {
		t.Fatal(err)
	}
	<-s.Events()
	if err := s.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	evs := drainTurn(t, s)
	if end := evs[len(evs)-1]; !end.Interrupted {
		t.Fatalf("turn end not marked interrupted: %+v", end)
	}
	// Nothing partial is stored: the transcript has the user turn only.
	msgs, err := s.p.Transcript(context.Background(), s.Ref())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Role == "agent" {
			t.Errorf("interrupted partial reply stored: %+v", m)
		}
	}
}

func TestLimitedAsksTheUser(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(map[bool]string{true: "allow", false: "deny"}[allow], func(t *testing.T) {
			var mu sync.Mutex
			var asks []provider.PermissionAsk
			asker := func(_ context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
				mu.Lock()
				asks = append(asks, ask)
				mu.Unlock()
				return provider.PermissionDecision{Allow: allow}
			}
			cwd := t.TempDir()
			s := startSession(t, fakeProvider(t), provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker, Cwd: cwd})
			evs := sendTurn(t, s, "attempt-shell")
			mu.Lock()
			defer mu.Unlock()
			if len(asks) != 1 || asks[0].Tool != "shell" || !strings.HasPrefix(asks[0].ToolUseID, "exec-") ||
				!strings.Contains(string(asks[0].Input), "touch fake-shell.txt") {
				t.Fatalf("asks = %+v", asks)
			}
			var res *provider.Event
			for i := range evs {
				if evs[i].Kind == provider.EventToolResult {
					res = &evs[i]
				}
			}
			if res == nil || res.Tool.ID != asks[0].ToolUseID {
				t.Fatalf("no tool result for the asked call: %+v", evs)
			}
			_, statErr := os.Stat(filepath.Join(cwd, "fake-shell.txt"))
			if allow && (res.IsError || statErr != nil) {
				t.Errorf("allowed call did not run: result %+v, stat %v", res, statErr)
			}
			if !allow && (!res.IsError || res.Tool.Status != provider.ToolStatusBlocked || statErr == nil) {
				t.Errorf("denied call: result %+v, stat %v", res, statErr)
			}
		})
	}
}

func TestReadOnlyNeverAsks(t *testing.T) {
	asked := false
	asker := func(context.Context, provider.PermissionAsk) provider.PermissionDecision {
		asked = true
		return provider.PermissionDecision{Allow: true}
	}
	cwd := t.TempDir()
	s := startSession(t, fakeProvider(t), provider.StartOptions{Trust: provider.TrustReadOnly, Permissions: asker, Cwd: cwd})
	sendTurn(t, s, "attempt-write")
	if asked {
		t.Error("read_only asked the user")
	}
	if _, err := os.Stat(filepath.Join(cwd, "fake-write.txt")); err == nil {
		t.Error("read_only wrote the file")
	}
}

// TestSpawnShapePerTrust reads what the adapter actually sent: the codex
// sandbox + approval pair per trust level, the trust override on every
// thread, clientUserMessageId and effort on every turn.
func TestSpawnShapePerTrust(t *testing.T) {
	cases := []struct {
		trust             provider.Trust
		sandbox, approval string
	}{
		{"", "danger-full-access", "never"},
		{provider.TrustFull, "danger-full-access", "never"},
		{provider.TrustLimited, "workspace-write", "untrusted"},
		{provider.TrustAutoReview, "workspace-write", "untrusted"}, // runs as limited
		{provider.TrustReadOnly, "read-only", "never"},
		{"something-new", "workspace-write", "untrusted"}, // never falls through to full
	}
	for _, tc := range cases {
		t.Run(string(tc.trust), func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "received.jsonl")
			p := fakeProvider(t, "MADS_FAKE_CODEX_LOG="+logPath)
			p.cfg.Effort = "low"
			s := startSession(t, p, provider.StartOptions{Trust: tc.trust, SystemPrompt: "be brief"})
			sendTurn(t, s, "echo mk-shape")
			start := received(t, logPath, "thread/start")
			if start["sandbox"] != tc.sandbox || start["approvalPolicy"] != tc.approval {
				t.Errorf("thread/start sandbox/approval = %v/%v, want %s/%s", start["sandbox"], start["approvalPolicy"], tc.sandbox, tc.approval)
			}
			if start["developerInstructions"] != "be brief" {
				t.Errorf("developerInstructions = %v", start["developerInstructions"])
			}
			projects, _ := start["config"].(map[string]any)["projects"].(map[string]any)
			if len(projects) != 1 {
				t.Errorf("config.projects = %v, want the one trust override", projects)
			}
			turn := received(t, logPath, "turn/start")
			if cid, _ := turn["clientUserMessageId"].(string); !strings.HasPrefix(cid, "mads-") || turn["effort"] != "low" {
				t.Errorf("turn/start = %v", turn)
			}
		})
	}
}

// received returns the params of the first line the fake logged for method.
func received(t *testing.T, path, method string) map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Method == method {
			return m.Params
		}
	}
	t.Fatalf("%s never sent", method)
	return nil
}

func TestMCPServersRideThreadConfig(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "received.jsonl")
	p := fakeProvider(t, "MADS_FAKE_CODEX_LOG="+logPath)
	startSession(t, p, provider.StartOptions{MCPServers: []provider.MCPServerRef{
		{Name: "links", Command: "mads mcp links --session s1", Env: []string{"A=1"}},
		{Name: "remote", Type: "http", URL: "http://127.0.0.1:9/mcp"},
		{Name: "old", Type: "sse", URL: "http://127.0.0.1:9/sse"},
	}})
	cfg := received(t, logPath, "thread/start")["config"].(map[string]any)
	servers, _ := cfg["mcp_servers"].(map[string]any)
	if got := sortedKeys(servers); strings.Join(got, ",") != "links,remote" {
		t.Fatalf("mcp_servers = %v (sse has no codex form)", servers)
	}
	links := servers["links"].(map[string]any)
	if links["command"] != "mads" || len(links["args"].([]any)) != 4 || links["env"].(map[string]any)["A"] != "1" {
		t.Errorf("links = %v", links)
	}
	if servers["remote"].(map[string]any)["url"] != "http://127.0.0.1:9/mcp" {
		t.Errorf("remote = %v", servers["remote"])
	}
}

func TestThreadConfigKeysTheResolvedPath(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(real)
	projects := threadConfig(link, nil)["projects"].(map[string]any)
	if _, ok := projects[resolved]; !ok || len(projects) != 1 {
		t.Fatalf("projects = %v, want the resolved path %s", projects, resolved)
	}
}

func TestCompactIsItsOwnTurn(t *testing.T) {
	s := startSession(t, fakeProvider(t), provider.StartOptions{})
	sendTurn(t, s, "echo mk-c")
	if err := s.Compact(context.Background(), "focus on tests"); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("Compact with instructions = %v, want ErrUnsupported", err)
	}
	if err := s.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	evs := drainTurn(t, s)
	var phases []string
	for _, ev := range evs {
		if ev.Kind == provider.EventCompact {
			phases = append(phases, ev.Compact.Phase)
			if ev.Compact.Phase == "done" && !ev.Compact.Success {
				t.Errorf("compaction failed: %+v", ev.Compact)
			}
		}
	}
	if strings.Join(phases, ",") != "start,done" {
		t.Fatalf("compact phases = %v (events %v)", phases, kinds(evs))
	}
	if end := evs[len(evs)-1]; end.Usage == nil || end.Usage.ContextTokens != 1200 {
		t.Errorf("turn end usage = %+v, want the compacted size", end.Usage)
	}
	msgs, err := s.p.Transcript(context.Background(), s.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if last := msgs[len(msgs)-1]; last.Role != "compact" {
		t.Errorf("last transcript row = %+v, want the compact marker", last)
	}
}

func TestCompactRefusedMidTurn(t *testing.T) {
	s := startSession(t, fakeProvider(t), provider.StartOptions{})
	if err := s.Send(context.Background(), "slow"); err != nil {
		t.Fatal(err)
	}
	<-s.Events()
	if err := s.Compact(context.Background(), ""); !errors.Is(err, provider.ErrTurnInFlight) {
		t.Fatalf("Compact mid-turn = %v, want ErrTurnInFlight", err)
	}
	_ = s.Interrupt(context.Background())
	drainTurn(t, s)
}

func TestForkLeavesOriginalUntouched(t *testing.T) {
	p := fakeProvider(t)
	orig := startSession(t, p, provider.StartOptions{})
	sendTurn(t, orig, "echo mk-fork-a")
	ref := orig.Ref()
	_ = orig.Close()
	before, err := p.Transcript(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}

	fork := startSession(t, p, provider.StartOptions{Resume: ref, Fork: true})
	if fork.Ref() == "" || fork.Ref() == ref {
		t.Fatalf("fork ref = %q, want a new thread", fork.Ref())
	}
	if got := textOf(sendTurn(t, fork, "recall")); !strings.Contains(got, "mk-fork-a") {
		t.Errorf("fork lost history: %q", got)
	}
	after, err := p.Transcript(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("original grew from %d to %d rows", len(before), len(after))
	}
}

func TestResumeUnknownRefFails(t *testing.T) {
	_, err := fakeProvider(t).Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Cwd: t.TempDir(), Resume: "01a1fa4e-0000-7000-8000-000000000000"})
	if err == nil {
		t.Fatal("resume of an unknown thread succeeded")
	}
}

func TestTranscriptPrefersTheLiveProcess(t *testing.T) {
	p := fakeProvider(t)
	s := startSession(t, p, provider.StartOptions{})
	sendTurn(t, s, "echo mk-live")
	if p.liveSession(s.Ref()) != s {
		t.Fatal("open session not registered as live")
	}
	msgs, err := p.Transcript(context.Background(), s.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Text != "echo: mk-live" {
		t.Fatalf("transcript = %+v", msgs)
	}
	_ = s.Close()
	if p.liveSession(s.Ref()) != nil {
		t.Error("closed session still registered")
	}
}

func TestOneShotFinalAnswerAndRef(t *testing.T) {
	p := fakeProvider(t)
	var sawRef string
	var chunks int
	res, err := p.OneShot(context.Background(), provider.OneShotRequest{Prompt: "think", Cwd: t.TempDir(), OnEvent: func(ev provider.Event) {
		switch ev.Kind {
		case provider.EventSessionRef:
			sawRef = ev.Ref
		case provider.EventText:
			chunks++
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Text is the final answer only; the commentary streamed but is not the
	// result (a JSON-parsing caller would choke on it).
	if res.Text != "Done." || res.Ref == "" || sawRef != res.Ref || chunks == 0 {
		t.Fatalf("result %+v, announced %q, chunks %d", res, sawRef, chunks)
	}
	if res.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v", res.Usage)
	}
}

func TestOneShotUsageLimitIsRateLimitError(t *testing.T) {
	_, err := fakeProvider(t).OneShot(context.Background(), provider.OneShotRequest{Prompt: "fail-limit", Cwd: t.TempDir()})
	var rle *provider.RateLimitError
	if !errors.As(err, &rle) || !strings.Contains(rle.Message, "usage limit") {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
}

func TestModels(t *testing.T) {
	models, err := fakeProvider(t).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "gpt-5.6-terra" || !models[0].Default || models[1].Name != "GPT-5.6-Luna" {
		t.Fatalf("models = %+v (hidden ones must be left out)", models)
	}
}

func TestModelsExecFailureIsDiscoveryUnavailable(t *testing.T) {
	p := New(Config{Bin: filepath.Join(t.TempDir(), "no-codex-here")})
	if _, err := p.Models(context.Background()); !errors.Is(err, provider.ErrDiscoveryUnavailable) {
		t.Fatalf("err = %v, want ErrDiscoveryUnavailable", err)
	}
}

func TestPTYUnsupported(t *testing.T) {
	_, err := New(Config{}).Start(context.Background(), provider.StartOptions{Mode: provider.ModePTY})
	if !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}
