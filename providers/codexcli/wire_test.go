package codexcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// The wire layer against real codex 0.154.0 output: each capture under
// testdata/captures is replayed line by line through a session whose conn
// has no process behind it, and what the adapter emitted — events, and the
// replies it wrote back — is checked against what the capture shows codex
// did.

type bufWC struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *bufWC) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *bufWC) Close() error { return nil }

func (b *bufWC) lines() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// replay feeds capture through a fresh session at trust with asker and
// returns every event it emitted plus what it wrote back to "codex".
func replay(t *testing.T, capture string, trust provider.Trust, asker provider.PermissionAsker) ([]provider.Event, *bufWC) {
	t.Helper()
	raw, err := os.ReadFile("testdata/captures/" + capture)
	if err != nil {
		t.Fatal(err)
	}
	w := &bufWC{}
	c := &conn{stdin: w, pending: map[int64]chan rpcMessage{}, done: make(chan struct{}), waited: make(chan struct{}), reaper: newReaper(0)}
	s := newSession(New(Config{}), c, provider.StartOptions{Trust: trust, Permissions: asker})
	c.onNotify, c.onRequest = s.handleNotification, s.handleRequest
	s.ref = captureThread(t, raw)
	s.model = "gpt-5.6-luna"

	var evs []provider.Event
	collected := make(chan struct{})
	go func() {
		for ev := range s.events {
			evs = append(evs, ev)
		}
		close(collected)
	}()
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		c.handle(sc.Bytes())
	}
	// Forwarded asks answer on goroutines of their own.
	time.Sleep(100 * time.Millisecond)
	s.onExit()
	<-collected
	return evs, w
}

func captureThread(t *testing.T, raw []byte) string {
	t.Helper()
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if id := threadOf(json.RawMessage(mustParams(sc.Bytes()))); id != "" {
			return id
		}
	}
	t.Fatal("capture names no thread")
	return ""
}

func mustParams(line []byte) []byte {
	var m rpcMessage
	_ = json.Unmarshal(line, &m)
	return m.Params
}

// turns splits events at each turn end.
func turns(evs []provider.Event) [][]provider.Event {
	var out [][]provider.Event
	var cur []provider.Event
	for _, ev := range evs {
		cur = append(cur, ev)
		if ev.Kind == provider.EventTurnEnd {
			out = append(out, cur)
			cur = nil
		}
	}
	return out
}

func TestReplayRun1(t *testing.T) {
	evs, _ := replay(t, "run1-stdout.jsonl", provider.TrustFull, nil)
	ts := turns(evs)
	if len(ts) != 5 {
		t.Fatalf("%d turn ends, want 5 (events %v)", len(ts), kinds(evs))
	}
	for i, turn := range ts[:4] {
		if turn[len(turn)-1].Interrupted {
			t.Errorf("turn %d marked interrupted", i)
		}
	}

	if got := textOf(ts[0]); got != "Hello!" {
		t.Errorf("turn a text = %q", got)
	}

	// Turn b: commentary, the `ls`, reasoning, the final answer — the
	// command under codex's item id with codex's own parse of it.
	var use, res *provider.Event
	var thinking []string
	for i, ev := range ts[1] {
		switch ev.Kind {
		case provider.EventToolUse:
			use = &ts[1][i]
		case provider.EventToolResult:
			res = &ts[1][i]
		case provider.EventThinking:
			thinking = append(thinking, ev.Text)
		}
	}
	if use == nil || use.Tool.ID != "exec-bceea15e-6692-431c-80a1-61e1e34f266a" || use.Tool.Name != "shell" ||
		use.Tool.Title != "ls" || use.Tool.Kind != provider.ToolKindRead {
		t.Errorf("turn b tool use = %+v", use)
	}
	if res == nil || res.Tool.ID != use.Tool.ID || res.IsError || res.Tool.Status != provider.ToolStatusCompleted {
		t.Errorf("turn b tool result = %+v", res)
	}
	if strings.Join(thinking, "|") != "**Clarifying step-by-step output structure**|**Noting empty directory result**" {
		t.Errorf("turn b thinking = %q", thinking)
	}
	if got := textOf(ts[1]); got != "I’m checking the workspace contents now.The workspace is empty." {
		// The two messages are separated by the tool row, not by text.
		t.Errorf("turn b text = %q", got)
	}
	// Usage is the turn's own: codex's per-process totals differenced.
	u := ts[1][len(ts[1])-1].Usage
	if u == nil || u.InputTokens != (56800-15845)-(41984-6144) || u.CacheReadInputTokens != 41984-6144 ||
		u.OutputTokens != 147-6 || u.ContextTokens != 20572 || u.ContextWindow != 258400 || u.Model != "gpt-5.6-luna" {
		t.Errorf("turn b usage = %+v", u)
	}

	if got := textOf(ts[2]); !strings.HasPrefix(got, "# Fruits\n\nFruits are colorful") || !strings.HasSuffix(got, "balanced diet.") {
		t.Errorf("turn c text = %q", got)
	}

	// Turn d: the web search, announced once its query is known.
	var web *provider.Event
	for i, ev := range ts[3] {
		if ev.Kind == provider.EventToolUse {
			web = &ts[3][i]
		}
	}
	if web == nil || web.Tool.Kind != provider.ToolKindSearch || web.Tool.Title != "site:go.dev current stable Go release version" {
		t.Errorf("turn d web search = %+v", web)
	}

	// Turn i: interrupted mid-stream; text streamed, the turn end says so.
	last := ts[4]
	if end := last[len(last)-1]; !end.Interrupted {
		t.Errorf("turn i end = %+v, want interrupted", end)
	}
	if textOf(last) == "" {
		t.Error("turn i streamed no text before the interrupt")
	}
}

