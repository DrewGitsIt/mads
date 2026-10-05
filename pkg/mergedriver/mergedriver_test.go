package mergedriver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main", ".")
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func exe(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mergiraf")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho 'mergiraf 9.9.9'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFind(t *testing.T) {
	if got := Find(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("Find(missing) = %q, want \"\" (no fallback to PATH)", got)
	}
	p := exe(t)
	if got := Find(p); got != p {
		t.Errorf("Find(%s) = %q", p, got)
	}
	t.Setenv("PATH", filepath.Dir(p))
	if got := Find(""); got != p {
		t.Errorf("Find(\"\") on PATH = %q, want %q", got, p)
	}
}

// Register twice: one attributes rule, the same config, no error.
func TestRegisterIsIdempotent(t *testing.T) {
	repo := initRepo(t)
	attr := filepath.Join(repo, ".git", "info", "attributes")
	if err := os.MkdirAll(filepath.Dir(attr), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attr, []byte("*.bin binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := exe(t)
	for i := 0; i < 2; i++ {
		if err := Register(context.Background(), repo, p); err != nil {
			t.Fatalf("Register #%d: %v", i+1, err)
		}
	}
	raw, _ := os.ReadFile(attr)
	if string(raw) != "*.bin binary\n* merge=mergiraf\n" {
		t.Errorf("attributes = %q", raw)
	}
	if got := Registered(context.Background(), repo); got != Command(p) {
		t.Errorf("Registered = %q, want %q", got, Command(p))
	}
	if !Usable(Registered(context.Background(), repo)) {
		t.Error("a registered, existing binary isn't Usable")
	}
	if got := strings.TrimSpace(gitRun(t, repo, "check-attr", "merge", "--", "x.go")); got != "x.go: merge: mergiraf" {
		t.Errorf("check-attr = %q", got)
	}
}

// From a linked worktree, --git-common-dir is absolute: registration must
// land in the main checkout's .git, not a path joined onto the worktree.
func TestRegisterFromLinkedWorktree(t *testing.T) {
	main := initRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitRun(t, main, "worktree", "add", "-q", "-b", "feature", linked)
	if err := Register(context.Background(), linked, "mergiraf"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(main, ".git", "info", "attributes"))
	if err != nil || !strings.Contains(string(raw), "* merge=mergiraf") {
		t.Errorf("attributes in the main .git = %q, %v", raw, err)
	}
	if got := strings.TrimSpace(gitRun(t, linked, "check-attr", "merge", "--", "x.go")); got != "x.go: merge: mergiraf" {
		t.Errorf("check-attr in the linked worktree = %q", got)
	}
}

func TestRegisteredAndUsable(t *testing.T) {
	repo := initRepo(t)
	if got := Registered(context.Background(), repo); got != "" {
		t.Errorf("Registered on a fresh repo = %q", got)
	}
	if Usable("") || Usable(Command(filepath.Join(t.TempDir(), "gone"))) {
		t.Error("a missing binary is Usable")
	}
}

func TestVersion(t *testing.T) {
	if got := Version(context.Background(), exe(t)); got != "mergiraf 9.9.9" {
		t.Errorf("Version = %q", got)
	}
	if got := Version(context.Background(), filepath.Join(t.TempDir(), "gone")); got != "" {
		t.Errorf("Version of a missing binary = %q", got)
	}
}
