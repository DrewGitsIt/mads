package api_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Push from a session working in place: the header counts the commit, the
// preview lists it, the push publishes it; and while another session's
// agent works in the same checkout, a rebase is refused.
func TestPushRoutes(t *testing.T) {
	srv, data := newWorktreeServer(t)
	ctx := context.Background()
	repo := wtRepo(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	wtGit(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	wtGit(t, repo, "remote", "add", "origin", bare)
	wtGit(t, repo, "push", "-q", "-u", "origin", "main")
	other := filepath.Join(t.TempDir(), "other")
	wtGit(t, filepath.Dir(other), "clone", "-q", bare, other)

	sess := contracts.Session{Name: "Pusher", Provider: "fake", Dir: repo, Isolation: contracts.IsolationCurrentDir}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtGit(t, repo, "add", "a.txt")
	wtGit(t, repo, "commit", "-q", "-m", "Add a")
	base := srv.URL + "/sessions/" + sess.ID

	if resp := postJSON(t, base+"/worktree/refresh", struct{}{}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("refresh: %d", resp.StatusCode)
	}
	if rec, _ := data.Session(ctx, sess.ID); rec.UnpushedCount != 1 {
		t.Errorf("unpushed_count = %d, want 1", rec.UnpushedCount)
	}

	preview := func() contracts.PushPreview {
		t.Helper()
		resp, err := http.Get(base + "/push?" + url.Values{"target": {contracts.PushBranch}}.Encode())
		if err != nil {
			t.Fatal(err)
		}
		return decodeJSON[contracts.PushPreview](t, resp)
	}

	// Someone else pushed, and another session's agent is working here.
	if err := os.WriteFile(filepath.Join(other, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtGit(t, other, "add", "b.txt")
	wtGit(t, other, "-c", "user.email=o@o", "-c", "user.name=o", "commit", "-q", "-m", "Add b")
	wtGit(t, other, "push", "-q", "origin", "main")
	busy := contracts.Session{Name: "Neighbour", Provider: "fake", Dir: repo, Isolation: contracts.IsolationCurrentDir}
	if err := data.CreateSession(ctx, &busy); err != nil {
		t.Fatal(err)
	}
	if err := data.SetSessionAgentState(ctx, busy.ID, contracts.AgentState{State: contracts.AgentWorking}); err != nil {
		t.Fatal(err)
	}
	p := preview()
	if p.Ahead != 1 || p.Behind != 1 || p.RebaseBlocked == nil || p.RebaseBlocked.Reason != contracts.PushBusy {
		t.Fatalf("preview with a busy neighbour = %+v", p)
	}
	resp := postJSON(t, base+"/push", contracts.PushRequest{Rebase: true, Expect: p.Head})
	if res := decodeJSON[contracts.PushResult](t, resp); res.Outcome != contracts.PushRefused || res.Reason != contracts.PushBusy {
		t.Fatalf("push with rebase while busy = %+v", res)
	}

	if err := data.SetSessionAgentState(ctx, busy.ID, contracts.AgentState{State: contracts.AgentIdle}); err != nil {
		t.Fatal(err)
	}
	resp = postJSON(t, base+"/push", contracts.PushRequest{Rebase: true, Expect: preview().Head})
	res := decodeJSON[contracts.PushResult](t, resp)
	if res.Outcome != contracts.PushPushed || !res.Rebased {
		t.Fatalf("push with rebase = %+v", res)
	}
	if wtGit(t, bare, "rev-parse", "main") != wtGit(t, repo, "rev-parse", "HEAD") {
		t.Error("origin/main isn't the pushed commit")
	}
	if rec, _ := data.Session(ctx, sess.ID); rec.UnpushedCount != 0 {
		t.Errorf("unpushed_count after the push = %d, want 0", rec.UnpushedCount)
	}
}
