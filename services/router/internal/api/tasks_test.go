package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
)

// The proxy round-trip: a draft task created directly against the Data
// Service, confirmed through the Router, observed as task.confirmed via a
// one-shot GET /events against the Data Service directly (the Router's own
// GET /events is SSE-only, per its README — a batch fetch belongs to the
// Data Service, same as agents_test.go's newTestServerWithScenario pattern).
func TestTaskConfirmRoundTrip(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{})
	c := dataclient.New(srv.URL, "test-verify")
	events := dataclient.New(dataBase, "test-verify")
	ctx := context.Background()

	task := contracts.Task{SessionID: "ses_x", Title: "draft task", Source: "synthesizer", Draft: true}
	if err := c.CreateTask(ctx, &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if !task.Draft {
		t.Fatal("created task is not a draft")
	}

	resp := postJSON(t, srv.URL+"/tasks/"+task.ID+"/confirm", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm status = %d, want 200", resp.StatusCode)
	}

	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.Draft {
		t.Error("task still marked draft after confirm")
	}

	got2, err := events.Events(ctx, 0, "ses_x", []string{"task.confirmed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got2) != 1 {
		t.Fatalf("got %d task.confirmed events, want 1", len(got2))
	}
}

// The discard round-trip: a discarded draft task is gone (404 on subsequent
// GET), proxied through the Router the same way as confirm.
func TestTaskDiscardRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	c := dataclient.New(srv.URL, "test-verify")
	ctx := context.Background()

	task := contracts.Task{SessionID: "ses_x", Title: "draft task", Source: "synthesizer", Draft: true}
	if err := c.CreateTask(ctx, &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	resp := postJSON(t, srv.URL+"/tasks/"+task.ID+"/discard", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discard status = %d, want 200", resp.StatusCode)
	}

	resp, err := http.Get(srv.URL + "/tasks/" + task.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after discard status = %d, want 404", resp.StatusCode)
	}
}

// Full CRUD + dependency listing through the Router, including the
// full-replace PUT gotcha and the cycles endpoint.
func TestTaskCRUDAndCyclesThroughRouter(t *testing.T) {
	srv := newTestServer(t)
	c := dataclient.New(srv.URL, "test-verify")
	ctx := context.Background()

	a := contracts.Task{SessionID: "ses_y", Title: "task a", Source: "user"}
	if err := c.CreateTask(ctx, &a); err != nil {
		t.Fatalf("CreateTask a: %v", err)
	}
	b := contracts.Task{SessionID: "ses_y", Title: "task b", Source: "user", Dependencies: []string{a.ID}}
	if err := c.CreateTask(ctx, &b); err != nil {
		t.Fatalf("CreateTask b: %v", err)
	}

	list, err := c.Tasks(ctx, "ses_y", "")
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d tasks, want 2", len(list))
	}

	// Full-replace PUT: change status, everything else must round-trip too.
	b.Status = contracts.StatusInProgress
	if err := c.UpdateTask(ctx, &b); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	got, err := c.Task(ctx, b.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.Status != contracts.StatusInProgress || got.Title != "task b" || len(got.Dependencies) != 1 {
		t.Errorf("updated task = %+v, want status in_progress, title/dependencies preserved", got)
	}

	// No cycle yet.
	cycles, err := c.Cycles(ctx, "ses_y")
	if err != nil {
		t.Fatalf("Cycles: %v", err)
	}
	if len(cycles) != 0 {
		t.Fatalf("cycles = %v, want none", cycles)
	}

	// Introduce one: a depends on b, b depends on a.
	a.Dependencies = []string{b.ID}
	if err := c.UpdateTask(ctx, &a); err != nil {
		t.Fatalf("UpdateTask a: %v", err)
	}
	cycles, err = c.Cycles(ctx, "ses_y")
	if err != nil {
		t.Fatalf("Cycles after cycle introduced: %v", err)
	}
	if len(cycles) != 1 {
		t.Fatalf("cycles = %v, want exactly one cycle", cycles)
	}
}

// Notes: add, confirm, discard, all proxied.
func TestTaskNotesThroughRouter(t *testing.T) {
	srv := newTestServer(t)
	c := dataclient.New(srv.URL, "test-verify")
	ctx := context.Background()

	task := contracts.Task{SessionID: "ses_z", Title: "task with notes", Source: "user"}
	if err := c.CreateTask(ctx, &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	n := contracts.Note{Type: contracts.NoteDecision, Content: "chose X", Source: "user", Status: contracts.NoteStatusConfirmed}
	if err := c.AddNote(ctx, task.ID, &n); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if n.ID == 0 {
		t.Fatal("note has no ID assigned")
	}

	draft := contracts.Note{Type: contracts.NoteRisk, Content: "might break", Source: "synthesizer", Status: contracts.NoteStatusDraft}
	if err := c.AddNote(ctx, task.ID, &draft); err != nil {
		t.Fatalf("AddNote draft: %v", err)
	}

	if err := c.ConfirmNote(ctx, task.ID, draft.ID); err != nil {
		t.Fatalf("ConfirmNote: %v", err)
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	var found bool
	for _, note := range got.Notes {
		if note.ID == draft.ID {
			found = true
			if note.Status != contracts.NoteStatusConfirmed {
				t.Errorf("note status = %q, want confirmed", note.Status)
			}
		}
	}
	if !found {
		t.Fatal("confirmed note not found on task")
	}

	other := contracts.Note{Type: contracts.NoteRisk, Content: "discard me", Source: "synthesizer", Status: contracts.NoteStatusDraft}
	if err := c.AddNote(ctx, task.ID, &other); err != nil {
		t.Fatalf("AddNote other: %v", err)
	}
	if err := c.DiscardNote(ctx, task.ID, other.ID); err != nil {
		t.Fatalf("DiscardNote: %v", err)
	}
	got, err = c.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	for _, note := range got.Notes {
		if note.ID == other.ID {
			t.Fatal("discarded note still present")
		}
	}
}

// Bulk import, atomically, with intra-batch dependencies.
func TestImportTasksThroughRouter(t *testing.T) {
	srv := newTestServer(t)
	c := dataclient.New(srv.URL, "test-verify")
	ctx := context.Background()

	imported, err := c.ImportTasks(ctx, []contracts.Task{
		{SessionID: "ses_import", Title: "first", Source: "user"},
		{SessionID: "ses_import", Title: "second", Source: "user"},
	})
	if err != nil {
		t.Fatalf("ImportTasks: %v", err)
	}
	if len(imported) != 2 {
		t.Fatalf("got %d imported tasks, want 2", len(imported))
	}

	list, err := c.Tasks(ctx, "ses_import", "")
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d tasks, want 2", len(list))
	}
}

// GET /sessions/{id}/files, proxied from the Data Service's file-change log.
// Recording a file change is not a Router route (that's pkg/githook talking
// to the Data Service directly) — only the read side is proxied here.
func TestSessionFilesThroughRouter(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{})
	direct := dataclient.New(dataBase, "test-verify")
	c := dataclient.New(srv.URL, "test-verify")
	ctx := context.Background()

	sess := contracts.Session{Name: "files test"}
	if err := direct.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	fc := contracts.FileChange{SessionID: sess.ID, Path: "main.go", Change: "modified", Origin: contracts.OriginToolUse}
	if err := direct.RecordFileChange(ctx, &fc); err != nil {
		t.Fatalf("RecordFileChange: %v", err)
	}

	changes, err := c.FileChanges(ctx, sess.ID)
	if err != nil {
		t.Fatalf("FileChanges: %v", err)
	}
	if len(changes) != 1 || changes[0].Path != "main.go" {
		t.Fatalf("changes = %+v, want one main.go entry", changes)
	}
}

func TestCreateTaskValidation(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/tasks", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (Router maps validation errors to 400, not the Data Service's 422)", resp.StatusCode)
	}
}

