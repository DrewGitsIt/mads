package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

type pickupBody struct {
	SharedDirectory bool `json:"shared_directory"`
	Tasks           []struct {
		TaskID          string `json:"task_id"`
		Mode            string `json:"mode"`
		FromStage       string `json:"from_stage"`
		LastRunSeq      int64  `json:"last_run_seq"`
		LastRunID       string `json:"last_run_id"`
		TemplateName    string `json:"template_name"`
		TemplateVersion int    `json:"template_version"`
	} `json:"tasks"`
}

const pickupTestTpl = "name: tpl\nstages:\n  - name: build\n    command: make\n  - name: code_review\n    command: make review\n  - name: docs\n    command: make docs\n"

// pickupFixture is a session with one task of each kind the pickup endpoint
// must tell apart.
type pickupFixture struct {
	sess    *contracts.Session
	tpl     *contracts.Template
	runSeq  int64
	resumed string // Interrupted mid code_review, with a usable record
	bare    string // Interrupted with no stage events
	byUser  string // in_progress, set by a person: not Interrupted
	held    string // in_progress under a live pipeline lease: not Interrupted
	ready   string
}

func setupPickup(t *testing.T, base string, isolation string) pickupFixture {
	t.Helper()
	ctx := context.Background()
	data := dataclient.New(base, "test")
	pipe := dataclient.New(base, "pipeline")

	ws := &contracts.Workspace{Name: "ws-" + isolation, Repos: []contracts.WorkspaceRepo{{Path: t.TempDir(), Primary: true}}}
	if err := data.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	f := pickupFixture{
		sess: &contracts.Session{Name: "s-" + isolation, Workspace: ws.ID, Isolation: isolation, Provider: "fake"},
		tpl:  &contracts.Template{Name: "tpl-" + isolation, Source: pickupTestTpl},
	}
	f.tpl.Source = "name: " + f.tpl.Name + pickupTestTpl[len("name: tpl"):]
	if err := data.CreateTemplate(ctx, f.tpl); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateSession(ctx, f.sess); err != nil {
		t.Fatal(err)
	}
	mk := func(title string) string {
		tk := &contracts.Task{SessionID: f.sess.ID, Title: title, Status: contracts.StatusReady, Source: "user"}
		if err := data.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		return tk.ID
	}
	setStatus := func(c *dataclient.Client, id string) {
		tk, err := c.Task(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		tk.Status = contracts.StatusInProgress
		if err := c.UpdateTask(ctx, &tk); err != nil {
			t.Fatal(err)
		}
	}
	appendEv := func(typ contracts.EventType, task string, payload map[string]any) int64 {
		raw, _ := json.Marshal(payload)
		env := contracts.Envelope{Type: typ, SessionID: f.sess.ID, TaskID: task, Payload: raw}
		if err := pipe.Append(ctx, &env); err != nil {
			t.Fatalf("append %s: %v", typ, err)
		}
		return env.Seq
	}

	f.resumed, f.bare, f.byUser, f.held, f.ready = mk("resumed"), mk("bare"), mk("byUser"), mk("held"), mk("ready")
	setStatus(pipe, f.resumed)
	setStatus(pipe, f.bare)
	setStatus(data, f.byUser)
	setStatus(pipe, f.held)
	if err := pipe.Claim(ctx, f.held, "pipeline:run_live", time.Minute); err != nil {
		t.Fatal(err)
	}

	f.runSeq = appendEv(contracts.PipelineStarted, "", map[string]any{
		"run_id": "run_old", "template": f.tpl.Name, "template_version": f.tpl.Version,
	})
	stage := func(typ contracts.EventType, name string, extra map[string]any) {
		p := map[string]any{"stage": name, "run_seq": f.runSeq}
		for k, v := range extra {
			p[k] = v
		}
		appendEv(typ, f.resumed, p)
	}
	stage(contracts.StageStarted, "build", map[string]any{"attempt": 1})
	stage(contracts.StageCompleted, "build", nil)
	stage(contracts.StageStarted, "code_review", map[string]any{"attempt": 1})
	return f
}

func getPickup(t *testing.T, srvURL, sessionID, template string) (*http.Response, pickupBody) {
	t.Helper()
	resp, err := http.Get(srvURL + "/sessions/" + sessionID + "/pipeline/pickup?template=" + template)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp, pickupBody{}
	}
	return resp, decodeJSON[pickupBody](t, resp)
}

