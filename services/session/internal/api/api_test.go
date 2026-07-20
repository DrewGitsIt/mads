package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/session/internal/runner"
)

func TestHTTPRoundTrip(t *testing.T) {
	ctx := context.Background()
	dataBase := datatest.Start(t)

	bootstrap := dataclient.New(dataBase, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake"}
	if err := bootstrap.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	data := dataclient.New(dataBase, sess.ID)
	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := runner.New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start runner: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	srv := httptest.NewServer(New(r))
	t.Cleanup(srv.Close)

	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/output", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamReq.Header.Set("Accept", "text/event-stream")
	streamResp, err := srv.Client().Do(streamReq)
	if err != nil {
		t.Fatalf("GET /output: %v", err)
	}
	t.Cleanup(func() { _ = streamResp.Body.Close() })
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /output status = %d, want 200", streamResp.StatusCode)
	}

	events := make(chan provider.Event, 64)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(streamResp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var ev provider.Event
			if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &ev) != nil {
				continue
			}
			events <- ev
		}
	}()

	body, err := json.Marshal(map[string]string{"text": "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Post(srv.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /messages: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /messages status = %d, want 202", resp.StatusCode)
	}

	var sb strings.Builder
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("stream closed before turn_end")
			}
			switch ev.Kind {
			case provider.EventText:
				sb.WriteString(ev.Text)
			case provider.EventTurnEnd:
				break loop
			case provider.EventError:
				t.Fatalf("unexpected error event: %s", ev.Text)
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_end")
		}
	}
	if got := sb.String(); got != "you said hi" {
		t.Fatalf("reply = %q, want %q", got, "you said hi")
	}
}

