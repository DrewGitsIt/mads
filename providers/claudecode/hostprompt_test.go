package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/toolstatus"
	"github.com/drewkidwell/mads/providers/provider"
)

// recordingAsker records every ask and answers with decide, or — when
// decide is nil — blocks until the ask's ctx is cancelled, reporting that
// on cancelled.
type recordingAsker struct {
	mu        sync.Mutex
	asks      []provider.PermissionAsk
	decide    func(provider.PermissionAsk) provider.PermissionDecision
	asked     chan struct{}
	cancelled chan struct{}
}

func newRecordingAsker(decide func(provider.PermissionAsk) provider.PermissionDecision) *recordingAsker {
	return &recordingAsker{decide: decide, asked: make(chan struct{}, 8), cancelled: make(chan struct{}, 8)}
}

func (r *recordingAsker) ask(ctx context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
	r.mu.Lock()
	r.asks = append(r.asks, ask)
	r.mu.Unlock()
	r.asked <- struct{}{}
	if r.decide != nil {
		return r.decide(ask)
	}
	<-ctx.Done()
	r.cancelled <- struct{}{}
	return provider.PermissionDecision{}
}

func (r *recordingAsker) only(t *testing.T) provider.PermissionAsk {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.asks) != 1 {
		t.Fatalf("asks = %+v, want exactly one", r.asks)
	}
	return r.asks[0]
}

// askTurn runs one fake-claude turn and returns its events.
func askTurn(t *testing.T, sess provider.Session, prompt string) []provider.Event {
	t.Helper()
	if err := sess.Send(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	var evs []provider.Event
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			evs = append(evs, ev)
			if ev.Kind == provider.EventTurnEnd {
				return evs
			}
		case <-deadline:
			t.Fatalf("no turn end for %q", prompt)
		}
	}
}

