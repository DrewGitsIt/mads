package sessionwt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// gitOut/gitRun/initRepo follow services/session/internal/runner's
// merge_test.go helpers: real repos in temp dirs.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = gitOut(t, dir, args...)
}

// initRepo makes a repo on main with one commit. The identity is repo-local
// because CI has no global one, and a worktree shares its repo's config.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main", ".")
	gitRun(t, dir, "config", "user.email", "t@t")
	gitRun(t, dir, "config", "user.name", "t")
	writeFile(t, filepath.Join(dir, "README.md"), "init\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), content)
	gitRun(t, dir, "add", name)
	gitRun(t, dir, "commit", "-q", "-m", "add "+name)
}

func head(t *testing.T, dir, ref string) string {
	t.Helper()
	return strings.TrimSpace(gitOut(t, dir, "rev-parse", ref))
}

func ensure(t *testing.T, s Spec) EnsureResult {
	t.Helper()
	res, err := EnsureWithLock(context.Background(), s)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	return res
}

func newSpec(t *testing.T) Spec {
	t.Helper()
	return Spec{Primary: initRepo(t), Root: t.TempDir(), SessionID: "ses_test", Base: "main"}
}

// Both gone: the branch starts from the base passed in, not from whatever
// the primary has checked out now.
func TestEnsureCreatesFromExplicitBase(t *testing.T) {
	s := newSpec(t)
	mainHead := head(t, s.Primary, "main")
	gitRun(t, s.Primary, "checkout", "-q", "-b", "elsewhere")
	commitFile(t, s.Primary, "elsewhere.txt", "x\n")

	res := ensure(t, s)
	if res.Action != ActionCreated {
		t.Fatalf("action = %q, want created", res.Action)
	}
	if res.Dir != filepath.Join(s.Root, s.SessionID) || res.Branch != "mads/session/ses_test" {
		t.Errorf("dir/branch = %q/%q", res.Dir, res.Branch)
	}
	if got := head(t, res.Dir, "HEAD"); got != mainHead || res.StartPoint != mainHead {
		t.Errorf("worktree HEAD = %s, start point %s; want main's %s", got, res.StartPoint, mainHead)
	}
	if b := strings.TrimSpace(gitOut(t, res.Dir, "symbolic-ref", "--short", "HEAD")); b != res.Branch {
		t.Errorf("worktree on %q, want %q", b, res.Branch)
	}
	if res.BaseMissing || res.StaleMovedTo != "" || !res.Notable() {
		t.Errorf("unexpected result %+v", res)
	}
}

