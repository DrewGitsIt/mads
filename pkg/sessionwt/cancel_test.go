package sessionwt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCancelledRequestStopsGit: a handler that runs git under r.Context()
// (as every Router git route does) returns soon after its client goes
// away, its git command killed, rather than running the command out.
func TestCancelledRequestStopsGit(t *testing.T) {
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q")
	const slow = 20 * time.Second
	returned := make(chan time.Duration, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// A git that takes `slow` to answer: an alias running a shell command.
		_, err := Git(r.Context(), dir, "-c", "alias.slow=!sleep 20", "slow")
		if err == nil {
			t.Error("git answered despite the cancelled request")
		}
		returned <- time.Since(start)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("request answered, want it cancelled")
	}
	select {
	case took := <-returned:
		if took > gitWaitDelay+3*time.Second {
			t.Fatalf("git returned after %v, want soon after the cancel", took)
		}
	case <-time.After(slow):
		t.Fatal("handler's git ran to completion after its request was cancelled")
	}
}

// TestGitSucceedsWhenAHookKeepsThePipeOpen: a hook that backgrounds a job
// holding stdout makes the wait outlast gitWaitDelay after git exited 0.
// That is a success, not exec.ErrWaitDelay.
func TestGitSucceedsWhenAHookKeepsThePipeOpen(t *testing.T) {
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q")
	hooks := filepath.Join(dir, ".git", "hooks")
	hook := "#!/bin/sh\n(sleep 6; echo late) &\nexit 0\n"
	if err := os.WriteFile(filepath.Join(hooks, "post-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := Git(context.Background(), dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x")
	if err != nil {
		t.Fatalf("commit with a backgrounding hook: %v", err)
	}
	if took := time.Since(start); took > gitWaitDelay+3*time.Second {
		t.Errorf("took %v, want about gitWaitDelay", took)
	}
	if head := strings.TrimSpace(gitOut(t, dir, "rev-list", "--count", "HEAD")); head != "1" {
		t.Errorf("commits = %s, want 1", head)
	}
}

// TestNetGitOutlivesItsCaller: a fetch or push writes refs, so a caller
// that went away (a closed Push dialog) mustn't kill it half-way and
// leave a .lock behind; timeout alone bounds it.
func TestNetGitOutlivesItsCaller(t *testing.T) {
	root := t.TempDir()
	bare, repo := filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	gitOut(t, root, "init", "-q", "--bare", "-b", "main", bare)
	gitOut(t, root, "init", "-q", "-b", "main", repo)
	gitOut(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "x")
	gitOut(t, repo, "remote", "add", "origin", bare)
	gitOut(t, repo, "push", "-q", "origin", "main")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := netGit(ctx, repo, time.Minute, "fetch", "--quiet", "origin", "main"); err != nil {
		t.Fatalf("fetch under a cancelled caller: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "refs", "remotes", "origin", "main.lock")); err == nil {
		t.Error("a ref lock was left behind")
	}
}
