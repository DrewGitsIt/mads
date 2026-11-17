package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// TestChildScrubsTokensFromItsEnv runs the real pipeline binary the way the
// Router spawns it, MADS_LOCAL_TOKEN (and, adversarially, MADS_ROUTER_TOKEN)
// in its env, through a stage that dumps the environment it runs in: what a
// run starts must inherit neither (docs/LOCAL-FRONT-DOOR-DESIGN.md D4).
func TestChildScrubsTokensFromItsEnv(t *testing.T) {
	base := datatest.Start(t)
	client := dataclient.New(base, "test")
	ctx := context.Background()

	cwd := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...) //nolint:gosec // test helper, fixed argv
		cmd.Dir = cwd
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	dump := filepath.Join(t.TempDir(), "stage.env")
	ws := &contracts.Workspace{Name: "ws1", Repos: []contracts.WorkspaceRepo{{Path: cwd, Primary: true}}}
	if err := client.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	tpl := &contracts.Template{Name: "dump", Source: "name: dump\nstages:\n  - name: verify\n    command: env > " + dump + "\n"}
	if err := client.CreateTemplate(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	sess := &contracts.Session{Name: "s1", Workspace: ws.ID, Provider: "fake", Isolation: contracts.IsolationCurrentDir}
	if err := client.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := client.CreateTask(ctx, &contracts.Task{SessionID: sess.ID, Title: "t1", Status: contracts.StatusReady, Source: "user"}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(pipelineBin, //nolint:gosec // built by TestMain
		"-addr", "localhost:0", "-run-id", "run_1", "-session-id", sess.ID,
		"-template", tpl.ID, "-data-url", base, "-mads-home", t.TempDir())
	cmd.Env = append(os.Environ(),
		localauth.EnvLocalToken+"=install-token-value", localauth.EnvRouterToken+"=scoped-token-value")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pipeline child: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(dump) //nolint:gosec // test-written file
	if err != nil {
		t.Fatalf("the stage never ran: %v", err)
	}
	if !strings.Contains(string(raw), "PATH=") {
		t.Fatalf("stage env dump looks empty:\n%s", raw)
	}
	for _, leak := range []string{localauth.EnvLocalToken, localauth.EnvRouterToken, "install-token-value", "scoped-token-value"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("a pipeline stage inherited %s", leak)
		}
	}
}
