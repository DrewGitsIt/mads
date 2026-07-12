package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func doJSON(t *testing.T, srv *httptest.Server, method, path string, body any, out any) *http.Response {
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
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp
}

func TestTaskCRUDRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)

	var created contracts.Task
	resp := doJSON(t, srv, "POST", "/tasks", contracts.Task{
		Title: "Wire the router", SessionID: "ses_api", Source: "user",
		Labels: []string{"backend"},
	}, &created)
	if resp.StatusCode != http.StatusCreated || created.ID == "" {
		t.Fatalf("create: status %d, task %+v", resp.StatusCode, created)
	}

	created.Status = contracts.StatusInProgress
	var updated contracts.Task
	if resp := doJSON(t, srv, "PUT", "/tasks/"+created.ID, created, &updated); resp.StatusCode != 200 {
		t.Fatalf("update: status %d", resp.StatusCode)
	}

	var fetched contracts.Task
	if resp := doJSON(t, srv, "GET", "/tasks/"+created.ID, nil, &fetched); resp.StatusCode != 200 {
		t.Fatalf("get: status %d", resp.StatusCode)
	}
	if fetched.Status != contracts.StatusInProgress || fetched.Title != "Wire the router" {
		t.Errorf("round trip lost data: %+v", fetched)
	}

	var list []contracts.Task
	doJSON(t, srv, "GET", "/tasks?session_id=ses_api&status=in_progress", nil, &list)
	if len(list) != 1 {
		t.Errorf("filtered list: want 1, got %d", len(list))
	}

	if resp := doJSON(t, srv, "GET", "/tasks/tsk_missing", nil, nil); resp.StatusCode != 404 {
		t.Errorf("missing task: status %d, want 404", resp.StatusCode)
	}
}

func TestTaskValidationOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t)
	var verr contracts.ValidationError
	resp := doJSON(t, srv, "POST", "/tasks", contracts.Task{}, &verr)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", resp.StatusCode)
	}
	if len(verr.Errors) < 3 {
		t.Errorf("want all errors together, got %+v", verr.Errors)
	}
}

func TestDraftConfirmDiscardOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t)
	var draft contracts.Task
	doJSON(t, srv, "POST", "/tasks", contracts.Task{
		Title: "maybe do this", SessionID: "ses_api", Source: "synthesizer", Draft: true,
	}, &draft)

	if resp := doJSON(t, srv, "POST", "/tasks/"+draft.ID+"/confirm", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("confirm: status %d", resp.StatusCode)
	}
	// Confirming twice fails: no longer a draft.
	if resp := doJSON(t, srv, "POST", "/tasks/"+draft.ID+"/confirm", nil, nil); resp.StatusCode != 422 {
		t.Errorf("double confirm: status %d, want 422", resp.StatusCode)
	}
}

func TestCyclesEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	var a contracts.Task
	doJSON(t, srv, "POST", "/tasks", contracts.Task{Title: "A", SessionID: "ses_cyc", Source: "user"}, &a)
	var b contracts.Task
	doJSON(t, srv, "POST", "/tasks", contracts.Task{
		Title: "B", SessionID: "ses_cyc", Source: "user", Dependencies: []string{a.ID},
	}, &b)
	a.Dependencies = []string{b.ID}
	doJSON(t, srv, "PUT", "/tasks/"+a.ID, a, nil)

	var out struct {
		Cycles [][]string `json:"cycles"`
	}
	if resp := doJSON(t, srv, "GET", "/tasks/cycles?session_id=ses_cyc", nil, &out); resp.StatusCode != 200 {
		t.Fatalf("cycles: status %d", resp.StatusCode)
	}
	if len(out.Cycles) != 1 || len(out.Cycles[0]) != 2 {
		t.Errorf("want one 2-task cycle, got %v", out.Cycles)
	}
}

func TestTaskEventsReachSubscribers(t *testing.T) {
	srv, _ := newTestServer(t)
	var task contracts.Task
	doJSON(t, srv, "POST", "/tasks", contracts.Task{Title: "A", SessionID: "ses_sub", Source: "user"}, &task)
	doJSON(t, srv, "POST", "/tasks/"+task.ID+"/notes",
		contracts.Note{Type: contracts.NoteContext, Content: "ctx", Source: "user"}, nil)

	var events []contracts.Envelope
	doJSON(t, srv, "GET", "/events?session_id=ses_sub&types=task.*,note.*", nil, &events)
	if len(events) != 2 {
		t.Fatalf("want task.created + note.created on the log, got %d", len(events))
	}
	if events[0].TaskID != task.ID {
		t.Errorf("event not scoped to task: %+v", events[0])
	}
}
