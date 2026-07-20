package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/session/internal/runner"
)

func newPermissionTestServer(t *testing.T) (*httptest.Server, *runner.Runner) {
	t.Helper()
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
	return srv, r
}

func postPermissionHook(t *testing.T, srv *httptest.Server, toolName string) chan *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]any{"tool_name": toolName, "tool_input": map[string]string{"command": "ls"}})
	if err != nil {
		t.Fatal(err)
	}
	respCh := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/permission-hook", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Errorf("POST /permission-hook: %v", err)
			respCh <- nil
			return
		}
		respCh <- resp
	}()
	return respCh
}

// TestPermissionHookRoundTripAllow exercises the full hook contract end to
// end: POST /permission-hook blocks, GET /permission-requests shows it
// pending in the meantime, POST /permission-requests/{id}/resolve unblocks
// it, and the original blocked call gets back the exact hookSpecificOutput
// JSON shape claude's PreToolUse hook contract requires.
func TestPermissionHookRoundTripAllow(t *testing.T) {
	srv, _ := newPermissionTestServer(t)
	respCh := postPermissionHook(t, srv, "Bash")

	var id string
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(srv.URL + "/permission-requests")
		if err != nil {
			t.Fatal(err)
		}
		var reqs []provider.PermissionRequest
		if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if len(reqs) == 1 && reqs[0].Status == "pending" {
			id = reqs[0].ID
			if reqs[0].Tool != "Bash" {
				t.Errorf("tool = %q, want Bash", reqs[0].Tool)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("permission request never appeared pending, got %v", reqs)
		}
		time.Sleep(10 * time.Millisecond)
	}

	resolveBody, _ := json.Marshal(map[string]string{"decision": "allow"})
	resolveResp, err := http.Post(srv.URL+"/permission-requests/"+id+"/resolve", "application/json", bytes.NewReader(resolveBody))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resolveResp.Body.Close() }()
	if resolveResp.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d, want 200", resolveResp.StatusCode)
	}

	select {
	case hookResp := <-respCh:
		if hookResp == nil {
			t.Fatal("hook request failed")
		}
		defer func() { _ = hookResp.Body.Close() }()
		var parsed struct {
			HookSpecificOutput struct {
				PermissionDecision string `json:"permissionDecision"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.NewDecoder(hookResp.Body).Decode(&parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.HookSpecificOutput.PermissionDecision != "allow" {
			t.Errorf("permissionDecision = %q, want allow", parsed.HookSpecificOutput.PermissionDecision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook request never unblocked after resolve")
	}

	// A resolved request must not still be listed as pending.
	resp, err := http.Get(srv.URL + "/permission-requests")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var reqs []provider.PermissionRequest
	if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 0 {
		t.Errorf("expected no pending requests after resolve, got %v", reqs)
	}
}

func TestPermissionHookRoundTripDeny(t *testing.T) {
	srv, r := newPermissionTestServer(t)
	respCh := postPermissionHook(t, srv, "Write")

	deadline := time.Now().Add(2 * time.Second)
	var id string
	for {
		reqs := r.PermissionRequests()
		if len(reqs) == 1 {
			id = reqs[0].ID
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("permission request never registered, got %v", reqs)
		}
		time.Sleep(10 * time.Millisecond)
	}

	resolveBody, _ := json.Marshal(map[string]string{"decision": "deny"})
	resolveResp, err := http.Post(srv.URL+"/permission-requests/"+id+"/resolve", "application/json", bytes.NewReader(resolveBody))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resolveResp.Body.Close() }()

	select {
	case hookResp := <-respCh:
		if hookResp == nil {
			t.Fatal("hook request failed")
		}
		defer func() { _ = hookResp.Body.Close() }()
		var parsed struct {
			HookSpecificOutput struct {
				PermissionDecision string `json:"permissionDecision"`
			} `json:"hookSpecificOutput"`
			SystemMessage string `json:"systemMessage"`
		}
		if err := json.NewDecoder(hookResp.Body).Decode(&parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.HookSpecificOutput.PermissionDecision != "deny" {
			t.Errorf("permissionDecision = %q, want deny", parsed.HookSpecificOutput.PermissionDecision)
		}
		if parsed.SystemMessage == "" {
			t.Error("expected a systemMessage explaining the denial")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook request never unblocked after resolve")
	}
}

// TestPermissionHookExpiresToExpiredCard proves the "unanswered denies after
// the bound, and shows as expired" acceptance criterion over real HTTP,
// using a short overridden PermissionTimeout.
func TestPermissionHookExpiresToExpiredCard(t *testing.T) {
	srv, r := newPermissionTestServer(t)
	r.PermissionTimeout = 30 * time.Millisecond
	respCh := postPermissionHook(t, srv, "Bash")

	select {
	case hookResp := <-respCh:
		if hookResp == nil {
			t.Fatal("hook request failed")
		}
		defer func() { _ = hookResp.Body.Close() }()
		var parsed struct {
			HookSpecificOutput struct {
				PermissionDecision string `json:"permissionDecision"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.NewDecoder(hookResp.Body).Decode(&parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.HookSpecificOutput.PermissionDecision != "deny" {
			t.Errorf("expired decision = %q, want deny", parsed.HookSpecificOutput.PermissionDecision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook request never resolved by its own timeout")
	}

	if got := r.PermissionRequests(); len(got) != 0 {
		t.Errorf("expired request should no longer be pending, got %v", got)
	}
}
