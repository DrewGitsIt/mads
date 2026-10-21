package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/session/internal/runner"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata golden files")

// legacyScripts are one turn each, shaped the way each adapter streams
// (keys as claude, codex and the fake derive them; kiro has none), with
// the settle hints a keyed session adds. Adjacent text items with
// different keys exercise the legacy text-join shim.
var legacyScripts = map[string][]provider.Event{
	"claude": {
		{Kind: provider.EventThinking, ThinkingTokens: 12},
		{Kind: provider.EventThinking, Key: "msg_01:0", Text: "Let me look."},
		{Kind: provider.EventText, Key: "msg_01:1", Text: "I'll check"},
		{Kind: provider.EventText, Key: "msg_01:1", Text: " the file."},
		{Kind: provider.EventSettle, Key: "msg_01:1"},
		{Kind: provider.EventToolUse, Key: "toolu_01", Tool: &provider.ToolUse{ID: "toolu_01", Name: "Read", Input: rawJSON(`{"file_path":"a.go"}`), Path: "a.go", Kind: provider.ToolKindRead, Title: "a.go"}},
		{Kind: provider.EventSettle, Key: "toolu_01"},
		{Kind: provider.EventTask, Task: &provider.TaskStatus{Phase: "started", TaskID: "task-1", ToolUseID: "toolu_01", Description: "look"}},
		{Kind: provider.EventText, Key: "msg_sub:0", Text: "sub says", ParentToolUseID: "toolu_01", SubagentType: "general-purpose", TaskDescription: "look"},
		{Kind: provider.EventToolResult, Key: "toolu_01", Tool: &provider.ToolUse{ID: "toolu_01", Name: "Read"}, Text: "package a"},
		{Kind: provider.EventSettle, Key: "toolu_01"},
		{Kind: provider.EventText, Key: "msg_02:0", Text: "Done."},
		{Kind: provider.EventSettle, Key: "msg_02:0"},
		{Kind: provider.EventText, Key: "msg_03:0", Text: "Also this."},
		{Kind: provider.EventSettle, Key: "msg_03:0"},
		{Kind: provider.EventSettle},
		{Kind: provider.EventTurnEnd, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5, Model: "claude-x", CostUSD: 0.01}},
	},
	"codex": {
		{Kind: provider.EventText, Key: "item_1", Text: "Checking now."},
		{Kind: provider.EventSettle, Key: "item_1"},
		{Kind: provider.EventText, Key: "item_2", Text: "The workspace"},
		{Kind: provider.EventText, Key: "item_2", Text: " is clean.\n"},
		{Kind: provider.EventSettle, Key: "item_2"},
		{Kind: provider.EventText, Key: "item_2b", Text: "Next."},
		{Kind: provider.EventSettle, Key: "item_2b"},
		{Kind: provider.EventToolUse, Key: "item_3", Tool: &provider.ToolUse{ID: "item_3", Name: "shell", Input: rawJSON(`{"command":"ls"}`), Kind: provider.ToolKindExecute, Title: "ls"}},
		{Kind: provider.EventToolResult, Key: "item_3", Tool: &provider.ToolUse{ID: "item_3", Name: "shell"}, Text: "a.txt", IsError: false},
		{Kind: provider.EventSettle, Key: "item_3"},
		{Kind: provider.EventText, Key: "item_4", Text: "Partial"},
		{Kind: provider.EventTurnEnd, Interrupted: true},
	},
	"fake": {
		{Kind: provider.EventText, Key: "fake-1:t3:b0", Text: "Looking. "},
		{Kind: provider.EventSettle, Key: "fake-1:t3:b0"},
		{Kind: provider.EventToolUse, Key: "fake-1:t3:tool0", Tool: &provider.ToolUse{ID: "fake-1:t3:tool0", Name: "ls"}},
		{Kind: provider.EventSettle, Key: "fake-1:t3:tool0"},
		{Kind: provider.EventToolResult, Key: "fake-1:t3:tool0", Tool: &provider.ToolUse{Name: "ls"}, Text: "a.txt"},
		{Kind: provider.EventText, Key: "fake-1:t3:b1", Text: "One file."},
		{Kind: provider.EventSettle, Key: "fake-1:t3:b1"},
		{Kind: provider.EventError, Text: "scripted error"},
		{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "start"}},
		{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "done", Success: true}},
		{Kind: provider.EventSettle},
		{Kind: provider.EventTurnEnd, Usage: &provider.Usage{InputTokens: 3, OutputTokens: 2, Model: "fake-fast"}},
	},
	"kiro": {
		{Kind: provider.EventText, Text: "plain "},
		{Kind: provider.EventText, Text: "text"},
		{Kind: provider.EventToolUse, Tool: &provider.ToolUse{ID: "call-1", Name: "fs_read", Kind: provider.ToolKindRead}},
		{Kind: provider.EventToolResult, Tool: &provider.ToolUse{ID: "call-1", Name: "fs_read"}, Text: "ok"},
		{Kind: provider.EventText, Text: "after"},
		{Kind: provider.EventTurnEnd},
	},
}