// A valid worktree is used as it is, edits and all.
func TestEnsureReusesValidWorktree(t *testing.T) {
	s := newSpec(t)
	first := ensure(t, s)
	writeFile(t, filepath.Join(first.Dir, "wip.txt"), "uncommitted\n")

	// Ensure (lock held by the caller) and EnsureWithLock's fast path agree.
	var again EnsureResult
	if err := WithLock(context.Background(), s.Primary, func() error {
		var err error
		again, err = Ensure(context.Background(), s)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if again.Action != ActionReused || again.Notable() || again.Dir != first.Dir {
		t.Fatalf("second ensure = %+v, want a quiet reuse of %s", again, first.Dir)
	}
	if _, err := os.Stat(filepath.Join(first.Dir, "wip.txt")); err != nil {
		t.Errorf("reuse lost the uncommitted edit: %v", err)
	}
}

// The agent switched branches inside its folder (or is mid-rebase): still
// its folder, used as is, and the result says so.
func TestEnsureReusesWorktreeOffBranch(t *testing.T) {
	s := newSpec(t)
	first := ensure(t, s)
	gitRun(t, first.Dir, "checkout", "-q", "-b", "side")
	res := ensure(t, s)
	if res.Action != ActionReused || res.Head != "side" || !res.Notable() {
		t.Fatalf("result = %+v, want reused with head side", res)
	}
	gitRun(t, first.Dir, "checkout", "-q", "--detach")
	if res := ensure(t, s); res.Action != ActionReused || res.Head != "HEAD" {
		t.Fatalf("detached: result = %+v, want reused with head HEAD", res)
	}
}

// Folder gone, branch present: checked out again at the same path, with the
// session's commits intact.
func TestEnsureReattachesLostFolder(t *testing.T) {
	s := newSpec(t)
	first := ensure(t, s)
	commitFile(t, first.Dir, "work.txt", "work\n")
	tip := head(t, first.Dir, "HEAD")
	if err := os.RemoveAll(first.Dir); err != nil {
		t.Fatal(err)
	}

	res := ensure(t, s)
	if res.Action != ActionReattached || res.Dir != first.Dir {
		t.Fatalf("result = %+v, want reattached at %s", res, first.Dir)
	}
	if got := head(t, res.Dir, "HEAD"); got != tip {
		t.Errorf("HEAD = %s, want the branch tip %s", got, tip)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "work.txt")); err != nil {
		t.Errorf("committed work missing after reattach: %v", err)
	}
}

// A locked registration survives prune; Ensure unlocks it first.
func TestEnsureUnlocksLockedStaleEntry(t *testing.T) {
	s := newSpec(t)
	first := ensure(t, s)
	gitRun(t, s.Primary, "worktree", "lock", "--reason", "test", first.Dir)
	if err := os.RemoveAll(first.Dir); err != nil {
		t.Fatal(err)
	}

	res := ensure(t, s)
	if res.Action != ActionReattached || !res.UnlockedStale {
		t.Fatalf("result = %+v, want reattached after unlocking", res)
	}
	if list := gitOut(t, s.Primary, "worktree", "list", "--porcelain"); strings.Contains(list, "locked") {
		t.Errorf("registration still locked:\n%s", list)
	}
}

// Something that isn't this session's worktree sits at the path: moved
// aside intact, never deleted, and the worktree made fresh.
func TestEnsureMovesAsideForeignPath(t *testing.T) {
	s := newSpec(t)
	path := Path(s.Root, s.SessionID)
	writeFile(t, filepath.Join(path, "keep.txt"), "mine\n")

	res := ensure(t, s)
	if res.Action != ActionCreated || res.StaleMovedTo == "" {
		t.Fatalf("result = %+v, want created after moving aside", res)
	}
	if !strings.HasPrefix(res.StaleMovedTo, path+".stale-") {
		t.Errorf("moved to %q, want %s.stale-<ts>", res.StaleMovedTo, path)
	}
	if b, err := os.ReadFile(filepath.Join(res.StaleMovedTo, "keep.txt")); err != nil || string(b) != "mine\n" {
		t.Errorf("moved-aside content = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "README.md")); err != nil {
		t.Errorf("new worktree not checked out: %v", err)
	}
}

// A registered worktree whose folder was replaced by something else: moved
// aside, the stale registration pruned, and the surviving branch reattached.
func TestEnsureMovesAsideReplacedWorktree(t *testing.T) {
	s := newSpec(t)
	first := ensure(t, s)
	commitFile(t, first.Dir, "work.txt", "work\n")
	if err := os.RemoveAll(first.Dir); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(first.Dir, "stray.txt"), "?\n")

	res := ensure(t, s)
	if res.Action != ActionReattached || res.StaleMovedTo == "" {
		t.Fatalf("result = %+v, want reattached after moving aside", res)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "work.txt")); err != nil {
		t.Errorf("branch content missing: %v", err)
	}
}

// A folder that sits inside another repo is not a worktree of this one.
func TestEnsureMovesAsideNestedRepoFolder(t *testing.T) {
	s := newSpec(t)
	other := initRepo(t)
	s.Root = filepath.Join(other, "nested")
	writeFile(t, filepath.Join(s.Root, s.SessionID, "x.txt"), "x\n")

	res := ensure(t, s)
	if res.StaleMovedTo == "" || res.Action != ActionCreated {
		t.Fatalf("result = %+v, want the folder inside another repo moved aside", res)
	}
}

// Both gone, after a Land-and-delete or a manual cleanup: made again.
func TestEnsureRecreatesWhenBothGone(t *testing.T) {
	s := newSpec(t)
	first := ensure(t, s)
	gitRun(t, s.Primary, "worktree", "remove", "--force", first.Dir)
	gitRun(t, s.Primary, "branch", "-D", first.Branch)

	if res := ensure(t, s); res.Action != ActionCreated || res.BaseMissing {
		t.Fatalf("result = %+v, want created", res)
	}
}

