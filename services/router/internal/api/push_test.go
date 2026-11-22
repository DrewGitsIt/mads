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

	if ahead := sessionAhead(t, srv.URL, sess.ID); ahead != 1 {
		t.Errorf("to push = %d, want 1", ahead)
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
	if ahead := sessionAhead(t, srv.URL, sess.ID); ahead != 0 {
		t.Errorf("to push after the push = %d, want 0", ahead)
	}
}

// sessionAhead is what GET /sessions/{id}/repos says the session's only
// repo has to push.
func sessionAhead(t *testing.T, base, id string) int {
	t.Helper()
	repos := freshRepos(t, base, id)
	if len(repos) != 1 {
		t.Fatalf("repos = %+v, want one", repos)
	}
	return repos[0].Worktree.Ahead
}

// An isolated session's repo comes with its own branch's facts and, for
// the push after Land, its base in the user's checkout.
func TestSessionReposIsolated(t *testing.T) {
	srv, data := newWorktreeServer(t)
	sess, repo := isolatedRecord(t, data, "Isolated repos")
	bare := filepath.Join(t.TempDir(), "origin.git")
	wtGit(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	wtGit(t, repo, "remote", "add", "origin", bare)
	wtGit(t, repo, "push", "-q", "-u", "origin", "main")
	if err := os.WriteFile(filepath.Join(repo, "local.txt"), []byte("l\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtGit(t, repo, "add", "local.txt")
	wtGit(t, repo, "commit", "-q", "-m", "local")

	repos := decodeJSON[[]contracts.SessionRepoState](t, settledGet(t, srv.URL+"/sessions/"+sess.ID+"/repos"))
	if len(repos) != 1 {
		t.Fatalf("repos = %+v", repos)
	}
	r := repos[0]
	if !r.Primary || !r.Isolated || r.Base != "main" || r.Worktree.Branch != sess.WorktreeBranch || r.Worktree.Main {
		t.Errorf("repo = %+v", r)
	}
	if r.BaseWorktree == nil || r.BaseWorktree.Branch != "main" || !r.BaseWorktree.Main || r.BaseWorktree.Ahead != 1 {
		t.Errorf("base = %+v, want main in the user's checkout, 1 to push", r.BaseWorktree)
	}
}

// Landing a worktree the agent made: the preview infers the base, the
// draft writes a message, and the Land squashes onto main.
func TestLandLinkedWorktreeRoutes(t *testing.T) {
	srv, data := newWorktreeServer(t)
	ctx := context.Background()
	repo := wtRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	wtGit(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	wt = wtGit(t, wt, "rev-parse", "--show-toplevel")
	sess := contracts.Session{Name: "Agent worktree", Provider: "fake", Dir: repo, Isolation: contracts.IsolationCurrentDir}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(wt, "feature.txt")
	if err := os.WriteFile(file, []byte("f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtGit(t, wt, "add", "feature.txt")
	wtGit(t, wt, "commit", "-q", "-m", "Add feature")
	if err := data.RecordFileChange(ctx, &contracts.FileChange{SessionID: sess.ID, Path: file, Change: "added", Origin: contracts.OriginToolUse}); err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/sessions/" + sess.ID

	resp, err := http.Get(base + "/land?" + url.Values{"repo": {wt}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	p := decodeJSON[contracts.LandPreview](t, resp)
	if p.Blocked != nil || p.Branch != "feature" || p.Base != "main" || p.ToLand != 1 {
		t.Fatalf("preview = %+v", p)
	}
	resp = postJSON(t, base+"/land/draft", contracts.LandDraftRequest{Repo: wt})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("draft: status %d", resp.StatusCode)
	}
	if d := decodeJSON[contracts.CommitDraft](t, resp); d.Message == "" {
		t.Error("empty draft")
	}
	resp = postJSON(t, base+"/land", contracts.LandRequest{Repo: wt, Message: "Add the feature"})
	res := decodeJSON[contracts.LandResult](t, resp)
	if res.Outcome != contracts.LandLanded || wtGit(t, repo, "rev-parse", "main") != res.MergeCommit {
		t.Fatalf("land = %+v", res)
	}
	if subject := wtGit(t, repo, "log", "-1", "--format=%s", "main"); subject != "Add the feature" {
		t.Errorf("main's subject = %q", subject)
	}
}
