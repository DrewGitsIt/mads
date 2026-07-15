package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
)

// Accept/reject go through the Router's own routes below (see
// TestAcceptDPA/TestRejectDPA); only Edit has no Router route of its own, so
// it's exercised against the Data Service client directly here.
func TestRetroDPAAcceptRejectEdit(t *testing.T) {
	_, dataBase := newTestServerWithScenario(t, fake.Scenario{})
	c := dataclient.New(dataBase, "test-verify")
	ctx := context.Background()

	retro := contracts.Retro{
		ID: "rto_transitions",
		DPAs: []contracts.DPA{
			{ID: "dpa_1", Problem: "p1", Evidence: "e1", RootCause: "r1",
				RemediationLayer: contracts.RemediationToolConfig, SuggestedAction: "a1", Status: contracts.DPAAccepted},
			{ID: "dpa_2", Problem: "p2", Evidence: "e2", RootCause: "r2",
				RemediationLayer: contracts.RemediationHumanProcess, SuggestedAction: "a2", Status: contracts.DPAAccepted},
		},
	}
	if err := c.CreateRetro(ctx, &retro); err != nil {
		t.Fatalf("CreateRetro: %v", err)
	}

	if err := c.AcceptDPA(ctx, retro.ID, "dpa_1"); err != nil {
		t.Fatalf("AcceptDPA: %v", err)
	}
	if err := c.RejectDPA(ctx, retro.ID, "dpa_2"); err != nil {
		t.Fatalf("RejectDPA: %v", err)
	}
	edited, err := c.EditDPA(ctx, retro.ID, "dpa_1", contracts.DPAEdit{Problem: "revised p1"})
	if err != nil {
		t.Fatalf("EditDPA: %v", err)
	}
	if edited.Problem != "revised p1" || edited.Status != contracts.DPAEdited {
		t.Fatalf("EditDPA result = %+v", edited)
	}

	got, err := c.Retro(ctx, retro.ID)
	if err != nil {
		t.Fatalf("Retro: %v", err)
	}
	if got.DPAs[0].Status != contracts.DPAEdited || got.DPAs[0].Problem != "revised p1" {
		t.Errorf("dpa_1 = %+v", got.DPAs[0])
	}
	if got.DPAs[1].Status != contracts.DPARejected {
		t.Errorf("dpa_2 = %+v", got.DPAs[1])
	}
}

// The create-follow-up-session requirement: accepted/edited DPAs become
// tasks in a new, "retro"-tagged session traceable back to the retro.
func TestRetroFollowUpSession(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{})
	c := dataclient.New(dataBase, "test-verify")
	ctx := context.Background()

	retro := contracts.Retro{
		ID: "rto_followup",
		DPAs: []contracts.DPA{
			{ID: "dpa_1", Problem: "Flaky deploys", Evidence: "3 failed deploys", RootCause: "No smoke test",
				RemediationLayer: contracts.RemediationPipelineTemplate, SuggestedAction: "Add a smoke test stage",
				Status: contracts.DPAAccepted},
		},
	}
	if err := c.CreateRetro(ctx, &retro); err != nil {
		t.Fatalf("CreateRetro: %v", err)
	}

	resp := postJSON(t, srv.URL+"/retros/"+retro.ID+"/follow-up-session", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
		Addr    string            `json:"addr"`
		Tasks   []contracts.Task  `json:"tasks"`
	}](t, resp)

	if created.Addr == "" {
		t.Fatal("no addr discovered from spawned follow-up session")
	}
	if len(created.Session.Tags) != 1 || created.Session.Tags[0] != "retro" {
		t.Errorf("session tags = %v, want [retro]", created.Session.Tags)
	}
	if created.Session.RetroID != retro.ID {
		t.Errorf("session.RetroID = %q, want %q", created.Session.RetroID, retro.ID)
	}
	if len(created.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(created.Tasks))
	}
	if created.Tasks[0].DPAID != "dpa_1" || created.Tasks[0].Source != "retro" {
		t.Errorf("task = %+v", created.Tasks[0])
	}
	if created.Tasks[0].SessionID != created.Session.ID {
		t.Errorf("task.SessionID = %q, want %q", created.Tasks[0].SessionID, created.Session.ID)
	}

	linked, err := c.Retro(ctx, retro.ID)
	if err != nil {
		t.Fatalf("Retro: %v", err)
	}
	if linked.FollowUpSessionID != created.Session.ID {
		t.Errorf("FollowUpSessionID = %q, want %q", linked.FollowUpSessionID, created.Session.ID)
	}
}

// A rejected DPA never becomes a follow-up task.
func TestRetroFollowUpSessionExcludesRejectedDPAs(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{})
	c := dataclient.New(dataBase, "test-verify")
	ctx := context.Background()

	retro := contracts.Retro{
		ID: "rto_excludes_rejected",
		DPAs: []contracts.DPA{
			{ID: "dpa_1", Problem: "Kept", Evidence: "e", RootCause: "r",
				RemediationLayer: contracts.RemediationToolConfig, SuggestedAction: "a", Status: contracts.DPAAccepted},
			{ID: "dpa_2", Problem: "Dropped", Evidence: "e", RootCause: "r",
				RemediationLayer: contracts.RemediationToolConfig, SuggestedAction: "a", Status: contracts.DPARejected},
		},
	}
	if err := c.CreateRetro(ctx, &retro); err != nil {
		t.Fatalf("CreateRetro: %v", err)
	}

	resp := postJSON(t, srv.URL+"/retros/"+retro.ID+"/follow-up-session", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Tasks []contracts.Task `json:"tasks"`
	}](t, resp)
	if len(created.Tasks) != 1 || created.Tasks[0].DPAID != "dpa_1" {
		t.Fatalf("tasks = %+v, want exactly the dpa_1 task", created.Tasks)
	}
}

