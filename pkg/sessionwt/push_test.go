package sessionwt

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// withRemote gives repo a bare origin with main pushed, and a second clone
// standing in for someone else pushing to it.
func withRemote(t *testing.T, repo string) (bare, other string) {
	t.Helper()
	bare = filepath.Join(t.TempDir(), "origin.git")
	gitRun(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	gitRun(t, repo, "remote", "add", "origin", bare)
	gitRun(t, repo, "push", "-q", "-u", "origin", "main")
	other = filepath.Join(t.TempDir(), "other")
	gitRun(t, filepath.Dir(other), "clone", "-q", bare, other)
	gitRun(t, other, "config", "user.email", "o@o")
	gitRun(t, other, "config", "user.name", "o")
	return bare, other
}

// theyPush commits a file in the other clone and pushes it.
func theyPush(t *testing.T, other, name, content string) {
	t.Helper()
	commitFile(t, other, name, content)
	gitRun(t, other, "push", "-q", "origin", "main")
}

// inPlace is a session working in place in a fresh repo with a remote.
func inPlace(t *testing.T) (repo, bare, other string, st *memStore, ops Ops) {
	t.Helper()
	repo = initRepo(t)
	bare, other = withRemote(t, repo)
	st = &memStore{sess: contracts.Session{ID: "ses_push", Name: "push", Dir: repo}}
	return repo, bare, other, st, Ops{Store: st}
}

func TestPushInPlace(t *testing.T) {
	ctx := context.Background()
	repo, bare, _, st, ops := inPlace(t)
	commitFile(t, repo, "a.txt", "a\n")
	if n, err := CountUnpushed(ctx, repo, "main"); err != nil || n != 1 {
		t.Fatalf("unpushed = %d, %v; want 1", n, err)
	}

	p, err := ops.PushPreview(ctx, st.sess.ID, "", "", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Blocked != nil || p.Ahead != 1 || p.Behind != 0 || p.Remote != "origin" || p.RemoteBranch != "main" ||
		len(p.Commits) != 1 || p.Commits[0].Subject != "add a.txt" || p.Head != head(t, repo, "HEAD") {
		t.Fatalf("preview = %+v", p)
	}
	res, err := ops.Push(ctx, st.sess.ID, contracts.PushRequest{Expect: p.Head}, Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.PushPushed || res.Pushed != 1 || res.Rebased {
		t.Fatalf("push = %+v", res)
	}
	if head(t, bare, "main") != head(t, repo, "HEAD") {
		t.Error("origin/main isn't the pushed commit")
	}
	p, _ = ops.PushPreview(ctx, st.sess.ID, "", "", Gates{})
	if p.Blocked == nil || p.Blocked.Reason != contracts.PushNothingToPush {
		t.Errorf("preview after the push = %+v, want nothing to push", p.Blocked)
	}
}

func TestPushBehindRebasesOnlyWhenAsked(t *testing.T) {
	ctx := context.Background()
	repo, bare, other, st, ops := inPlace(t)
	theyPush(t, other, "theirs.txt", "t\n")
	commitFile(t, repo, "mine.txt", "m\n")
	mine := head(t, repo, "HEAD")

	p, err := ops.PushPreview(ctx, st.sess.ID, "", "", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Behind != 1 || p.Ahead != 1 || p.RebaseBlocked != nil || len(p.Incoming) != 1 || p.Incoming[0].Subject != "add theirs.txt" {
		t.Fatalf("preview = %+v", p)
	}
	res, err := ops.Push(ctx, st.sess.ID, contracts.PushRequest{Expect: p.Head}, Gates{})
	if err != nil || res.Outcome != contracts.PushRefused || res.Reason != contracts.PushBehind {
		t.Fatalf("push without rebase = %+v, %v; want refused behind", res, err)
	}
	if head(t, repo, "HEAD") != mine {
		t.Fatal("a refused push moved the branch")
	}
	res, err = ops.Push(ctx, st.sess.ID, contracts.PushRequest{Expect: p.Head, Rebase: true}, Gates{})
	if err != nil || res.Outcome != contracts.PushPushed || !res.Rebased {
		t.Fatalf("push with rebase = %+v, %v", res, err)
	}
	if head(t, bare, "main") != head(t, repo, "HEAD") || head(t, repo, "HEAD~1") != head(t, other, "HEAD") {
		t.Error("the branch wasn't rebased onto origin's commit and pushed")
	}
}

func TestPushRebaseConflictLeavesBranchAlone(t *testing.T) {
	ctx := context.Background()
	repo, bare, other, st, ops := inPlace(t)
	theyPush(t, other, "README.md", "theirs\n")
	commitFile(t, repo, "README.md", "mine\n")
	mine, remote := head(t, repo, "HEAD"), head(t, bare, "main")

	res, err := ops.Push(ctx, st.sess.ID, contracts.PushRequest{Rebase: true}, Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.PushConflicted || !slices.Equal(res.Files, []string{"README.md"}) {
		t.Fatalf("push = %+v, want conflicted on README.md", res)
	}
	if head(t, repo, "HEAD") != mine || head(t, bare, "main") != remote || midOperation(ctx, repo) != "" {
		t.Error("the conflicted rebase left something changed")
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "README.md")); string(got) != "mine\n" {
		t.Errorf("README.md = %q, want the user's version back", got)
	}
}

func TestPushRebaseGates(t *testing.T) {
	ctx := context.Background()
	repo, _, other, st, ops := inPlace(t)
	theyPush(t, other, "shared.txt", "theirs\n")
	commitFile(t, repo, "mine.txt", "m\n")
	mine := head(t, repo, "HEAD")

	// An uncommitted file the incoming commit adds.
	writeFile(t, filepath.Join(repo, "shared.txt"), "local edit\n")
	p, err := ops.PushPreview(ctx, st.sess.ID, "", "", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if p.RebaseBlocked == nil || p.RebaseBlocked.Reason != contracts.PushOverlap || !slices.Equal(p.RebaseBlocked.Files, []string{"shared.txt"}) {
		t.Fatalf("rebase gate = %+v, want overlap on shared.txt", p.RebaseBlocked)
	}
	res, _ := ops.Push(ctx, st.sess.ID, contracts.PushRequest{Rebase: true}, Gates{})
	if res.Reason != contracts.PushOverlap || head(t, repo, "HEAD") != mine {
		t.Errorf("push with overlap = %+v, want refused and nothing moved", res)
	}
	if err := os.Remove(filepath.Join(repo, "shared.txt")); err != nil {
		t.Fatal(err)
	}

	res, _ = ops.Push(ctx, st.sess.ID, contracts.PushRequest{Rebase: true}, Gates{Busy: true})
	if res.Reason != contracts.PushBusy {
		t.Errorf("push while an agent works = %+v, want refused busy", res)
	}
	res, _ = ops.Push(ctx, st.sess.ID, contracts.PushRequest{Rebase: true, Expect: "0000000"}, Gates{})
	if res.Reason != contracts.PushMoved {
		t.Errorf("push with a stale confirmation = %+v, want refused moved", res)
	}
}

func TestPushNoRemote(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	st := &memStore{sess: contracts.Session{ID: "ses_local", Dir: repo}}
	commitFile(t, repo, "a.txt", "a\n")
	if n, err := CountUnpushed(ctx, repo, "main"); err != nil || n != 0 {
		t.Errorf("unpushed with no remote = %d, %v; want 0", n, err)
	}
	p, err := Ops{Store: st}.PushPreview(ctx, st.sess.ID, "", "", Gates{})
	if err != nil || p.Blocked == nil || p.Blocked.Reason != contracts.PushNoRemote {
		t.Errorf("preview = %+v, %v; want no remote", p.Blocked, err)
	}
}

// An isolated session pushes its own branch (a new one on the remote, with
// its upstream set) before Land, and the base from the user's checkout
// after; the header counts the base's.
func TestPushIsolatedBranchThenBase(t *testing.T) {
	ctx := context.Background()
	s := newIsolated(t)
	bare, _ := withRemote(t, s.primary)
	s.work(t, map[string]string{"feature.txt": "f\n"})
	s.commit(t, contracts.CommitRequest{Message: "Add feature"})

	p, err := s.ops.PushPreview(ctx, s.store.sess.ID, "", contracts.PushBranch, Gates{})
	if err != nil {
		t.Fatal(err)
	}
	branch := s.store.sess.WorktreeBranch
	if p.Blocked != nil || !p.NewBranch || p.Branch != branch || p.Ahead != 1 {
		t.Fatalf("branch preview = %+v", p)
	}
	res, err := s.ops.Push(ctx, s.store.sess.ID, contracts.PushRequest{Target: contracts.PushBranch}, Gates{})
	if err != nil || res.Outcome != contracts.PushPushed {
		t.Fatalf("branch push = %+v, %v", res, err)
	}
	if head(t, bare, branch) != head(t, s.folder, "HEAD") {
		t.Error("the session branch isn't on origin")
	}
	if got := gitOut(t, s.primary, "config", "--get", "branch."+branch+".remote"); got != "origin\n" {
		t.Errorf("upstream remote = %q, want origin", got)
	}

	if _, err := s.ops.Land(ctx, s.store.sess.ID, Gates{}); err != nil {
		t.Fatal(err)
	}
	c, err := countTarget(ctx, Target{SessionID: s.store.sess.ID, Folder: s.folder, Primary: s.primary, Branch: branch, Base: "main"}, nil)
	if err != nil || c.Unpushed != 2 {
		t.Fatalf("base unpushed after Land = %+v, %v; want 2 (the commit and the merge)", c, err)
	}
	res, err = s.ops.Push(ctx, s.store.sess.ID, contracts.PushRequest{Target: contracts.PushBase}, Gates{})
	if err != nil || res.Outcome != contracts.PushPushed || res.Branch != "main" {
		t.Fatalf("base push = %+v, %v", res, err)
	}
	if head(t, bare, "main") != head(t, s.primary, "main") {
		t.Error("main isn't on origin after the base push")
	}
}
