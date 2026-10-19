package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// doLease is doJSON with an actor and an optional lease token. It returns
// the response and its body decoded generically (for error codes).
func doLease(t *testing.T, srv *httptest.Server, method, path, actorName, token string, body, out any) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, srv.URL+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if actorName != "" {
		req.Header.Set("X-Mads-Actor", actorName)
	}
	if token != "" {
		req.Header.Set("X-Mads-Lease", token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	return resp, generic
}

func TestLeaseFlowOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t)
	var task contracts.Task
	doJSON(t, srv, "POST", "/tasks", contracts.Task{Title: "A", SessionID: "ses_l", Source: "user"}, &task)
	base := "/tasks/" + task.ID

	// Claim as a pipeline run: the response carries the token.
	var lease contracts.Lease
	resp, _ := doLease(t, srv, "POST", base+"/claim", "pipeline", "",
		map[string]any{"holder": "pipeline:run_1", "ttl_seconds": 60}, &lease)
	if resp.StatusCode != 200 {
		t.Fatalf("claim: status %d", resp.StatusCode)
	}
	if lease.Holder != "pipeline:run_1" || lease.Token == "" {
		t.Fatalf("claim response = %+v, want holder pipeline:run_1 and a token", lease)
	}

	// Any second claim is 409, the same holder's included.
	if resp, _ := doLease(t, srv, "POST", base+"/claim", "pipeline", "", map[string]any{"holder": "pipeline:run_1"}, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("same-holder re-claim: status %d, want 409", resp.StatusCode)
	}
	if resp := doJSON(t, srv, "POST", base+"/claim", nil, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("second claim: status %d, want 409", resp.StatusCode)
	}

	// Writes without the token are 409, even under the pipeline's actor.
	task.Title = "A edited"
	if resp := doJSON(t, srv, "PUT", base, task, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("tokenless user write: status %d, want 409", resp.StatusCode)
	}
	if resp, body := doLease(t, srv, "PUT", base, "pipeline", "", task, nil); resp.StatusCode != http.StatusConflict || body["code"] != "lease_held" {
		t.Errorf("tokenless pipeline write: status %d body %v, want 409 lease_held", resp.StatusCode, body)
	}
	note := contracts.Note{Type: contracts.NoteContext, Content: "x", Source: "pipeline"}
	if resp, _ := doLease(t, srv, "POST", base+"/notes", "pipeline", "wrong", note, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("wrong-token note: status %d, want 409", resp.StatusCode)
	}
	// With the token they pass.
	if resp, _ := doLease(t, srv, "PUT", base, "pipeline", lease.Token, task, nil); resp.StatusCode != 200 {
		t.Errorf("write with token: status %d", resp.StatusCode)
	}
	if resp, _ := doLease(t, srv, "POST", base+"/notes", "pipeline", lease.Token, note, nil); resp.StatusCode != http.StatusCreated {
		t.Errorf("note with token: status %d", resp.StatusCode)
	}

	// Renew: the token extends; anything else is 409 lease_lost.
	var renewed contracts.Lease
	if resp, _ := doLease(t, srv, "POST", base+"/lease/renew", "pipeline", lease.Token, nil, &renewed); resp.StatusCode != 200 {
		t.Fatalf("renew: status %d", resp.StatusCode)
	}
	if renewed.Token != "" {
		t.Error("renew echoed the token")
	}
	for _, tok := range []string{"", "wrong"} {
		if resp, body := doLease(t, srv, "POST", base+"/lease/renew", "pipeline", tok, nil, nil); resp.StatusCode != http.StatusConflict || body["code"] != "lease_lost" {
			t.Errorf("renew with token %q: status %d body %v, want 409 lease_lost", tok, resp.StatusCode, body)
		}
	}

	// Release: a wrong or missing token is 409; the token releases; again
	// is a no-op.
	for _, tok := range []string{"", "wrong"} {
		if resp, _ := doLease(t, srv, "POST", base+"/release", "pipeline", tok, nil, nil); resp.StatusCode != http.StatusConflict {
			t.Errorf("release with token %q: status %d, want 409", tok, resp.StatusCode)
		}
	}
	if resp, _ := doLease(t, srv, "POST", base+"/release", "pipeline", lease.Token, nil, nil); resp.StatusCode != 200 {
		t.Fatalf("release: status %d", resp.StatusCode)
	}
	if resp, _ := doLease(t, srv, "POST", base+"/release", "pipeline", lease.Token, nil, nil); resp.StatusCode != 200 {
		t.Errorf("double release: status %d, want 200", resp.StatusCode)
	}

	// Renew after release never re-creates the lease.
	if resp, body := doLease(t, srv, "POST", base+"/lease/renew", "pipeline", lease.Token, nil, nil); resp.StatusCode != http.StatusConflict || body["code"] != "lease_lost" {
		t.Errorf("renew after release: status %d body %v, want 409 lease_lost", resp.StatusCode, body)
	}
	if resp := doJSON(t, srv, "PUT", base, task, nil); resp.StatusCode != 200 {
		t.Errorf("write after release: status %d (renew must not have re-created the lease)", resp.StatusCode)
	}
}