func startAsking(t *testing.T, asker provider.PermissionAsker) provider.Session {
	t.Helper()
	sess, err := testProvider(t).Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, Trust: provider.TrustLimited, Permissions: asker,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// toolResult returns the turn's tool_result event for the asked call.
func toolResult(t *testing.T, evs []provider.Event) provider.Event {
	t.Helper()
	for _, ev := range evs {
		if ev.Kind == provider.EventToolResult && ev.Tool != nil && ev.Tool.ID == "toolu_ask" {
			return ev
		}
	}
	t.Fatalf("no tool_result for toolu_ask in %+v", evs)
	return provider.Event{}
}

func turnTextOf(evs []provider.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		if ev.Kind == provider.EventText {
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

func TestHostPromptAllow(t *testing.T) {
	asker := newRecordingAsker(func(provider.PermissionAsk) provider.PermissionDecision {
		return provider.PermissionDecision{Allow: true}
	})
	evs := askTurn(t, startAsking(t, asker.ask), "ask")

	ask := asker.only(t)
	if ask.ToolUseID != "toolu_ask" || ask.Tool != "Write" || ask.AgentID != "" || ask.ParentToolUseID != "" {
		t.Errorf("ask = %+v, want the main agent's Write toolu_ask", ask)
	}
	if ask.Reason != "Path is outside allowed working directories" {
		t.Errorf("reason = %q", ask.Reason)
	}
	if !strings.Contains(string(ask.Input), `"/outside/x.txt"`) {
		t.Errorf("input = %s, want the call's input", ask.Input)
	}
	if res := toolResult(t, evs); res.IsError {
		t.Errorf("allowed call's result is an error: %q", res.Text)
	}
	// claude runs the call with updatedInput, so an allow must echo the
	// input back unchanged.
	if got := turnTextOf(evs); got != `decision=allow updated_input={"content":"hi","file_path":"/outside/x.txt"}` {
		t.Errorf("fake saw %q", got)
	}
}

func TestHostPromptDeny(t *testing.T) {
	asker := newRecordingAsker(func(provider.PermissionAsk) provider.PermissionDecision {
		return provider.PermissionDecision{Message: "the user said no"}
	})
	evs := askTurn(t, startAsking(t, asker.ask), "ask")
	res := toolResult(t, evs)
	if !res.IsError || res.Text != "the user said no" {
		t.Errorf("denied call's result = %+v, want the deny message as an error", res)
	}
	if got := turnTextOf(evs); got != "decision=deny message=the user said no" {
		t.Errorf("fake saw %q", got)
	}
}

// An interrupt while the ask is open: claude withdraws it with
// control_cancel_request, the asker's ctx is cancelled, and no answer is
// written for a request claude stopped waiting on.
func TestHostPromptCancel(t *testing.T) {
	asker := newRecordingAsker(nil)
	sess := startAsking(t, asker.ask)
	if err := sess.Send(context.Background(), "ask-cancel"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-asker.asked:
	case <-time.After(10 * time.Second):
		t.Fatal("never asked")
	}
	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-asker.cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("asker's ctx never cancelled")
	}
	var evs []provider.Event
	for ev := range sess.Events() {
		evs = append(evs, ev)
		if ev.Kind == provider.EventTurnEnd {
			break
		}
	}
	if res := toolResult(t, evs); !res.IsError {
		t.Errorf("withdrawn call's result = %+v, want an error", res)
	}
	if got := turnTextOf(evs); got != "cancelled" {
		t.Errorf("fake saw %q", got)
	}
}

// A subagent's ask carries its agent_id, and the adapter names the Agent
// call it runs under from the subagent's task_started line.
func TestHostPromptSubagent(t *testing.T) {
	asker := newRecordingAsker(func(provider.PermissionAsk) provider.PermissionDecision {
		return provider.PermissionDecision{Allow: true}
	})
	evs := askTurn(t, startAsking(t, asker.ask), "ask-subagent")
	ask := asker.only(t)
	if ask.AgentID != "agent-1" || ask.ParentToolUseID != "toolu_agent" || ask.ToolUseID != "toolu_ask" {
		t.Errorf("ask = %+v, want agent-1 under toolu_agent", ask)
	}
	if res := toolResult(t, evs); res.ParentToolUseID != "toolu_agent" {
		t.Errorf("result parent = %q, want toolu_agent", res.ParentToolUseID)
	}
}

// Without an asker the host channel isn't wired: claude refuses the call
// itself rather than stalling on a prompt nobody can answer.
func TestHostPromptNoAskerRefuses(t *testing.T) {
	evs := askTurn(t, startAsking(t, nil), "ask")
	if res := toolResult(t, evs); !res.IsError || !strings.Contains(res.Text, "haven't granted it yet") {
		t.Errorf("result = %+v, want claude's own refusal", res)
	}
}

// claude blocks on every inbound control_request, so one mads doesn't
// understand still gets an answer: an error response.
func TestHostPromptUnknownControlRequest(t *testing.T) {
	asker := newRecordingAsker(nil)
	evs := askTurn(t, startAsking(t, asker.ask), "control-bogus")
	if got := turnTextOf(evs); got != `reply=error error=mads does not handle control request "bogus"` {
		t.Errorf("fake saw %q", got)
	}
}

// Close withdraws every open ask: nobody is left to answer it.
func TestHostPromptCloseCancelsAsks(t *testing.T) {
	asker := newRecordingAsker(nil)
	sess := startAsking(t, asker.ask)
	if err := sess.Send(context.Background(), "ask"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-asker.asked:
	case <-time.After(10 * time.Second):
		t.Fatal("never asked")
	}
	_ = sess.Close()
	select {
	case <-asker.cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("Close left the ask open")
	}
}

// TestHostPromptCapture replays the real can_use_tool capture (claude-code
// 2.1.285, a main-agent Write and a foreground subagent's Write, both
// outside cwd) through handleLine, answering every ask, and checks the asks
// and the control_responses written back.
func TestHostPromptCapture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "stream-can-use-tool.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var written safeBuffer
	asker := newRecordingAsker(func(a provider.PermissionAsk) provider.PermissionDecision {
		return provider.PermissionDecision{Allow: a.AgentID == "", Message: "no"}
	})
	s := &streamSession{
		events:    make(chan provider.Event, 1024),
		announced: make(chan string, 1),
		stdin:     &written,
		asker:     asker.ask,
		asks:      map[string]context.CancelFunc{},
	}
	s.askCtx, s.askStop = context.WithCancel(context.Background())
	defer s.askStop()
	var want int
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if strings.Contains(ln, `"can_use_tool"`) {
			want++
		}
		s.handleLine([]byte(ln))
	}
	if want != 2 {
		t.Fatalf("capture has %d can_use_tool requests, want 2", want)
	}
	deadline := time.After(5 * time.Second)
	for written.lines() < want {
		select {
		case <-deadline:
			t.Fatalf("only %d responses written: %s", written.lines(), written.String())
		case <-time.After(10 * time.Millisecond):
		}
	}

	asker.mu.Lock()
	asks := append([]provider.PermissionAsk(nil), asker.asks...)
	asker.mu.Unlock()
	var main, sub *provider.PermissionAsk
	for i := range asks {
		if !strings.HasPrefix(asks[i].ToolUseID, "toolu_") {
			t.Errorf("ask without a tool_use id: %+v", asks[i])
		}
		if asks[i].AgentID == "" {
			main = &asks[i]
		} else {
			sub = &asks[i]
		}
	}
	if main == nil || sub == nil {
		t.Fatalf("asks = %+v, want one main-agent and one subagent ask", asks)
	}
	if sub.ParentToolUseID == "" {
		t.Errorf("subagent ask %+v: parent not learned from task_started", sub)
	}
	if main.Reason == "" || sub.Reason == "" {
		t.Errorf("asks lack claude's decision_reason: %+v", asks)
	}
	out := written.String()
	if !strings.Contains(out, `"behavior":"allow"`) || !strings.Contains(out, `"behavior":"deny"`) {
		t.Errorf("responses = %s, want one allow and one deny", out)
	}
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		var resp struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(ln), &resp); err != nil || resp.Type != "control_response" ||
			resp.Response.Subtype != "success" || resp.Response.RequestID == "" {
			t.Errorf("malformed response line %q", ln)
		}
	}
}

