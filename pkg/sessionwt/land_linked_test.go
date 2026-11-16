package sessionwt

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// linked is a session working in place in a repo whose agent made a linked
// worktree on a new branch and committed there (as `git worktree add -b
// nested-delegates ../mads-nested`).
type linked struct {
	repo, wt string
	store    *memStore
	ops      Ops
}

func newLinked(t *testing.T) linked {
	t.Helper()
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	st := &memStore{sess: contracts.Session{ID: "ses_linked", Name: "Nested", Dir: repo}}
	return linked{repo: repo, wt: wt, store: st, ops: Ops{Store: st}}
}

func (l linked) agentCommits(t *testing.T, name, content string) {
	t.Helper()
	commitFile(t, l.wt, name, content)
	l.store.rows = append(l.store.rows, contracts.FileChange{SessionID: "ses_linked", Path: filepath.Join(l.wt, name),
		Change: "added", Origin: contracts.OriginToolUse, Attributed: true})
}

func TestLandLinkedWorktree(t *testing.T) {
	ctx := context.Background()
	l := newLinked(t)
	l.agentCommits(t, "one.txt", "1\n")
	l.agentCommits(t, "two.txt", "2\n")
	commitFile(t, l.repo, "elsewhere.txt", "main moved on\n")

	p, err := l.ops.LandPreview(ctx, "ses_linked", l.wt, "", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Blocked != nil || p.Branch != "feature" || p.Base != "main" || p.BaseSource != BaseDefault || p.ToLand != 2 ||
		len(p.Commits) != 2 || !slices.Contains(p.Bases, "main") || slices.Contains(p.Bases, "feature") {
		t.Fatalf("preview = %+v", p)
	}
	if p, _ := l.ops.LandPreview(ctx, "ses_linked", "", "", Gates{}); p.Blocked == nil || p.Blocked.Reason != contracts.LandNotIsolated {
		t.Errorf("preview of the main checkout = %+v, want nothing to land", p.Blocked)
	}

	res, err := l.ops.Land(ctx, "ses_linked", contracts.LandRequest{Repo: l.wt, Message: "Add one and two"}, Gates{})
	if err != nil || res.Outcome != contracts.LandLanded {
		t.Fatalf("land = %+v, %v", res, err)
	}
	if got := strings.TrimSpace(gitOut(t, l.repo, "log", "-1", "--format=%s%n%P", "main")); !strings.HasPrefix(got, "Add one and two\n") || len(strings.Fields(got)) != 5 {
		// subject (4 words) + one parent
		t.Errorf("main's tip = %q, want the squash with one parent", got)
	}
	for _, f := range []string{"one.txt", "two.txt", "elsewhere.txt"} {
		if _, err := os.Stat(filepath.Join(l.repo, f)); err != nil {
			t.Errorf("%s isn't in the checkout after Land", f)
		}
	}
	if head(t, l.wt, "HEAD") != head(t, l.repo, "main") {
		t.Error("the worktree's branch didn't move to the landing commit")
	}
	if b, _ := BaseOf(ctx, l.wt, "feature"); b.Base != "main" || b.Source != BaseConfirmed || b.Fork != res.MergeCommit {
		t.Errorf("base after Land = %+v, want main, confirmed, forked at the landing", b)
	}
	if n, _ := CountToLand(ctx, l.wt, "main", "feature"); n != 0 {
		t.Errorf("to land after Land = %d, want 0", n)
	}
}

// The nested-delegates case: main was rewritten after the branch was cut,
// so the branch carries copies of commits main already has. Land brings
// only the branch's real change.
func TestLandLinkedWorktreeWithCopies(t *testing.T) {
	ctx := context.Background()
	l := newLinked(t)
	// Both get the same change as different commits (a rebase on push
	// rewrote main's), and the branch adds one real one.
	commitFile(t, l.wt, "shared.txt", "s\n")
	commitFile(t, l.repo, "shared.txt", "s\n")
	l.agentCommits(t, "real.txt", "r\n")

	p, err := l.ops.LandPreview(ctx, "ses_linked", l.wt, "", Gates{})
	if err != nil || p.ToLand != 1 || len(p.Commits) != 1 || p.Commits[0].Subject != "add real.txt" {
		t.Fatalf("preview = %+v, %v; want just the real commit", p, err)
	}
	res, err := l.ops.Land(ctx, "ses_linked", contracts.LandRequest{Repo: l.wt, Message: "Add real"}, Gates{})
	if err != nil || res.Outcome != contracts.LandLanded {
		t.Fatalf("land = %+v, %v", res, err)
	}
	if n := strings.TrimSpace(gitOut(t, l.repo, "rev-list", "--count", "HEAD~1..main")); n != "1" {
		t.Errorf("commits added to main = %s, want 1", n)
	}
	if diff := strings.TrimSpace(gitOut(t, l.repo, "diff", "--name-only", "main~1", "main")); diff != "real.txt" {
		t.Errorf("the landing changed %q, want only real.txt", diff)
	}
}

// A chosen base is used, and kept as confirmed.
func TestLandLinkedWorktreeChosenBase(t *testing.T) {
	ctx := context.Background()
	l := newLinked(t)
	gitRun(t, l.repo, "branch", "release")
	l.agentCommits(t, "fix.txt", "f\n")
	res, err := l.ops.Land(ctx, "ses_linked", contracts.LandRequest{Repo: l.wt, Base: "release", Message: "Fix for release"}, Gates{})
	if err != nil || res.Outcome != contracts.LandLanded || res.Base != "release" || head(t, l.repo, "release") != res.MergeCommit {
		t.Fatalf("land into release = %+v, %v", res, err)
	}
	if head(t, l.repo, "main") == res.MergeCommit {
		t.Error("main moved too")
	}
	if b, _ := BaseOf(ctx, l.wt, "feature"); b.Base != "release" || b.Source != BaseConfirmed {
		t.Errorf("base after Land = %+v, want release, confirmed", b)
	}
}