var legacyTurns = []string{"claude", "codex", "fake", "kiro"}

// TestLegacyOutputGolden pins GET /output (no ?v) to main's bytes
// (e73db680): the same kinds, data, per-connection ids and text joins. The
// golden was written on main with -update, which plays the scripts minus
// their settle hints — the one kind main would have written and v2 keeps
// off the legacy stream. Here the scripts play in full, with a v2
// subscriber attached too, and the legacy bytes must not move.
func TestLegacyOutputGolden(t *testing.T) {
	p := newScriptProvider("scripted", legacyScripts)
	if *updateGolden {
		p.strip = map[provider.EventKind]bool{provider.EventSettle: true}
	}
	srv, _ := startScripted(t, p)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	legacy := openRaw(t, ctx, srv.URL+"/output", nil)
	var v2 *rawStream
	if !*updateGolden {
		v2 = openRaw(t, ctx, srv.URL+"/output?v=2", nil)
	}

	for i, turn := range legacyTurns {
		postMessage(t, srv, turn)
		legacy.waitCount(t, "event: turn_end\n", i+1)
	}
	got := upTo(legacy.bytes(), "event: turn_end\n", len(legacyTurns))

	golden := filepath.Join("testdata", "legacy-output.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("legacy /output moved from main's bytes:\n--- got\n%s\n--- want\n%s", got, want)
	}

	// The v2 stream saw the same turns, as its own frames.
	v2.waitCount(t, "event: turn_end\n", len(legacyTurns))
	for _, s := range []string{"event: hello\n", "event: user_message\n", "event: item_start\n", "event: text_delta\n", "event: tool_result\n"} {
		if !strings.Contains(string(v2.bytes()), s) {
			t.Errorf("v2 stream has no %q", strings.TrimSpace(s))
		}
	}
}

// startScripted runs a runner over p behind the session API.
func startScripted(t *testing.T, p provider.Provider) (*httptest.Server, *runner.Runner) {
	t.Helper()
	return startRunner(t, p, runner.ChatLimits{})
}

// startRunner is startScripted with the v2 journal's limits set.
func startRunner(t *testing.T, p provider.Provider, limits runner.ChatLimits) (*httptest.Server, *runner.Runner) {
	t.Helper()
	ctx := context.Background()
	base := datatest.Start(t)
	rec := contracts.Session{Name: "scripted", Provider: p.Name()}
	if err := dataclient.New(base, "test-bootstrap").CreateSession(ctx, &rec); err != nil {
		t.Fatal(err)
	}
	reg := provider.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	r := runner.New(rec.ID, dataclient.New(base, rec.ID), reg)
	r.SetChatLimits(limits)
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	srv := httptest.NewServer(New(r))
	t.Cleanup(srv.Close)
	return srv, r
}

func postMessage(t *testing.T, srv *httptest.Server, text string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"text": text})
	resp, err := srv.Client().Post(srv.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /messages %q: status %d", text, resp.StatusCode)
	}
}

// rawStream collects an SSE response's bytes as they arrive.
type rawStream struct {
	mu    chan struct{} // 1-slot lock, so waitCount can poll
	buf   bytes.Buffer
	done  chan struct{}
	close func()
}

func openRaw(t *testing.T, ctx context.Context, url string, header http.Header) *rawStream {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	s := &rawStream{mu: make(chan struct{}, 1), done: make(chan struct{})}
	s.close = func() { cancel(); _ = resp.Body.Close() }
	t.Cleanup(s.close)
	go func() {
		defer close(s.done)
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				s.mu <- struct{}{}
				s.buf.Write(line)
				<-s.mu
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

func (s *rawStream) bytes() []byte {
	s.mu <- struct{}{}
	defer func() { <-s.mu }()
	return append([]byte(nil), s.buf.Bytes()...)
}

// upTo is b through the end of the frame holding marker's nth occurrence.
func upTo(b []byte, marker string, n int) []byte {
	s := string(b)
	idx := 0
	for range n {
		idx += strings.Index(s[idx:], marker) + len(marker)
	}
	return b[:idx+strings.Index(s[idx:], "\n\n")+2]
}

// waitCount waits until marker has appeared n times and the frame holding
// the nth is complete.
func (s *rawStream) waitCount(t *testing.T, marker string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b := string(s.bytes())
		if strings.Count(b, marker) >= n {
			idx := 0
			for range n {
				idx += strings.Index(b[idx:], marker) + len(marker)
			}
			if strings.Contains(b[idx:], "\n\n") {
				return
			}
		}
		select {
		case <-s.done:
			t.Fatalf("stream closed before %d× %q:\n%s", n, marker, s.bytes())
		case <-time.After(5 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %d× %q:\n%s", n, marker, s.bytes())
}