// TestRealClaudeHostPromptSmoke drives a real limited-trust chat session
// through the adapter: the main agent and a foreground subagent each try to
// write a file outside cwd. Both asks must reach the asker with their
// tool_use_id (and the subagent's agent_id and parent); the main agent's is
// allowed, the subagent's denied, and the Runner's status rules
// (pkg/toolstatus, fed the way services/session's runner feeds them) must
// stamp the denied call blocked. Opt-in: it spends real (haiku) tokens.
// MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run HostPromptSmoke
func TestRealClaudeHostPromptSmoke(t *testing.T) {
	if os.Getenv("MADS_CLAUDE_SMOKE") != "1" {
		t.Skip("set MADS_CLAUDE_SMOKE=1 (spends tokens) to run against the real claude binary")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("no claude binary on PATH")
	}
	cwd, outside := t.TempDir(), t.TempDir()
	mainFile, subFile := filepath.Join(outside, "main.txt"), filepath.Join(outside, "sub.txt")

	tracker := toolstatus.New()
	asker := newRecordingAsker(func(a provider.PermissionAsk) provider.PermissionDecision {
		if a.AgentID == "" {
			return provider.PermissionDecision{Allow: true}
		}
		tracker.PermissionParked(a.ToolUseID, a.ParentToolUseID)
		tracker.PermissionDenied(a.ToolUseID)
		return provider.PermissionDecision{Message: toolstatus.DenialPrefix + "the user denied permission."}
	})
	sess, err := New(Config{}).Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, Model: "claude-haiku-4-5-20251001", Cwd: cwd,
		Trust: provider.TrustLimited, Permissions: asker.ask,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	prompt := "Do exactly two things, in order, then stop. " +
		"1) Use the Write tool yourself to create " + mainFile + " containing 'main'. " +
		"2) Then use the Agent tool (subagent_type general-purpose, in the foreground, not in the background) with the prompt: " +
		"'Use the Write tool to create " + subFile + " containing sub. Do nothing else and do not retry if refused.'"
	if err := sess.Send(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	deadline := time.After(4 * time.Minute)
	for done := false; !done; {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			switch ev.Kind {
			case provider.EventToolUse:
				statuses[ev.Tool.ID] = tracker.ToolUse(ev.Tool.ID, ev.Tool.Name, ev.ParentToolUseID, "")
			case provider.EventToolResult:
				statuses[ev.Tool.ID] = tracker.ToolResult(ev.Tool.ID, ev.IsError, ev.Text, "")
			case provider.EventTurnEnd:
				done = true
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}

	asker.mu.Lock()
	asks := append([]provider.PermissionAsk(nil), asker.asks...)
	asker.mu.Unlock()
	t.Logf("asks: %+v", asks)
	var main, sub *provider.PermissionAsk
	for i := range asks {
		if asks[i].AgentID == "" {
			main = &asks[i]
		} else {
			sub = &asks[i]
		}
	}
	if main == nil || sub == nil {
		t.Fatalf("asks = %+v, want one from the main agent and one from the subagent", asks)
	}
	if main.ToolUseID == "" || sub.ToolUseID == "" || sub.ParentToolUseID == "" {
		t.Errorf("asks lack ids: main %+v, sub %+v", main, sub)
	}
	if _, err := os.Stat(mainFile); err != nil {
		t.Errorf("allowed write didn't happen: %v", err)
	}
	if _, err := os.Stat(subFile); err == nil {
		t.Error("denied write happened anyway")
	}
	if got := statuses[main.ToolUseID]; got != toolstatus.Completed {
		t.Errorf("allowed call status = %q, want completed", got)
	}
	if got := statuses[sub.ToolUseID]; got != toolstatus.Blocked {
		t.Errorf("denied call status = %q, want blocked", got)
	}
}

// safeBuffer is a concurrency-safe stdin stand-in.
type safeBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) Close() error { return nil }

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *safeBuffer) lines() int {
	return strings.Count(b.String(), "\n")
}