// A retro with every DPA rejected has nothing to create a follow-up from.
func TestRetroFollowUpSessionNoAcceptedDPAs(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{})
	c := dataclient.New(dataBase, "test-verify")
	ctx := context.Background()

	retro := contracts.Retro{
		ID: "rto_all_rejected",
		DPAs: []contracts.DPA{
			{ID: "dpa_1", Problem: "p", Evidence: "e", RootCause: "r",
				RemediationLayer: contracts.RemediationToolConfig, SuggestedAction: "a", Status: contracts.DPARejected},
		},
	}
	if err := c.CreateRetro(ctx, &retro); err != nil {
		t.Fatalf("CreateRetro: %v", err)
	}

	resp := postJSON(t, srv.URL+"/retros/"+retro.ID+"/follow-up-session", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestStartRetroValidation(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/retros", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	verr := decodeJSON[contracts.ValidationError](t, resp)
	if len(verr.Errors) != 1 || verr.Errors[0].Field != "session_ids" {
		t.Fatalf("errors = %+v, want one error on session_ids", verr.Errors)
	}
}

func TestCancelJobNotFound(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/jobs/job_never_existed/cancel", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestListRetros(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{})
	c := dataclient.New(dataBase, "test-verify")
	ctx := context.Background()

	retro := contracts.Retro{ID: "rto_list_me"}
	if err := c.CreateRetro(ctx, &retro); err != nil {
		t.Fatalf("CreateRetro: %v", err)
	}

	resp, err := http.Get(srv.URL + "/retros")
	if err != nil {
		t.Fatal(err)
	}
	list := decodeJSON[[]contracts.Retro](t, resp)
	found := false
	for _, r := range list {
		if r.ID == retro.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("GET /retros did not include created retro %s: %v", retro.ID, list)
	}
}

func TestGetRetro(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{})
	c := dataclient.New(dataBase, "test-verify")
	ctx := context.Background()

	retro := contracts.Retro{
		ID: "rto_get_me",
		DPAs: []contracts.DPA{
			{ID: "dpa_1", Problem: "p1", Evidence: "e1", RootCause: "r1",
				RemediationLayer: contracts.RemediationToolConfig, SuggestedAction: "a1", Status: contracts.DPAAccepted},
		},
	}
	if err := c.CreateRetro(ctx, &retro); err != nil {
		t.Fatalf("CreateRetro: %v", err)
	}

	resp, err := http.Get(srv.URL + "/retros/" + retro.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[contracts.Retro](t, resp)
	if got.ID != retro.ID || len(got.DPAs) != 1 {
		t.Fatalf("GET /retros/%s = %+v", retro.ID, got)
	}
}

func TestGetRetroNotFound(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/retros/rto_never_existed")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAcceptDPA(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{})
	c := dataclient.New(dataBase, "test-verify")
	ctx := context.Background()

	retro := contracts.Retro{
		ID: "rto_accept_http",
		DPAs: []contracts.DPA{
			{ID: "dpa_1", Problem: "p1", Evidence: "e1", RootCause: "r1",
				RemediationLayer: contracts.RemediationToolConfig, SuggestedAction: "a1"},
		},
	}
	if err := c.CreateRetro(ctx, &retro); err != nil {
		t.Fatalf("CreateRetro: %v", err)
	}

	resp := postJSON(t, srv.URL+"/retros/"+retro.ID+"/dpas/dpa_1/accept", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	got, err := c.Retro(ctx, retro.ID)
	if err != nil {
		t.Fatalf("Retro: %v", err)
	}
	if got.DPAs[0].Status != contracts.DPAAccepted {
		t.Errorf("dpa_1 status = %q, want accepted", got.DPAs[0].Status)
	}
}

func TestRejectDPA(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{})
	c := dataclient.New(dataBase, "test-verify")
	ctx := context.Background()

	retro := contracts.Retro{
		ID: "rto_reject_http",
		DPAs: []contracts.DPA{
			{ID: "dpa_1", Problem: "p1", Evidence: "e1", RootCause: "r1",
				RemediationLayer: contracts.RemediationToolConfig, SuggestedAction: "a1"},
		},
	}
	if err := c.CreateRetro(ctx, &retro); err != nil {
		t.Fatalf("CreateRetro: %v", err)
	}

	resp := postJSON(t, srv.URL+"/retros/"+retro.ID+"/dpas/dpa_1/reject", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	got, err := c.Retro(ctx, retro.ID)
	if err != nil {
		t.Fatalf("Retro: %v", err)
	}
	if got.DPAs[0].Status != contracts.DPARejected {
		t.Errorf("dpa_1 status = %q, want rejected", got.DPAs[0].Status)
	}
}
