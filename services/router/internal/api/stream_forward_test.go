package api_test

import (
	"bufio"
	"context"
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