// The task editor's suggestion source (call site A, owner decision
// 2026-07-30): grounds the Fast Model's reply against the live registry,
// dropping any hallucinated name — same rule pkg/capinfer enforces for
// session-start's agent auto (call site B). Suggestions are advisory only;
// this endpoint must never write Task.RequiredCapabilities itself.
func TestSuggestTaskCapabilitiesDropsHallucinatedNames(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{Name: "suggest-task-caps", Rules: []fake.Rule{{
		Match: "Wire up Playwright E2E coverage",
		Reply: `{"capabilities":["playwright","ghost-capability"]}`,
	}}})

	direct := dataclient.New(dataBase, "test-setup")
	cap := contracts.Capability{
		Name: "playwright", Type: contracts.CapabilitySkill,
		Config: contracts.CapabilityConfig{Path: "~/skills/playwright"},
	}
	if err := direct.CreateCapability(context.Background(), &cap); err != nil {
		t.Fatalf("CreateCapability: %v", err)
	}

	resp := postJSON(t, srv.URL+"/tasks/suggest-capabilities", map[string]string{
		"title": "Wire up Playwright E2E coverage", "description": "add browser tests",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON[struct {
		Capabilities []string `json:"capabilities"`
	}](t, resp)
	if len(body.Capabilities) != 1 || body.Capabilities[0] != "playwright" {
		t.Fatalf("capabilities = %+v, want only [\"playwright\"] (hallucinated ghost-capability dropped)", body.Capabilities)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/tasks/tsk_never_existed")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
