package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func TestHealth(t *testing.T) {
	srv := httptest.NewServer(newMux())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /health = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["service"] != "pipeline" || body["status"] != "ok" {
		t.Errorf("unexpected health body: %v", body)
	}
}

// TestRunDrainsSeededBatchLikeCLI exercises child-mode's exact resolution +
// execution chain (run() -> pipelinerun.Run -> engine.Run) — the same
// engine.Run cmd/mads/run.go drives — over a seeded session/workspace/
// template/task, and checks the task drains to done and the pipeline.*
// events the UI subscribes to fire, same as a CLI `mads run` would produce.
func TestRunDrainsSeededBatchLikeCLI(t *testing.T) {
	base := datatest.Start(t)
	client := dataclient.New(base, "test")

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "Makefile"), []byte("test:\n\t@true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "add", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...) //nolint:gosec // test helper, fixed argv
		cmd.Dir = cwd
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	ws := &contracts.Workspace{Name: "ws1", Repos: []contracts.WorkspaceRepo{{Path: cwd, Primary: true}}}
	if err := client.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	tpl := &contracts.Template{Name: "nightly", Source: "name: nightly\nstages:\n  - name: verify\n    command: make test\n"}
	if err := client.CreateTemplate(context.Background(), tpl); err != nil {
		t.Fatal(err)
	}
	sess := &contracts.Session{Name: "s1", Workspace: ws.ID, Provider: "fake", Isolation: contracts.IsolationCurrentDir}
	if err := client.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	task := &contracts.Task{SessionID: sess.ID, Title: "t1", Status: contracts.StatusReady, Source: "user"}
	if err := client.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	if err := run(context.Background(), "run_1", sess.ID, tpl.ID, base, t.TempDir()); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := client.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.StatusDone {
		t.Fatalf("task status = %s, want done", got.Status)
	}

	evs, err := client.Events(context.Background(), 0, sess.ID, []string{"pipeline.started", "pipeline.completed"})
	if err != nil {
		t.Fatal(err)
	}
	var started, completed int
	for _, ev := range evs {
		switch ev.Type {
		case "pipeline.started":
			started++
		case "pipeline.completed":
			completed++
		}
	}
	if started != 1 || completed != 1 {
		t.Fatalf("pipeline events: started=%d completed=%d, want 1 each", started, completed)
	}
}