// The base branch was deleted: start from the primary's HEAD and say so.
func TestEnsureFallsBackWhenBaseDeleted(t *testing.T) {
	s := newSpec(t)
	gitRun(t, s.Primary, "checkout", "-q", "-b", "current")
	commitFile(t, s.Primary, "current.txt", "c\n")
	s.Base = "gone"

	res := ensure(t, s)
	if res.Action != ActionCreated || !res.BaseMissing {
		t.Fatalf("result = %+v, want created with BaseMissing", res)
	}
	if got, want := head(t, res.Dir, "HEAD"), head(t, s.Primary, "HEAD"); got != want {
		t.Errorf("worktree HEAD = %s, want primary HEAD %s", got, want)
	}
}

func TestCurrentBranchDetached(t *testing.T) {
	repo := initRepo(t)
	if b, err := CurrentBranch(context.Background(), repo); err != nil || b != "main" {
		t.Fatalf("CurrentBranch = %q, %v", b, err)
	}
	gitRun(t, repo, "checkout", "-q", "--detach")
	if _, err := CurrentBranch(context.Background(), repo); !errors.Is(err, ErrDetachedHEAD) {
		t.Fatalf("detached: err = %v, want ErrDetachedHEAD", err)
	}
	if _, err := CurrentBranch(context.Background(), t.TempDir()); err == nil || errors.Is(err, ErrDetachedHEAD) {
		t.Fatalf("non-repo: err = %v, want a plain error", err)
	}
}

func TestCountUncommitted(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	commitFile(t, repo, "mod.txt", "a\n")
	commitFile(t, repo, "del.txt", "a\n")
	commitFile(t, repo, "rev.txt", "a\n")
	commitFile(t, repo, "staged.txt", "a\n")
	commitFile(t, repo, "notmine.txt", "a\n")
	writeFile(t, filepath.Join(repo, ".gitignore"), "*.log\n")

	writeFile(t, filepath.Join(repo, "mod.txt"), "b\n")        // modified
	writeFile(t, filepath.Join(repo, "dir", "new.txt"), "n\n") // added, untracked
	_ = os.Remove(filepath.Join(repo, "del.txt"))              // deleted
	writeFile(t, filepath.Join(repo, "rev.txt"), "b\n")        // touched...
	writeFile(t, filepath.Join(repo, "rev.txt"), "a\n")        // ...and reverted
	writeFile(t, filepath.Join(repo, "staged.txt"), "b\n")     // staged
	gitRun(t, repo, "add", "staged.txt")                       //
	writeFile(t, filepath.Join(repo, "debug.log"), "x\n")      // ignored
	writeFile(t, filepath.Join(repo, "notmine.txt"), "b\n")    // someone else's edit
	writeFile(t, filepath.Join(repo, "gone-before.txt"), "")   // created...
	_ = os.Remove(filepath.Join(repo, "gone-before.txt"))      // ...and deleted again

	files := []string{"mod.txt", "dir/new.txt", "del.txt", "rev.txt", "staged.txt", "debug.log", "gone-before.txt"}
	n, err := CountUncommitted(ctx, repo, files)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 { // mod, new, del, staged
		t.Errorf("uncommitted = %d, want 4", n)
	}
	if n, err := CountUncommitted(ctx, repo, nil); err != nil || n != 0 {
		t.Errorf("no files: %d, %v", n, err)
	}
	// Paths with glob characters are literal, not patterns.
	if n, err := CountUncommitted(ctx, repo, []string{"*.txt"}); err != nil || n != 0 {
		t.Errorf("glob path: %d, %v; want 0", n, err)
	}
}

func TestCountUncommittedRename(t *testing.T) {
	repo := initRepo(t)
	commitFile(t, repo, "old.txt", "a\n")
	gitRun(t, repo, "mv", "old.txt", "new.txt")
	n, err := CountUncommitted(context.Background(), repo, []string{"old.txt", "new.txt"})
	if err != nil || n != 1 {
		t.Errorf("rename: %d, %v; want 1", n, err)
	}
}

