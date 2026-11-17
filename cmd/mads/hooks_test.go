package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/githook"
	"github.com/drewkidwell/mads/services/data/datatest"
)

const v1Hook = "#!/bin/sh\n# installed by mads — reports commits with task context (spec §5)\nexit 0\n"

func gitInit(t *testing.T, dir string, hook string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q", "-b", "main", "."}, {"config", "extensions.worktreeConfig", "true"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if hook != "" {
		if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "post-commit"), []byte(hook), 0o755); err != nil { //nolint:gosec // a hook is executable
			t.Fatal(err)
		}
	}
}

// TestHooksRefresh: every repo mads knows gets hook v2 and the token path,
// and a session's commits report again; what can't be refreshed is listed
// with why (docs/LOCAL-FRONT-DOOR-DESIGN.md D6).
func TestHooksRefresh(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl on PATH (the hook depends on it)")
	}
	g := datatest.StartGuarded(t)
	t.Setenv("MADS_HOME", g.Home)
	ctx := context.Background()
	data := dataclient.New(g.URL, "user", dataclient.WithToken(g.Token))

	wsRepo, sessDir, plain, foreign := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	gitInit(t, wsRepo, v1Hook)
	gitInit(t, sessDir, v1Hook)
	gitInit(t, foreign, "#!/bin/sh\necho mine\n")
	ws := &contracts.Workspace{Name: "ws", Repos: []contracts.WorkspaceRepo{{Path: wsRepo, Primary: true}, {Path: foreign}}}
	if err := data.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	sess := contracts.Session{Name: "s", Provider: "fake", Dir: sessDir}
	other := contracts.Session{Name: "o", Provider: "fake", Dir: plain}
	gone := contracts.Session{Name: "g", Provider: "fake", Dir: filepath.Join(plain, "gone")}
	for _, s := range []*contracts.Session{&sess, &other, &gone} {
		if err := data.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// What a session's runner stamped before v2: session and URL, no token path.
	if err := githook.SetWorktreeConfig(ctx, sessDir, githook.Config{SessionID: sess.ID, DataURL: g.URL}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cmdHooks([]string{"refresh", "-data", g.URL}, &out); err != nil {
		t.Fatal(err)
	}
	report := out.String()
	for _, want := range []string{
		"updated " + wsRepo, "updated " + sessDir,
		"skipped " + plain, "not a git repo",
		"skipped " + foreign, "foreign post-commit hook",
		"skipped " + filepath.Join(plain, "gone"), "no such directory",
		"2 updated, 3 skipped",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	for _, dir := range []string{wsRepo, sessDir} {
		raw, _ := os.ReadFile(filepath.Join(dir, ".git", "hooks", "post-commit")) //nolint:gosec // test path
		if string(raw) != githook.Script {
			t.Errorf("%s still has the old hook", dir)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(foreign, ".git", "hooks", "post-commit")); !strings.Contains(string(raw), "echo mine") { //nolint:gosec // test path
		t.Error("a foreign hook was overwritten")
	}

	// The refreshed session folder reports its commits again.
	cmd := exec.Command("git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "after refresh")
	cmd.Dir = sessDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if commits, err := data.Commits(ctx, sess.ID); err == nil && len(commits) == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the refreshed hook never reported the commit")
}