func TestPipelinePickup(t *testing.T) {
	srv, base := newTestServerWithData(t)
	ctx := context.Background()
	data := dataclient.New(base, "test")
	f := setupPickup(t, base, "worktree")

	resp, got := getPickup(t, srv.URL, f.sess.ID, f.tpl.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got.SharedDirectory {
		t.Error("worktree session reported a shared directory")
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("tasks = %+v, want the two Interrupted ones (not the person's, the leased or the ready task)", got.Tasks)
	}
	byID := map[string]int{}
	for i, pt := range got.Tasks {
		byID[pt.TaskID] = i
	}
	r, ok := byID[f.resumed]
	if !ok {
		t.Fatalf("resumed task missing: %+v", got.Tasks)
	}
	res := got.Tasks[r]
	if res.Mode != "resume" || res.FromStage != "code_review" || res.LastRunSeq != f.runSeq || res.LastRunID != "run_old" ||
		res.TemplateName != f.tpl.Name || res.TemplateVersion != f.tpl.Version {
		t.Errorf("resumed = %+v, want resume at code_review from run_old/%d under %s v%d", res, f.runSeq, f.tpl.Name, f.tpl.Version)
	}
	bare := got.Tasks[byID[f.bare]]
	if bare.Mode != "restart" || bare.FromStage != "build" || bare.LastRunSeq != 0 || bare.LastRunID != "" {
		t.Errorf("bare = %+v, want restart at build with no last run", bare)
	}

	// Another template: restart at its first stage, under its name.
	other := &contracts.Template{Name: "other", Source: "name: other\nstages:\n  - name: lint\n    command: make lint\n"}
	if err := data.CreateTemplate(ctx, other); err != nil {
		t.Fatal(err)
	}
	_, got = getPickup(t, srv.URL, f.sess.ID, other.ID)
	for _, pt := range got.Tasks {
		if pt.Mode != "restart" || pt.FromStage != "lint" || pt.TemplateName != "other" {
			t.Errorf("other template: %+v, want restart at lint under other", pt)
		}
	}

	// The same template, edited since: restart too.
	f.tpl.Source += "  - name: ship\n    command: make ship\n"
	if err := data.UpdateTemplate(ctx, f.tpl); err != nil {
		t.Fatal(err)
	}
	_, got = getPickup(t, srv.URL, f.sess.ID, f.tpl.ID)
	for _, pt := range got.Tasks {
		if pt.TaskID == f.resumed && (pt.Mode != "restart" || pt.FromStage != "build" || pt.LastRunSeq != f.runSeq) {
			t.Errorf("edited template: %+v, want restart at build, still naming the last run", pt)
		}
		if pt.TemplateVersion != f.tpl.Version {
			t.Errorf("template_version = %d, want the current %d", pt.TemplateVersion, f.tpl.Version)
		}
	}

	if resp, _ := getPickup(t, srv.URL, f.sess.ID, ""); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no template: status = %d, want 400", resp.StatusCode)
	}
	if resp, _ := getPickup(t, srv.URL, f.sess.ID, "tpl_nope"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown template: status = %d, want 400", resp.StatusCode)
	}
}

func TestPipelinePickupSharedDirectory(t *testing.T) {
	srv, base := newTestServerWithData(t)
	f := setupPickup(t, base, contracts.IsolationCurrentDir)
	_, got := getPickup(t, srv.URL, f.sess.ID, f.tpl.ID)
	if !got.SharedDirectory {
		t.Error("current_dir session: shared_directory = false")
	}
	if len(got.Tasks) != 2 {
		t.Errorf("tasks = %+v, want 2", got.Tasks)
	}
}

// A session whose only work is Interrupted tasks can start a run: the run
// picks them up. Excluding them all leaves nothing to do.
func TestStartPipelineWithOnlyInterruptedTasks(t *testing.T) {
	srv, base := newTestServerWithData(t)
	ctx := context.Background()
	data := dataclient.New(base, "test")
	f := setupPickup(t, base, "worktree")
	tk, err := data.Task(ctx, f.ready)
	if err != nil {
		t.Fatal(err)
	}
	tk.Status = contracts.StatusDone
	if err := data.UpdateTask(ctx, &tk); err != nil {
		t.Fatal(err)
	}
	start := func(exclude ...string) *http.Response {
		return postJSON(t, srv.URL+"/pipelines", map[string]any{
			"session_id": f.sess.ID, "template_id": f.tpl.ID, "exclude_task_ids": exclude,
		})
	}

	resp := start(f.resumed, f.bare)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("all Interrupted excluded: status = %d, want 400", resp.StatusCode)
	}
	if body := decodeJSON[map[string]string](t, resp); body["error"] != "session has no ready or interrupted tasks" {
		t.Fatalf("body = %+v", body)
	}

	resp = start(f.bare)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("one Interrupted task: status = %d, want 201", resp.StatusCode)
	}
	started := decodeJSON[struct {
		RunID string `json:"run_id"`
	}](t, resp)
	resp = postJSON(t, srv.URL+"/pipelines/"+started.RunID+"/cancel", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel status = %d, want 204", resp.StatusCode)
	}
}