func TestCountIsolated(t *testing.T) {
	ctx := context.Background()
	s := newSpec(t)
	res := ensure(t, s)
	commitFile(t, res.Dir, "one.txt", "1\n")
	commitFile(t, res.Dir, "two.txt", "2\n")
	writeFile(t, filepath.Join(res.Dir, "one.txt"), "changed\n")

	c, err := Count(ctx, CountTarget{Dir: res.Dir, Files: []string{"one.txt", "two.txt"}, Branch: res.Branch, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if c != (Counts{Uncommitted: 1, Unlanded: 2}) {
		t.Errorf("counts = %+v, want 1 uncommitted, 2 unlanded", c)
	}

	// Base moved ahead independently: still only the session's commits.
	commitFile(t, s.Primary, "trunk.txt", "t\n")
	if n, err := CountUnlanded(ctx, res.Dir, "main", res.Branch); err != nil || n != 2 {
		t.Errorf("after trunk moved: %d, %v; want 2", n, err)
	}
	// Base deleted: counts against the primary's HEAD.
	gitRun(t, s.Primary, "checkout", "-q", "-b", "other")
	gitRun(t, s.Primary, "branch", "-D", "main")
	if n, err := CountUnlanded(ctx, res.Dir, "main", res.Branch); err != nil || n != 2 {
		t.Errorf("base deleted: %d, %v; want 2", n, err)
	}
}

// Working in place: the folder is the user's checkout, and nothing is
// "unlanded" because there is no session branch.
func TestCountInPlace(t *testing.T) {
	repo := initRepo(t)
	commitFile(t, repo, "a.txt", "a\n")
	commitFile(t, repo, "local.txt", "l\n") // a commit the session made in place
	writeFile(t, filepath.Join(repo, "a.txt"), "b\n")
	c, err := Count(context.Background(), CountTarget{Dir: repo, Files: []string{"a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if c != (Counts{Uncommitted: 1}) {
		t.Errorf("counts = %+v, want 1 uncommitted, 0 unlanded", c)
	}
}

func TestSessionFiles(t *testing.T) {
	root := t.TempDir()
	rows := []contracts.FileChange{
		{Repo: root, Path: "b.txt"},
		{Repo: filepath.Join(root, "sub"), Path: "c.txt"},
		{Repo: root, Path: filepath.Join(root, "a.txt")},
		{Repo: root, Path: "b.txt"},                     // duplicate touch
		{Repo: root, Path: "task.txt", TaskID: "tsk_1"}, // a pipeline task's row
		{Repo: root, Path: "../outside.txt"},            // escapes the folder
		{Repo: "/somewhere/else", Path: "x.txt"},        // another repo
		{Path: "rel-no-repo.txt"},
		{Repo: root, Path: "someone-elses.txt", Origin: contracts.OriginObserved},
		{Repo: root, Path: "observed-in-worktree.txt", Origin: contracts.OriginObserved, Attributed: true}, // resolved against root
	}
	got := strings.Join(SessionFiles(root, rows), ",")
	if want := "a.txt,b.txt,observed-in-worktree.txt,rel-no-repo.txt,sub/c.txt"; got != want {
		t.Errorf("files = %s, want %s", got, want)
	}
}

func TestParsePorcelainZ(t *testing.T) {
	out := " M a.txt\x00R  new.txt\x00old.txt\x00?? dir/n.txt\x00 D gone.txt\x00"
	got := strings.Join(parsePorcelainZ(out), ",")
	if want := "a.txt,new.txt,dir/n.txt,gone.txt"; got != want {
		t.Errorf("paths = %s, want %s", got, want)
	}
}

// Two holders of the same repo lock serialize — on separate file
// descriptions, which is also what two processes have.
func TestLockSerializes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repo lock is a no-op on non-unix hosts")
	}
	ctx := context.Background()
	s := newSpec(t)
	res := ensure(t, s)

	// The primary and its linked worktree name the same lock.
	p1, err := LockPath(ctx, s.Primary)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := LockPath(ctx, res.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(p1, p2) || filepath.Base(p1) != "mads-land.lock" {
		t.Fatalf("lock paths %s vs %s", p1, p2)
	}

	var mu sync.Mutex
	var order []string
	holding := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = WithLock(ctx, s.Primary, func() error {
			close(holding)
			time.Sleep(200 * time.Millisecond)
			mu.Lock()
			order = append(order, "first")
			mu.Unlock()
			return nil
		})
	}()
	<-holding
	if err := WithLock(ctx, res.Dir, func() error {
		mu.Lock()
		order = append(order, "second")
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-done
	if strings.Join(order, ",") != "first,second" {
		t.Errorf("order = %v, want first,second", order)
	}
}

// A waiter gives up when its context ends instead of hanging behind a
// long-running holder.
func TestLockWaitHonorsContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repo lock is a no-op on non-unix hosts")
	}
	path := filepath.Join(t.TempDir(), "mads-land.lock")
	release, err := AcquireFlock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := AcquireFlock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}

	if err := release(); err != nil {
		t.Fatal(err)
	}
	r2, err := AcquireFlock(context.Background(), path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = r2()
}
