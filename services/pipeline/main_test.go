package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// pipelineBin is the real compiled services/pipeline binary, built once in
// TestMain — TestChildReleasesLeaseOnCancel needs to exercise main.go's
// actual signal-handling wiring (an in-process call to run() alone can't:
// the bug under test lives in main's select/ctx.Done() branch).
var pipelineBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mads-pipeline-main-test")
	if err != nil {
		panic(err)
	}
	pipelineBin = filepath.Join(dir, "mads-pipeline")
	build := exec.Command("go", "build", "-o", pipelineBin, "github.com/drewkidwell/mads/services/pipeline") //nolint:gosec // fixed argv
	if out, err := build.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("build services/pipeline: %v\n%s", err, out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestResolveAgentEnv_InheritOmitsCredEnvToStayByteIdentical(t *testing.T) {
	parent := []string{"PATH=/bin", "MADS_CRED_A=secret-value"}
	cfg := contracts.Config{CredentialBindings: []string{"MADS_CRED_A"}}

	mode, passthrough, credEnv := resolveAgentEnv(cfg, parent)

	if mode == agentenv.ModeIsolated {
		t.Fatalf("mode = %v, want inherit (zero value)", mode)
	}
	if passthrough != nil || credEnv != nil {
		t.Fatalf("inherit mode should resolve no passthrough/credEnv (parentEnv already has it): passthrough=%v credEnv=%v", passthrough, credEnv)
	}
}

func TestResolveAgentEnv_IsolatedSelectsBoundCredentialsFromParentEnv(t *testing.T) {
	parent := []string{"PATH=/bin", "MADS_CRED_A=secret-value", "OTHER=x"}
	cfg := contracts.Config{
		EnvMode:            contracts.EnvModeIsolated,
		EnvPassthrough:     []string{"AWS_PROFILE"},
		CredentialBindings: []string{"MADS_CRED_A"},
	}

	mode, passthrough, credEnv := resolveAgentEnv(cfg, parent)

	if mode != agentenv.ModeIsolated {
		t.Fatalf("mode = %v, want isolated", mode)
	}
	if !reflect.DeepEqual(passthrough, cfg.EnvPassthrough) {
		t.Fatalf("passthrough = %v, want %v", passthrough, cfg.EnvPassthrough)
	}
	want := []string{"MADS_CRED_A=secret-value"}
	if !reflect.DeepEqual(credEnv, want) {
		t.Fatalf("credEnv = %v, want %v", credEnv, want)
	}
}

// TestBuildProviderRegistryIncludesGoose pins the registration set the
// pipeline child boots with: goose alongside the pre-existing three.
func TestBuildProviderRegistryIncludesGoose(t *testing.T) {
	reg, err := buildProviderRegistry(t.TempDir(), agentenv.ModeInherit, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude-code", "kiro-cli", "goose", "fake"} {
		if _, err := reg.Get(name); err != nil {
			t.Fatalf("Get(%q): %v", name, err)
		}
	}
}

// TestBuildProviderRegistryFakeScenario: -fake-scenario scripts the fake the
// same way `mads run -fake-scenario` does, and a scenario that can't be
// loaded fails the run up front instead of silently running unscripted.
func TestBuildProviderRegistryFakeScenario(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scenario.yaml")
	src := "name: t\nrules:\n  - match: \"\"\n    reply: \"STAGE: pass\"\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := buildProviderRegistry(dir, agentenv.ModeInherit, nil, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Get("fake"); err != nil {
		t.Fatalf("Get(fake): %v", err)
	}
	if _, err := buildProviderRegistry(dir, agentenv.ModeInherit, nil, nil, filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing scenario file: want an error")
	}
}

func TestResolveStageTimeout_DefaultsWhenAbsentOrZero(t *testing.T) {
	for _, minutes := range []int{0, -1} {
		got := resolveStageTimeout(contracts.Config{PipelineStageTimeoutMinutes: minutes})
		want := time.Duration(contracts.DefaultPipelineStageTimeoutMinutes) * time.Minute
		if got != want {
			t.Fatalf("PipelineStageTimeoutMinutes=%d: resolveStageTimeout = %v, want %v", minutes, got, want)
		}
	}
}