// TestHostPromptAutoModeCapture replays a real auto-mode run (claude-code
// 2.1.285, sonnet, --permission-mode auto --permission-prompt-tool stdio,
// an autoMode hard_deny rule blocking curl to 127.0.0.1:9, and four such
// curls asked for — README "Auto-review: claude's auto mode"). The first
// two blocks are the classifier's own: a system permission_denied line and
// an error tool_result, no ask. From the third consecutive block claude
// stops trusting its reviewer and asks the host instead — so the capture
// holds exactly two can_use_tool requests, both carrying the "N
// consecutive actions were blocked" reason the card shows.
func TestHostPromptAutoModeCapture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "stream-auto-mode.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var written safeBuffer
	asker := newRecordingAsker(func(provider.PermissionAsk) provider.PermissionDecision {
		return provider.PermissionDecision{Allow: false, Message: "no"}
	})
	s := &streamSession{
		events:    make(chan provider.Event, 1024),
		announced: make(chan string, 1),
		stdin:     &written,
		asker:     asker.ask,
		asks:      map[string]context.CancelFunc{},
	}
	s.askCtx, s.askStop = context.WithCancel(context.Background())
	defer s.askStop()
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(ln) != "" {
			s.handleLine([]byte(ln))
		}
	}
	deadline := time.After(5 * time.Second)
	for written.lines() < 2 {
		select {
		case <-deadline:
			t.Fatalf("only %d responses written: %s", written.lines(), written.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	asker.mu.Lock()
	asks := append([]provider.PermissionAsk(nil), asker.asks...)
	asker.mu.Unlock()
	if len(asks) != 2 {
		t.Fatalf("asks = %d, want 2 (only the escalated blocks ask): %+v", len(asks), asks)
	}
	for i, a := range asks {
		if a.Tool != "Bash" || !strings.HasPrefix(a.ToolUseID, "toolu_") {
			t.Errorf("ask %d = %+v, want a Bash call with its tool_use id", i, a)
		}
		if !strings.Contains(a.Reason, "consecutive actions were blocked") {
			t.Errorf("ask %d reason = %q, want the classifier's escalation reason", i, a.Reason)
		}
	}
	if got := written.String(); strings.Count(got, `"behavior":"deny"`) != 2 {
		t.Errorf("responses = %s, want two denies", got)
	}
}
