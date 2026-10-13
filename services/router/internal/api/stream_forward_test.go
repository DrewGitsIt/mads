package api_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/dataclient"
)

func TestSessionOutputForwarding(t *testing.T) {
	srv, dataBase := newTestServerWithData(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "output test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	verifier := dataclient.New(dataBase, "verifier")
	before, err := verifier.Events(context.Background(), 0, "", nil)
	if err != nil {
		t.Fatalf("events before: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/sessions/"+created.Session.ID+"/output", nil)
	if err != nil {
		t.Fatal(err)
	}
	outResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outResp.Body.Close() }()
	if outResp.StatusCode != http.StatusOK {
		t.Fatalf("output status = %d, want 200", outResp.StatusCode)
	}

	var got []string
	sc := bufio.NewScanner(outResp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			got = append(got, strings.TrimPrefix(line, "data: "))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan output: %v", err)
	}

	want := []string{"token-1", "token-2", "token-3"}
	if len(got) != len(want) {
		t.Fatalf("got %d frames %v, want %d frames %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frame %d = %q, want %q", i, got[i], want[i])
		}
	}

	after, err := verifier.Events(context.Background(), 0, "", nil)
	if err != nil {
		t.Fatalf("events after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("event log grew from %d to %d entries: data-plane output must never be persisted", len(before), len(after))
	}
	for _, env := range after {
		if strings.Contains(string(env.Payload), "token-") {
			t.Fatalf("found data-plane token leaked into event log: %+v", env)
		}
	}
}

func TestSessionOutputNotRunning(t *testing.T) {
	srv, _ := newTestServerWithData(t)
	resp, err := http.Get(srv.URL + "/sessions/does-not-exist/output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestPipelineOutputNotRunning(t *testing.T) {
	srv, _ := newTestServerWithData(t)
	resp, err := http.Get(srv.URL + "/pipelines/does-not-exist/output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSessionMessagesForwarding(t *testing.T) {
	srv, _ := newTestServerWithData(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "messages test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	msgResp, err := http.Post(srv.URL+"/sessions/"+created.Session.ID+"/messages", "application/json", bytes.NewBufferString(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = msgResp.Body.Close() }()
	if msgResp.StatusCode != http.StatusAccepted {
		t.Fatalf("messages status = %d, want 202", msgResp.StatusCode)
	}
	body, err := io.ReadAll(msgResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != `{"text":"hi"}` {
		t.Errorf("proxied body = %q, want the request body echoed back unmodified", got)
	}
}

func TestSessionMessagesNotRunning(t *testing.T) {
	srv, _ := newTestServerWithData(t)
	resp, err := http.Post(srv.URL+"/sessions/does-not-exist/messages", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSessionInterruptForwarding(t *testing.T) {
	srv, _ := newTestServerWithData(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "interrupt test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	intResp, err := http.Post(srv.URL+"/sessions/"+created.Session.ID+"/interrupt", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = intResp.Body.Close() }()
	if intResp.StatusCode != http.StatusOK {
		t.Fatalf("interrupt status = %d, want 200", intResp.StatusCode)
	}
}

func TestSessionCompactForwarding(t *testing.T) {
	srv, _ := newTestServerWithData(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "compact test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	cResp, err := http.Post(srv.URL+"/sessions/"+created.Session.ID+"/compact", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cResp.Body.Close() }()
	if cResp.StatusCode != http.StatusAccepted {
		t.Fatalf("compact status = %d, want 202", cResp.StatusCode)
	}

	// The optional focus body passes through to the child.
	fResp := postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/compact", map[string]string{"instructions": "keep the plan"})
	defer func() { _ = fResp.Body.Close() }()
	if fResp.StatusCode != http.StatusAccepted {
		t.Fatalf("compact with instructions status = %d, want 202", fResp.StatusCode)
	}
}

func TestSessionInterruptNotRunning(t *testing.T) {
	srv, _ := newTestServerWithData(t)
	resp, err := http.Post(srv.URL+"/sessions/does-not-exist/interrupt", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSessionTransportForwarding(t *testing.T) {
	srv, _ := newTestServerWithData(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "transport test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	tResp, err := http.Post(srv.URL+"/sessions/"+created.Session.ID+"/transport", "application/json", bytes.NewBufferString(`{"mode":"terminal"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tResp.Body.Close() }()
	if tResp.StatusCode != http.StatusOK {
		t.Fatalf("transport status = %d, want 200", tResp.StatusCode)
	}
	got := decodeJSON[struct {
		Mode string `json:"mode"`
		Ref  string `json:"ref"`
	}](t, tResp)
	if got.Mode != "terminal" || got.Ref == "" {
		t.Errorf("proxied transport response = %+v, want mode echoed back with a non-empty ref", got)
	}
}

// The child's documented switch-failure responses (409 busy, 502 resume
// failed) must pass through the Router untouched — it does no status-code
// special-casing on this route.
func TestSessionTransportForwardingPassesThroughFailure(t *testing.T) {
	srv, _ := newTestServerWithData(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "transport busy test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	tResp, err := http.Post(srv.URL+"/sessions/"+created.Session.ID+"/transport", "application/json", bytes.NewBufferString(`{"mode":"busy"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tResp.Body.Close() }()
	if tResp.StatusCode != http.StatusConflict {
		t.Fatalf("transport status = %d, want 409", tResp.StatusCode)
	}
}

func TestSessionTransportNotRunning(t *testing.T) {
	srv, _ := newTestServerWithData(t)
	resp, err := http.Post(srv.URL+"/sessions/does-not-exist/transport", "application/json", bytes.NewBufferString(`{"mode":"terminal"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestGetSessionTransportForwarding proves the read side (GET, used by the
// UI to initialize its display-mode toggle on mount) reflects a switch made
// through the write side (POST) — both routes proxy to the same running
// child.
func TestGetSessionTransportForwarding(t *testing.T) {
	srv, _ := newTestServerWithData(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "transport status test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	before, err := http.Get(srv.URL + "/sessions/" + created.Session.ID + "/transport")
	if err != nil {
		t.Fatal(err)
	}
	beforeGot := decodeJSON[struct {
		Mode string `json:"mode"`
	}](t, before)
	if beforeGot.Mode != "chat" {
		t.Fatalf("mode before switching = %q, want %q", beforeGot.Mode, "chat")
	}

	tResp, err := http.Post(srv.URL+"/sessions/"+created.Session.ID+"/transport", "application/json", bytes.NewBufferString(`{"mode":"terminal"}`))
	if err != nil {
		t.Fatal(err)
	}
	if tResp.StatusCode != http.StatusOK {
		t.Fatalf("switch status = %d, want 200", tResp.StatusCode)
	}
	_ = tResp.Body.Close()

	after, err := http.Get(srv.URL + "/sessions/" + created.Session.ID + "/transport")
	if err != nil {
		t.Fatal(err)
	}
	afterGot := decodeJSON[struct {
		Mode string `json:"mode"`
	}](t, after)
	if afterGot.Mode != "terminal" {
		t.Fatalf("mode after switching to terminal = %q, want %q", afterGot.Mode, "terminal")
	}
}

// TestGetSessionTransportNotRunning is the dormant-session case: no child
// means no transport history to report, so this answers "chat" (the UI's
// own default) rather than 404ing like every write-side proxy in this file.
func TestGetSessionTransportNotRunning(t *testing.T) {
	srv, _ := newTestServerWithData(t)
	resp, err := http.Get(srv.URL + "/sessions/does-not-exist/transport")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[struct {
		Mode string `json:"mode"`
	}](t, resp)
	if got.Mode != "chat" {
		t.Fatalf("mode = %q, want %q", got.Mode, "chat")
	}
}

func TestSessionInputForwarding(t *testing.T) {
	srv, _ := newTestServerWithData(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "input test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	iResp, err := http.Post(srv.URL+"/sessions/"+created.Session.ID+"/input", "application/octet-stream", bytes.NewBufferString("ls\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = iResp.Body.Close() }()
	if iResp.StatusCode != http.StatusOK {
		t.Fatalf("input status = %d, want 200", iResp.StatusCode)
	}
}

func TestSessionInputNotRunning(t *testing.T) {
	srv, _ := newTestServerWithData(t)
	resp, err := http.Post(srv.URL+"/sessions/does-not-exist/input", "application/octet-stream", bytes.NewBufferString("ls\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestSessionResizeForwarding(t *testing.T) {
	srv, _ := newTestServerWithData(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "resize test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	rResp, err := http.Post(srv.URL+"/sessions/"+created.Session.ID+"/resize", "application/json", bytes.NewBufferString(`{"cols":80,"rows":24}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rResp.Body.Close() }()
	if rResp.StatusCode != http.StatusOK {
		t.Fatalf("resize status = %d, want 200", rResp.StatusCode)
	}
}

func TestSessionResizeNotRunning(t *testing.T) {
	srv, _ := newTestServerWithData(t)
	resp, err := http.Post(srv.URL+"/sessions/does-not-exist/resize", "application/json", bytes.NewBufferString(`{"cols":80,"rows":24}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
