package api

import (
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestLeaseFlowOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t)
	var task contracts.Task
	doJSON(t, srv, "POST", "/tasks", contracts.Task{Title: "A", SessionID: "ses_l", Source: "user"}, &task)

	// Claim as the pipeline.
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

	// A default-actor (user) write is refused with 409 while leased.
	task.Title = "A edited"
	if resp := doJSON(t, srv, "PUT", "/tasks/"+task.ID, task, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("non-holder write: status %d, want 409", resp.StatusCode)
	}
	// Claiming as someone else is refused too.
	if resp := doJSON(t, srv, "POST", "/tasks/"+task.ID+"/claim", nil, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("second claim: status %d, want 409", resp.StatusCode)
	}

	// Release-all (the Router's supervision call) frees it.
	var out struct {
		Released int `json:"released"`
	}
	doJSON(t, srv, "POST", "/leases/release-all", map[string]string{"holder": "pipeline"}, &out)
	if out.Released != 1 {
		t.Fatalf("release-all released %d, want 1", out.Released)
	}
	if resp := doJSON(t, srv, "PUT", "/tasks/"+task.ID, task, nil); resp.StatusCode != 200 {
		t.Errorf("write after release: status %d", resp.StatusCode)
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
