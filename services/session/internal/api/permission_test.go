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

// ask raises one permission ask the way the provider adapter does — on its
// own goroutine, through the Runner's PermissionAsker — and delivers the
// decision on the returned channel.
func ask(r *runner.Runner, tool string) chan provider.PermissionDecision {
	out := make(chan provider.PermissionDecision, 1)
	go func() {
		out <- r.RequestPermission(context.Background(), provider.PermissionAsk{
			ToolUseID: "toolu_1", Tool: tool, Input: json.RawMessage(`{"command":"ls"}`),
		})
	}()
	return out
}

// pendingRequest polls GET /permission-requests until exactly one request
// is pending, and returns it.
func pendingRequest(t *testing.T, srv *httptest.Server) provider.PermissionRequest {
	t.Helper()
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
			return reqs[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("permission request never appeared pending, got %v", reqs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func resolve(t *testing.T, srv *httptest.Server, id, decision string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"decision": decision})
	resp, err := http.Post(srv.URL+"/permission-requests/"+id+"/resolve", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d, want 200", resp.StatusCode)
	}
}

// TestPermissionRoundTripAllow exercises the HTTP half of the contract end
// to end: an ask blocks, GET /permission-requests shows it pending (with
// the asking call's tool_use_id) in the meantime, and POST
// /permission-requests/{id}/resolve unblocks it with the user's decision.
func TestPermissionRoundTripAllow(t *testing.T) {
	srv, r := newPermissionTestServer(t)
	decision := ask(r, "Bash")

	req := pendingRequest(t, srv)
	if req.Tool != "Bash" || req.ToolUseID != "toolu_1" {
		t.Errorf("request = %+v, want Bash for toolu_1", req)
	}
	resolve(t, srv, req.ID, "allow")

	select {
	case d := <-decision:
		if !d.Allow {
			t.Errorf("decision = %+v, want allow", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask never unblocked after resolve")
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

func TestPermissionRoundTripDeny(t *testing.T) {
	srv, r := newPermissionTestServer(t)
	decision := ask(r, "Write")
	resolve(t, srv, pendingRequest(t, srv).ID, "deny")

	select {
	case d := <-decision:
		if d.Allow || d.Message == "" {
			t.Errorf("decision = %+v, want a deny with a message for the model", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask never unblocked after resolve")
	}
}

// TestPermissionExpires proves the "unanswered denies after the bound"
// acceptance criterion, using a short overridden PermissionTimeout.
func TestPermissionExpires(t *testing.T) {
	_, r := newPermissionTestServer(t)
	r.PermissionTimeout = 30 * time.Millisecond

	select {
	case d := <-ask(r, "Bash"):
		if d.Allow {
			t.Errorf("expired decision = %+v, want deny", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask never resolved by its own timeout")
	}
	if got := r.PermissionRequests(); len(got) != 0 {
		t.Errorf("expired request should no longer be pending, got %v", got)
	}
}