func TestSendMessageBeforeReadyReturns503(t *testing.T) {
	r := runner.New("unstarted", nil, nil)
	srv := httptest.NewServer(New(r))
	defer srv.Close()

	body, err := json.Marshal(map[string]string{"text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Post(srv.URL+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestTransportBeforeReadyReturns503(t *testing.T) {
	r := runner.New("unstarted", nil, nil)
	srv := httptest.NewServer(New(r))
	defer srv.Close()

	body, err := json.Marshal(map[string]string{"mode": "terminal"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Post(srv.URL+"/transport", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestTerminalInputBeforeReadyReturns503(t *testing.T) {
	r := runner.New("unstarted", nil, nil)
	srv := httptest.NewServer(New(r))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/input", "application/octet-stream", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestTerminalResizeBeforeReadyReturns503(t *testing.T) {
	r := runner.New("unstarted", nil, nil)
	srv := httptest.NewServer(New(r))
	defer srv.Close()

	body, err := json.Marshal(map[string]int{"cols": 80, "rows": 24})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Post(srv.URL+"/resize", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestTransportStatusBeforeStartDefaultsChat(t *testing.T) {
	r := runner.New("unstarted", nil, nil)
	srv := httptest.NewServer(New(r))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/transport")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /transport status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "chat" {
		t.Fatalf("mode = %q, want %q", got.Mode, "chat")
	}
}

func TestSwitchTransportRoundTrip(t *testing.T) {
	ctx := context.Background()
	dataBase := datatest.Start(t)

	bootstrap := dataclient.New(dataBase, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake"}
	if err := bootstrap.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	data := dataclient.New(dataBase, sess.ID)
	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := runner.New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start runner: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	srv := httptest.NewServer(New(r))
	t.Cleanup(srv.Close)

	// Drive one chat turn first: a genuinely fresh, zero-turn session has no
	// announced provider ref yet (providers/claudecode learns it late, off
	// the wire), so a terminal switch straight off Start would resume "" —
	// matching real behavior, not a bug. Wait for the turn to actually reach
	// turn_end (not just the 202 accepting it) — SwitchTransport refuses
	// mid-turn, and the fake's echo turn, though instant, still resolves on
	// a separate goroutine that can lose the race against an immediate
	// switch.
	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/output", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamReq.Header.Set("Accept", "text/event-stream")
	streamResp, err := srv.Client().Do(streamReq)
	if err != nil {
		t.Fatalf("GET /output: %v", err)
	}
	t.Cleanup(func() { _ = streamResp.Body.Close() })

	events := make(chan provider.Event, 64)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(streamResp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var ev provider.Event
			if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &ev) != nil {
				continue
			}
			events <- ev
		}
	}()

	msgBody, err := json.Marshal(map[string]string{"text": "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	msgResp, err := srv.Client().Post(srv.URL+"/messages", "application/json", bytes.NewReader(msgBody))
	if err != nil {
		t.Fatalf("POST /messages: %v", err)
	}
	_ = msgResp.Body.Close()
	if msgResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /messages status = %d, want 202", msgResp.StatusCode)
	}

	deadline := time.After(5 * time.Second)
waitTurnEnd:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("stream closed before turn_end")
			}
			if ev.Kind == provider.EventTurnEnd {
				break waitTurnEnd
			}
			if ev.Kind == provider.EventError {
				t.Fatalf("unexpected error event: %s", ev.Text)
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_end")
		}
	}

	toTerminal, err := json.Marshal(map[string]string{"mode": "terminal"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Post(srv.URL+"/transport", "application/json", bytes.NewReader(toTerminal))
	if err != nil {
		t.Fatalf("POST /transport (terminal): %v", err)
	}
	var terminalResult struct {
		Mode string `json:"mode"`
		Ref  string `json:"ref"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&terminalResult); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /transport (terminal) status = %d, want 200", resp.StatusCode)
	}
	if terminalResult.Mode != "terminal" || terminalResult.Ref == "" {
		t.Fatalf("terminal switch result = %+v, want mode=terminal and non-empty ref", terminalResult)
	}

	statusResp, err := srv.Client().Get(srv.URL + "/transport")
	if err != nil {
		t.Fatalf("GET /transport: %v", err)
	}
	var statusResult struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&statusResult); err != nil {
		t.Fatal(err)
	}
	_ = statusResp.Body.Close()
	if statusResult.Mode != "terminal" {
		t.Fatalf("GET /transport after switching to terminal = %q, want %q", statusResult.Mode, "terminal")
	}

	toChat, err := json.Marshal(map[string]string{"mode": "chat"})
	if err != nil {
		t.Fatal(err)
	}
	resp2, err := srv.Client().Post(srv.URL+"/transport", "application/json", bytes.NewReader(toChat))
	if err != nil {
		t.Fatalf("POST /transport (chat): %v", err)
	}
	var chatResult struct {
		Mode string `json:"mode"`
		Ref  string `json:"ref"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&chatResult); err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("POST /transport (chat) status = %d, want 200", resp2.StatusCode)
	}
	if chatResult.Ref != terminalResult.Ref {
		t.Fatalf("ref changed across switch: terminal=%q chat=%q, want equal", terminalResult.Ref, chatResult.Ref)
	}
}

// TestTerminalOutputReplaysOnAttach is the acceptance test for reattach: a
// terminal turn driven with nobody attached to GET /output must not be
// lost (the bug report's "scrollback GONE") — the first attach, even a
// first-ever one, gets a leading pty_output replay event carrying it before
// any live bytes.
func TestTerminalOutputReplaysOnAttach(t *testing.T) {
	ctx := context.Background()
	dataBase := datatest.Start(t)

	bootstrap := dataclient.New(dataBase, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake"}
	if err := bootstrap.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	data := dataclient.New(dataBase, sess.ID)
	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := runner.New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start runner: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	srv := httptest.NewServer(New(r))
	t.Cleanup(srv.Close)

	toTerminal, err := json.Marshal(map[string]string{"mode": "terminal"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Post(srv.URL+"/transport", "application/json", bytes.NewReader(toTerminal))
	if err != nil {
		t.Fatalf("POST /transport (terminal): %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /transport (terminal) status = %d, want 200", resp.StatusCode)
	}

	msgBody, err := json.Marshal(map[string]string{"text": "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	msgResp, err := srv.Client().Post(srv.URL+"/messages", "application/json", bytes.NewReader(msgBody))
	if err != nil {
		t.Fatalf("POST /messages: %v", err)
	}
	_ = msgResp.Body.Close()
	if msgResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /messages status = %d, want 202", msgResp.StatusCode)
	}

	// Wait for the turn to actually land: pumpPTY doesn't rebroadcast
	// turn_end to the hub (see runner_test.go's waitForTranscriptLen), so
	// the transcript is the transport-agnostic way to observe completion.
	deadline := time.After(5 * time.Second)
	for {
		if len(r.Transcript(ctx)) >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the pty turn to land in transcript")
		case <-time.After(10 * time.Millisecond):
		}
	}

	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/output", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamReq.Header.Set("Accept", "text/event-stream")
	streamResp, err := srv.Client().Do(streamReq)
	if err != nil {
		t.Fatalf("GET /output: %v", err)
	}
	t.Cleanup(func() { _ = streamResp.Body.Close() })

	events := make(chan provider.Event, 64)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(streamResp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var ev provider.Event
			if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &ev) != nil {
				continue
			}
			events <- ev
		}
	}()

	select {
	case first, ok := <-events:
		if !ok {
			t.Fatal("stream closed before any event")
		}
		if first.Kind != provider.EventPTYOutput {
			t.Fatalf("first event kind = %q, want %q", first.Kind, provider.EventPTYOutput)
		}
		if !strings.Contains(first.Text, "you said hi") {
			t.Fatalf("replay = %q, want it to contain %q", first.Text, "you said hi")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the replay event")
	}
}
