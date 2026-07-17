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
