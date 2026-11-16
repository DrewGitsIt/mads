package sessionwt

import (
	"context"
	"path/filepath"
	"testing"
)

// BaseOf, in order of trust: a recorded or confirmed base, the reflog's
// "Created from <branch>", then the default branch; the fork commit from
// the record or the reflog's first entry.
func TestBaseOf(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	gitRun(t, repo, "branch", "dev")
	fork := head(t, repo, "HEAD")
	wt := func(name string, args ...string) string {
		dir := filepath.Join(t.TempDir(), name)
		gitRun(t, repo, append([]string{"worktree", "add", "-q", "-b", name, dir}, args...)...)
		commitFile(t, dir, name+".txt", name+"\n")
		return dir
	}

	named := wt("named", "dev")
	if b, ok := BaseOf(ctx, named, "named"); !ok || b != (LandBase{Base: "dev", Fork: fork, Source: BaseReflog}) {
		t.Errorf("created from dev = %+v, %v", b, ok)
	}
	implicit := wt("implicit")
	if b, ok := BaseOf(ctx, implicit, "implicit"); !ok || b != (LandBase{Base: "main", Fork: fork, Source: BaseDefault}) {
		t.Errorf("created from HEAD = %+v, %v", b, ok)
	}
	if err := RecordBase(ctx, implicit, "implicit", LandBase{Base: "dev", Source: BaseConfirmed}); err != nil {
		t.Fatal(err)
	}
	if b, _ := BaseOf(ctx, implicit, "implicit"); b.Base != "dev" || b.Source != BaseConfirmed || b.Fork != fork {
		t.Errorf("after confirming dev = %+v", b)
	}

	// The base moved on and was rewritten: the reflog's fork still holds,
	// and copies of the branch's commit on the base don't count to land.
	if n, err := CountToLand(ctx, named, "dev", "named"); err != nil || n != 1 {
		t.Fatalf("to land = %d, %v; want 1", n, err)
	}
	devDir := filepath.Join(t.TempDir(), "dev")
	gitRun(t, repo, "worktree", "add", "-q", devDir, "dev")
	commitFile(t, devDir, "named.txt", "named\n")
	if n, _ := CountToLand(ctx, named, "dev", "named"); n != 0 {
		t.Errorf("to land after a copy reached dev = %d, want 0", n)
	}
}

func TestBaseFromCreation(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	gitRun(t, repo, "branch", "dev")
	gitRun(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")

	gitRun(t, repo, "branch", "named", "dev")
	if b := BaseFromCreation(ctx, repo, "named", "dev", true); b != "dev" {
		t.Errorf("start dev = %q", b)
	}
	gitRun(t, repo, "branch", "tracking", "origin/main")
	if b := BaseFromCreation(ctx, repo, "tracking", "origin/main", true); b != "main" {
		t.Errorf("start origin/main = %q, want main", b)
	}
	gitRun(t, repo, "checkout", "-q", "dev")
	gitRun(t, repo, "branch", "fromhere")
	if b := BaseFromCreation(ctx, repo, "fromhere", "", true); b != "dev" {
		t.Errorf("worktree add from a checkout on dev = %q, want dev", b)
	}
	gitRun(t, repo, "checkout", "-q", "-b", "switched")
	if b := BaseFromCreation(ctx, repo, "switched", "", false); b != "dev" {
		t.Errorf("checkout -b from dev = %q, want dev", b)
	}
	if err := RecordBase(ctx, repo, "named", LandBase{Base: "main", Source: BaseConfirmed}); err != nil {
		t.Fatal(err)
	}
	if b := BaseFromCreation(ctx, repo, "named", "dev", true); b != "" {
		t.Errorf("a branch with a kept base = %q, want left alone", b)
	}
}