func TestResolveStageTimeout_HonorsConfiguredValue(t *testing.T) {
	got := resolveStageTimeout(contracts.Config{PipelineStageTimeoutMinutes: 5})
	want := 5 * time.Minute
	if got != want {
		t.Fatalf("resolveStageTimeout = %v, want %v", got, want)
	}
}

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

	if err := run(context.Background(), "run_1", sess.ID, tpl.ID, base, t.TempDir(), "", nil); err != nil {
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

// TestChildReleasesLeaseOnCancel is the regression for main's ctx.Done()
// branch: on SIGTERM mid-run, main must wait for run()'s goroutine (and
// pipelinerun.Run/engine.Run's own cleanup underneath it) to finish before
// the process exits, or the task lease it holds is never released. Spawns
// the real compiled binary — not an in-process call to run() — because the
// bug lives in main's own select statement, not in the engine underneath
// it, and only a real SIGTERM against the real process exercises it.
func TestChildReleasesLeaseOnCancel(t *testing.T) {
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
	// A stage that outlives the SIGTERM we're about to send, so the child
	// is still mid-run (task leased, sleep subprocess in flight) at the
	// moment of cancellation.
	tpl := &contracts.Template{Name: "slow", Source: "name: slow\nstages:\n  - name: verify\n    command: sleep 30\n"}
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

	cmd := exec.Command(pipelineBin, //nolint:gosec // pipelineBin is built by TestMain, argv is test-constructed
		"-addr", "localhost:0",
		"-run-id", "run_1",
		"-session-id", sess.ID,
		"-template", tpl.ID,
		"-data-url", base,
		"-mads-home", t.TempDir(),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start pipeline child: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("pipeline child stderr:\n%s", stderr.String())
		}
	})
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// Wait for the task to actually be claimed and running before
	// cancelling — SIGTERM landing before the child ever claimed anything
	// wouldn't exercise the mid-run cleanup path this test is for.
	deadline := time.Now().Add(10 * time.Second)
	var got contracts.Task
	for {
		var err error
		got, err = client.Task(context.Background(), task.ID)
		if err == nil && got.Status == contracts.StatusInProgress {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			<-exited
			t.Fatalf("task never reached in_progress: status=%v err=%v", got.Status, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal SIGTERM: %v", err)
	}

	select {
	case <-exited:
	case <-time.After(shutdownGrace + 5*time.Second):
		_ = cmd.Process.Kill()
		<-exited
		t.Fatal("pipeline child did not exit within shutdownGrace of SIGTERM")
	}

	// run()'s own dataclient authenticates as actor "pipeline" — checking
	// via that same actor's Leases() view proves the child released the
	// lease itself before exiting, not that some other backstop (e.g. the
	// Router's onPipelineExit ReleaseAll, which this test bypasses
	// entirely by spawning the binary directly) papered over a leak.
	pipelineActor := dataclient.New(base, "pipeline")
	leases, err := pipelineActor.Leases(context.Background())
	if err != nil {
		t.Fatalf("Leases: %v", err)
	}
	for _, l := range leases {
		if l.TaskID == task.ID {
			t.Fatalf("task %s still leased by %q after child exited on cancel", task.ID, l.Holder)
		}
	}
}

// gitRepo makes a repo on main with one commit and a repo-local identity
// (CI has no global one).
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test:\n\t@true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", "."},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"add", "."},
		{"commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...) //nolint:gosec // test helper, fixed argv
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // test helper, fixed argv
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// seedRun creates a template whose one command stage leaves a file named
// after the run in whatever directory the stage runs in, and one ready
// task for sess.
func seedRun(t *testing.T, client *dataclient.Client, sess *contracts.Session, src string) *contracts.Template {
	t.Helper()
	ctx := context.Background()
	tpl := &contracts.Template{Name: "where-" + sess.Name, Source: src}
	if err := client.CreateTemplate(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	if err := client.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	task := &contracts.Task{SessionID: sess.ID, Title: "t1", Status: contracts.StatusReady, Source: "user"}
	if err := client.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	return tpl
}

// Problem 6: a session started in a folder of its own runs its pipeline
// there — in the top of that folder's repo — never in the default
// workspace's repo.
func TestRunUsesSessionDirNotWorkspaceRepo(t *testing.T) {
	base := datatest.Start(t)
	client := dataclient.New(base, "test")
	ctx := context.Background()

	wsRepo, dirRepo := gitRepo(t), gitRepo(t)
	sub := filepath.Join(dirRepo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ws := &contracts.Workspace{Name: "ws1", Repos: []contracts.WorkspaceRepo{{Path: wsRepo, Primary: true}}}
	if err := client.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		sess contracts.Session
	}{
		{"with a workspace", contracts.Session{Name: "s1", Workspace: ws.ID, Dir: sub, Provider: "fake", Isolation: contracts.IsolationCurrentDir}},
		{"without one", contracts.Session{Name: "s2", Dir: sub, Provider: "fake", Isolation: contracts.IsolationCurrentDir}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := "ran-" + strings.ReplaceAll(tc.name, " ", "-")
			tpl := seedRun(t, client, &tc.sess, "name: where-"+tc.sess.Name+"\nstages:\n  - name: verify\n    command: touch "+marker+"\n")
			if err := run(ctx, "run_1", tc.sess.ID, tpl.ID, base, t.TempDir(), "", nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dirRepo, marker)); err != nil {
				t.Errorf("the stage didn't run at the top of the session folder's repo")
			}
			if _, err := os.Stat(filepath.Join(wsRepo, marker)); err == nil {
				t.Errorf("the stage ran in the workspace's repo")
			}
		})
	}
}

// D8 end to end through run(): an isolated session whose worktree was lost
// gets it back, its task lands on the session branch, and the user's
// checkout is untouched.
func TestRunIsolatedSessionLandsOnSessionBranch(t *testing.T) {
	base := datatest.Start(t)
	client := dataclient.New(base, "test")
	ctx := context.Background()

	primary := gitRepo(t)
	headBefore := gitIn(t, primary, "rev-parse", "HEAD")
	sess := contracts.Session{Name: "iso", Dir: primary, Provider: "fake", Isolation: contracts.IsolationWorktree}
	tpl := seedRun(t, client, &sess, `name: where-iso
stages:
  - name: implement
    command: echo hi > piped.txt
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: make test
`)
	folder := filepath.Join(t.TempDir(), "session-worktrees", sess.ID)
	branch, baseBranch := "mads/session/"+sess.ID, "main"
	if err := client.SetSessionWorktree(ctx, sess.ID, contracts.SessionWorktreePatch{
		WorktreeDir: &folder, WorktreeBranch: &branch, WorktreeBase: &baseBranch,
	}); err != nil {
		t.Fatal(err)
	}

	if err := run(ctx, "run_1", sess.ID, tpl.ID, base, t.TempDir(), "", nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "piped.txt")); err != nil {
		t.Errorf("the task's work isn't in the session folder: %v", err)
	}
	if got := gitIn(t, primary, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("the user's checkout moved: %s -> %s", headBefore, got)
	}
	if st := gitIn(t, primary, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("the user's checkout is dirty:\n%s", st)
	}
	if n := strings.TrimSpace(gitIn(t, primary, "rev-list", "--count", "main.."+branch)); n == "0" {
		t.Errorf("the session branch has nothing unlanded")
	}
	evs, err := client.Events(ctx, 0, sess.ID, []string{"worktree.ensured"})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Errorf("worktree.ensured events = %d, want 1 for the re-created folder", len(evs))
	}
	got, err := client.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UnlandedCount == 0 {
		t.Errorf("unlanded_count wasn't refreshed after the merge")
	}
}