func TestClaimHolderMustMatchActor(t *testing.T) {
	srv, _ := newTestServer(t)
	var task contracts.Task
	doJSON(t, srv, "POST", "/tasks", contracts.Task{Title: "A", SessionID: "ses_l", Source: "user"}, &task)

	for _, holder := range []string{"synthesizer", "pipelinex:run_1", "pipeline:"} {
		if resp, _ := doLease(t, srv, "POST", "/tasks/"+task.ID+"/claim", "pipeline", "", map[string]any{"holder": holder}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("holder %q as pipeline: status %d, want 422", holder, resp.StatusCode)
		}
	}
	// No holder: holder = actor (the session service and synthesizer path).
	var lease contracts.Lease
	if resp, _ := doLease(t, srv, "POST", "/tasks/"+task.ID+"/claim", "synthesizer", "", nil, &lease); resp.StatusCode != 200 || lease.Holder != "synthesizer" {
		t.Fatalf("holderless claim: status %d lease %+v, want 200 holder synthesizer", resp.StatusCode, lease)
	}
}

func TestReleaseAllAndKindListingOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t)
	mk := func(title string) contracts.Task {
		var task contracts.Task
		doJSON(t, srv, "POST", "/tasks", contracts.Task{Title: title, SessionID: "ses_l", Source: "user", Status: contracts.StatusInProgress}, &task)
		return task
	}
	a, b, bare := mk("a"), mk("b"), mk("bare")
	for task, holder := range map[string]string{a.ID: "pipeline:run_a", b.ID: "pipeline:run_b", bare.ID: "pipeline"} {
		if resp, _ := doLease(t, srv, "POST", "/tasks/"+task+"/claim", "pipeline", "", map[string]any{"holder": holder}, nil); resp.StatusCode != 200 {
			t.Fatalf("claim %s: status %d", holder, resp.StatusCode)
		}
	}

	// ?kind= lists every lease of the kind; the kind must be the caller's
	// actor.
	var byKind []contracts.Lease
	if resp, _ := doLease(t, srv, "GET", "/leases?kind=pipeline", "pipeline", "", nil, &byKind); resp.StatusCode != 200 || len(byKind) != 3 {
		t.Fatalf("GET /leases?kind=pipeline: status %d, %d leases, want 3", resp.StatusCode, len(byKind))
	}
	for _, l := range byKind {
		if l.Token != "" {
			t.Error("a lease listing carried a token")
		}
	}
	if resp, _ := doLease(t, srv, "GET", "/leases?kind=pipeline", "user", "", nil, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("kind other than the actor: status %d, want 422", resp.StatusCode)
	}
	// Without kind: exactly the caller's actor, as before.
	var mine []contracts.Lease
	doLease(t, srv, "GET", "/leases", "pipeline", "", nil, &mine)
	if len(mine) != 1 || mine[0].TaskID != bare.ID {
		t.Errorf("GET /leases as pipeline = %+v, want just the bare pipeline lease", mine)
	}

	// Release-all (the Router's supervision call) frees one run's leases,
	// tokenless, and leaves task status alone.
	var out struct {
		Released int `json:"released"`
	}
	doJSON(t, srv, "POST", "/leases/release-all", map[string]string{"holder": "pipeline:run_a"}, &out)
	if out.Released != 1 {
		t.Fatalf("release-all released %d, want 1", out.Released)
	}
	var got contracts.Task
	doJSON(t, srv, "GET", "/tasks/"+a.ID, nil, &got)
	if got.Status != contracts.StatusInProgress {
		t.Errorf("status after release-all = %s, want in_progress", got.Status)
	}
	got.Title = "edited"
	if resp := doJSON(t, srv, "PUT", "/tasks/"+a.ID, got, nil); resp.StatusCode != 200 {
		t.Errorf("write after release-all: status %d", resp.StatusCode)
	}
}

func TestSessionLeaseActiveOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t)
	var task contracts.Task
	doJSON(t, srv, "POST", "/tasks", contracts.Task{Title: "A", SessionID: "ses_lease", Source: "user"}, &task)

	var before struct {
		Active bool `json:"active"`
	}
	doJSON(t, srv, "GET", "/sessions/ses_lease/lease-active", nil, &before)
	if before.Active {
		t.Error("no claim yet, want active=false")
	}

	req, _ := http.NewRequest("POST", srv.URL+"/tasks/"+task.ID+"/claim", nil)
	req.Header.Set("X-Mads-Actor", "pipeline")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("claim: status %d", resp.StatusCode)
	}

	var after struct {
		Active bool `json:"active"`
	}
	doJSON(t, srv, "GET", "/sessions/ses_lease/lease-active", nil, &after)
	if !after.Active {
		t.Error("claimed lease, want active=true")
	}
}

func TestClaimValidatesTTL(t *testing.T) {
	srv, _ := newTestServer(t)
	var task contracts.Task
	doJSON(t, srv, "POST", "/tasks", contracts.Task{Title: "A", SessionID: "ses_l", Source: "user"}, &task)

	if resp := doJSON(t, srv, "POST", "/tasks/"+task.ID+"/claim",
		map[string]int{"ttl_seconds": 999999}, nil); resp.StatusCode != 422 {
		t.Errorf("oversized ttl: status %d, want 422", resp.StatusCode)
	}
	if resp := doJSON(t, srv, "POST", "/tasks/tsk_missing/claim", nil, nil); resp.StatusCode != 404 {
		t.Errorf("claim on missing task: status %d, want 404", resp.StatusCode)
	}
}