func TestReplayLimitedDecline(t *testing.T) {
	t.Run("no asker declines", func(t *testing.T) {
		evs, w := replay(t, "limited-decline.jsonl", provider.TrustLimited, nil)
		var decisions []string
		for _, m := range w.lines() {
			if r, ok := m["result"].(map[string]any); ok {
				decisions = append(decisions, r["decision"].(string))
			}
		}
		if strings.Join(decisions, ",") != "decline,decline" {
			t.Fatalf("replies = %v, want both asks declined", decisions)
		}
		var results int
		for _, ev := range evs {
			if ev.Kind == provider.EventToolResult {
				results++
				if !ev.IsError || ev.Tool.Status != provider.ToolStatusBlocked {
					t.Errorf("declined command result = %+v", ev)
				}
			}
		}
		if results != 2 {
			t.Errorf("%d tool results, want 2", results)
		}
	})
	t.Run("asker sees the call", func(t *testing.T) {
		var mu sync.Mutex
		var asks []provider.PermissionAsk
		asker := func(_ context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
			mu.Lock()
			asks = append(asks, ask)
			mu.Unlock()
			return provider.PermissionDecision{Allow: false}
		}
		replay(t, "limited-decline.jsonl", provider.TrustLimited, asker)
		mu.Lock()
		defer mu.Unlock()
		if len(asks) != 2 {
			t.Fatalf("asks = %+v", asks)
		}
		if asks[0].ToolUseID != "exec-cb057118-0652-46c0-81b9-731a7c2cffca" || asks[0].Tool != "shell" ||
			!strings.Contains(string(asks[0].Input), `"command":"touch a.txt"`) {
			t.Errorf("first ask = %+v", asks[0])
		}
		if asks[1].Reason != "Create the requested empty a.txt file in the workspace." {
			t.Errorf("second ask reason = %q", asks[1].Reason)
		}
	})
	t.Run("full allows", func(t *testing.T) {
		_, w := replay(t, "limited-decline.jsonl", provider.TrustFull, nil)
		for _, m := range w.lines() {
			if r, ok := m["result"].(map[string]any); ok && r["decision"] != "accept" {
				t.Errorf("reply %v", m)
			}
		}
	})
}

func TestReplayFileChangeApproval(t *testing.T) {
	var ask provider.PermissionAsk
	asker := func(_ context.Context, a provider.PermissionAsk) provider.PermissionDecision {
		ask = a
		return provider.PermissionDecision{Allow: true}
	}
	evs, w := replay(t, "limited-filechange.jsonl", provider.TrustLimited, asker)
	if ask.Tool != "apply_patch" || !strings.HasPrefix(ask.ToolUseID, "exec-") {
		t.Fatalf("ask = %+v", ask)
	}
	lines := w.lines()
	if len(lines) != 1 || lines[0]["result"].(map[string]any)["decision"] != "accept" {
		t.Fatalf("replies = %v", lines)
	}
	for _, ev := range evs {
		if ev.Kind == provider.EventToolUse {
			if ev.Tool.Path != "/tmp/work/b.txt" || !ev.Tool.Mutates || ev.Tool.Kind != provider.ToolKindEdit {
				t.Errorf("file change use = %+v", ev.Tool)
			}
		}
		if ev.Kind == provider.EventToolResult && (ev.IsError || ev.Text != "applied") {
			t.Errorf("file change result = %+v", ev)
		}
	}
}

func TestReplaySteerAndCompact(t *testing.T) {
	evs, _ := replay(t, "steer-compact.jsonl", provider.TrustReadOnly, nil)
	ts := turns(evs)
	if len(ts) != 2 {
		t.Fatalf("%d turn ends, want the steered turn's one and the compaction's", len(ts))
	}
	if got := textOf(ts[0]); !strings.Contains(got, "STEERED") {
		t.Errorf("steered turn text = %q", got)
	}
	var phases []string
	for _, ev := range ts[1] {
		if ev.Kind == provider.EventCompact {
			phases = append(phases, ev.Compact.Phase)
		}
	}
	if strings.Join(phases, ",") != "start,done" {
		t.Errorf("compaction phases = %v", phases)
	}
	if u := ts[1][len(ts[1])-1].Usage; u == nil || u.ContextTokens != 4900 {
		t.Errorf("compaction turn usage = %+v, want ContextTokens 4900", u)
	}
}

func TestUnknownServerRequestsAreAnswered(t *testing.T) {
	w := &bufWC{}
	c := &conn{stdin: w, pending: map[int64]chan rpcMessage{}, reaper: newReaper(0)}
	s := newSession(New(Config{}), c, provider.StartOptions{Trust: provider.TrustLimited})
	for i, method := range []string{"item/tool/call", "account/chatgptAuthTokens/refresh", "mcpServer/elicitation/request", "item/tool/requestUserInput", "something/new"} {
		s.handleRequest(json.RawMessage(strings.Repeat("9", i+1)), method, json.RawMessage(`{}`))
	}
	if n := len(w.lines()); n != 5 {
		t.Fatalf("%d replies to 5 requests: %s", n, w.buf.String())
	}
}
