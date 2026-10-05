package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// newWorktreeServer is newTestServer with its Data Service client handed
// back, so a test can write session records no child ever started.
func newWorktreeServer(t *testing.T) (*httptest.Server, *dataclient.Client) {
	t.Helper()
	data := dataclient.New(datatest.Start(t), "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	srv := httptest.NewServer(api.New(testOptions(t, data, sup)))
	t.Cleanup(srv.Close)
	return srv, data
}

func wtGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// wtRepo is a repo on main with one commit and a repo-local identity (CI
// has no global one, and Commit and Land use the user's).
func wtRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wtGit(t, dir, "init", "-q", "-b", "main", ".")
	wtGit(t, dir, "config", "user.email", "t@t")
	wtGit(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtGit(t, dir, "add", ".")
	wtGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// isolatedRecord creates a session record isolated the way its child would
// have isolated it at first start, with no child running.
func isolatedRecord(t *testing.T, data *dataclient.Client, name string) (contracts.Session, string) {
	t.Helper()
	ctx := context.Background()
	repo := wtRepo(t)
	sess := contracts.Session{Name: name, Provider: "fake", Dir: repo, Isolation: contracts.IsolationWorktree}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	res, err := sessionwt.EnsureWithLock(ctx, sessionwt.Spec{Primary: repo, Root: t.TempDir(), SessionID: sess.ID, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	base := "main"
	if err := data.SetSessionWorktree(ctx, sess.ID, contracts.SessionWorktreePatch{
		WorktreeDir: &res.Dir, WorktreeBranch: &res.Branch, WorktreeBase: &base,
	}); err != nil {
		t.Fatal(err)
	}
	sess, err = data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return sess, repo
}

// agentWrote puts a file in the session's folder and on its change list,
// as the agent's edit tool would.
func agentWrote(t *testing.T, data *dataclient.Client, sess contracts.Session, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(sess.WorktreeDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := data.RecordFileChange(context.Background(), &contracts.FileChange{
		SessionID: sess.ID, Repo: sess.WorktreeDir, Path: name, Change: "added", Origin: contracts.OriginToolUse,
	}); err != nil {
		t.Fatal(err)
	}
}

// A live child answers the worktree routes itself: the Router forwards the
// body as sent and adds only the pipeline gate it alone can see.
func TestWorktreeRoutesForwardToLiveChild(t *testing.T) {
	srv := newTestServer(t)
	sess := mkSessionRouter(t, srv.URL, "live")

	type marker struct{ Via, Route, Query, Body string }
	resp, err := http.Get(srv.URL + "/sessions/" + sess.ID + "/worktree")
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeJSON[marker](t, resp); got.Via != "child" || got.Route != "GET /worktree" {
		t.Errorf("status = %+v, want the child's", got)
	}
	got := decodeJSON[marker](t, postJSON(t, srv.URL+"/sessions/"+sess.ID+"/commit",
		contracts.CommitRequest{Paths: []string{"a.txt"}, Message: "Add a"}))
	if got.Via != "child" || got.Route != "POST /commit" || !strings.Contains(got.Body, `"message":"Add a"`) {
		t.Errorf("commit = %+v, want forwarded with its body", got)
	}
	got = decodeJSON[marker](t, postJSON(t, srv.URL+"/sessions/"+sess.ID+"/commit/undo", contracts.UndoCommitRequest{Hash: "abc"}))
	if got.Route != "POST /commit/undo" || !strings.Contains(got.Body, `"abc"`) {
		t.Errorf("undo = %+v", got)
	}
	got = decodeJSON[marker](t, postJSON(t, srv.URL+"/sessions/"+sess.ID+"/land", struct{}{}))
	if got.Via != "child" || got.Route != "POST /land" || got.Query != "" {
		t.Errorf("land = %+v, want forwarded with no pipeline gate", got)
	}
}

// With no child, the Router runs Commit, Undo and Land itself against the
// session's real folder and records what it made.
func TestWorktreeRoutesRunDirectlyWhenInactive(t *testing.T) {
	srv, data := newWorktreeServer(t)
	sess, repo := isolatedRecord(t, data, "Inactive work")
	agentWrote(t, data, sess, "feature.txt", "f\n")
	url := srv.URL + "/sessions/" + sess.ID

	resp, err := http.Get(url + "/worktree")
	if err != nil {
		t.Fatal(err)
	}
	st := decodeJSON[contracts.WorktreeStatus](t, resp)
	if !st.Isolated || len(st.Files) != 1 || st.Files[0] != (contracts.WorktreeFile{Path: "feature.txt", Kind: "added"}) ||
		st.LandBlocked == nil || st.LandBlocked.Reason != contracts.LandDirty {
		t.Fatalf("status = %+v", st)
	}

	resp = postJSON(t, url+"/commit", contracts.CommitRequest{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("commit with no message, auto off: status %d, want 400", resp.StatusCode)
	}
	if body := decodeJSON[contracts.CommitError](t, resp); body.Code != contracts.CommitMessageRequired {
		t.Errorf("refusal = %+v", body)
	}

	resp = postJSON(t, url+"/commit", contracts.CommitRequest{Message: "Add feature"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("commit: status %d", resp.StatusCode)
	}
	first := decodeJSON[contracts.CommitResult](t, resp)
	resp = postJSON(t, url+"/commit/undo", contracts.UndoCommitRequest{Hash: first.Hash})
	if resp.StatusCode != http.StatusOK || !decodeJSON[contracts.UndoCommitResult](t, resp).Undone {
		t.Fatalf("undo: status %d", resp.StatusCode)
	}
	commits, err := data.Commits(context.Background(), sess.ID)
	if err != nil || len(commits) != 0 {
		t.Fatalf("commit records after undo = %+v, %v; want none", commits, err)
	}

	// "Write commit messages for me": an empty message is drafted.
	cfg, err := data.Config(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.AutoCommitMessages = true
	if resp := putJSON(t, srv.URL+"/config", cfg); resp.StatusCode != http.StatusOK {
		t.Fatalf("set auto commit messages: status %d", resp.StatusCode)
	}
	resp = postJSON(t, url+"/commit", contracts.CommitRequest{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("one-click commit: status %d", resp.StatusCode)
	}
	auto := decodeJSON[contracts.CommitResult](t, resp)
	if !auto.Drafted || !auto.Undoable || !strings.HasSuffix(auto.Message, "Session: "+sess.ID) {
		t.Errorf("one-click commit = %+v", auto)
	}

	resp = postJSON(t, url+"/land", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("land: status %d", resp.StatusCode)
	}
	landed := decodeJSON[contracts.LandResult](t, resp)
	if landed.Outcome != contracts.LandLanded || landed.MergeCommit != wtGit(t, repo, "rev-parse", "HEAD") {
		t.Fatalf("land = %+v", landed)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Error("work didn't land in the checkout")
	}
	rec, err := data.Session(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.UncommittedCount != 0 || rec.UnlandedCount != 0 {
		t.Errorf("counts after land = %d/%d, want 0/0", rec.UncommittedCount, rec.UnlandedCount)
	}
	commits, err = data.Commits(context.Background(), sess.ID)
	if err != nil || len(commits) != 2 || commits[1].Hash != landed.MergeCommit {
		t.Errorf("commit records = %+v, %v; want the commit and the land", commits, err)
	}
}

// Drafting refuses paths the session didn't change, like Commit.
func TestCommitDraftValidatesPaths(t *testing.T) {
	srv, data := newWorktreeServer(t)
	sess, _ := isolatedRecord(t, data, "draft")
	agentWrote(t, data, sess, "a.txt", "a\n")

	resp := postJSON(t, srv.URL+"/sessions/"+sess.ID+"/commit/draft", contracts.CommitDraftRequest{Paths: []string{"../../etc/passwd"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("draft of an outside path: status %d, want 400", resp.StatusCode)
	}
	resp = postJSON(t, srv.URL+"/sessions/"+sess.ID+"/commit/draft", contracts.CommitDraftRequest{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("draft: status %d", resp.StatusCode)
	}
	if d := decodeJSON[contracts.CommitDraft](t, resp); d.Message == "" {
		t.Error("empty draft")
	}
}

// Deleting an isolated session removes its folder and branch; the user's
// checkout stays.
func TestDeleteSessionRemovesWorktree(t *testing.T) {
	srv, data := newWorktreeServer(t)
	sess, repo := isolatedRecord(t, data, "doomed")
	agentWrote(t, data, sess, "wip.txt", "unsaved\n")

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/sessions/"+sess.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}
	if _, err := os.Stat(sess.WorktreeDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("worktree folder still there: %v", err)
	}
	if out := wtGit(t, repo, "branch", "--list", sess.WorktreeBranch); out != "" {
		t.Errorf("branch still there: %q", out)
	}
	if _, err := os.Stat(filepath.Join(repo, "README.md")); err != nil {
		t.Error("delete touched the user's checkout")
	}
}

// deleteSession deletes a session through the Router, wanting 204.
func deleteSession(t *testing.T, url, id string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url+"/sessions/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}
}

// A linked session that joined the folder still works in it: status names
// it, and delete keeps the folder and branch.
func TestDeleteSessionKeepsFolderAJoinedSessionUses(t *testing.T) {
	srv, data := newWorktreeServer(t)
	owner, repo := isolatedRecord(t, data, "owner")
	joiner := contracts.Session{Name: "joiner", Provider: "fake", Dir: owner.WorktreeDir, Isolation: contracts.IsolationCurrentDir}
	if err := data.CreateSession(context.Background(), &joiner); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/sessions/" + owner.ID + "/worktree")
	if err != nil {
		t.Fatal(err)
	}
	st := decodeJSON[contracts.WorktreeStatus](t, resp)
	if len(st.SharedWith) != 1 || st.SharedWith[0].ID != joiner.ID || st.SharedWith[0].Name != "joiner" || st.PipelineRunning {
		t.Errorf("status shared_with = %+v, pipeline %v", st.SharedWith, st.PipelineRunning)
	}

	deleteSession(t, srv.URL, owner.ID)
	if _, err := os.Stat(filepath.Join(owner.WorktreeDir, "README.md")); err != nil {
		t.Errorf("folder removed under the joined session: %v", err)
	}
	if out := wtGit(t, repo, "branch", "--list", owner.WorktreeBranch); out == "" {
		t.Error("branch deleted under the joined session")
	}
}

// A pipeline run for the session merges into its folder: status says so,
// and delete keeps the folder and branch.
func TestDeleteSessionKeepsFolderARunningPipelineUses(t *testing.T) {
	srv, data := newWorktreeServer(t)
	ctx := context.Background()
	repo := wtRepo(t)
	ws := &contracts.Workspace{Name: "ws", Repos: []contracts.WorkspaceRepo{{Path: repo, Primary: true}}}
	if err := data.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	owner := contracts.Session{Name: "owner", Provider: "fake", Workspace: ws.ID, Dir: repo, Isolation: contracts.IsolationWorktree}
	if err := data.CreateSession(ctx, &owner); err != nil {
		t.Fatal(err)
	}
	res, err := sessionwt.EnsureWithLock(ctx, sessionwt.Spec{Primary: repo, Root: t.TempDir(), SessionID: owner.ID, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetSessionWorktree(ctx, owner.ID, contracts.SessionWorktreePatch{WorktreeDir: &res.Dir, WorktreeBranch: &res.Branch}); err != nil {
		t.Fatal(err)
	}
	owner.WorktreeDir, owner.WorktreeBranch = res.Dir, res.Branch
	task := &contracts.Task{SessionID: owner.ID, Title: "t1", Status: contracts.StatusReady, Source: "user"}
	if err := data.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	tpl := &contracts.Template{Name: "nightly", Source: "name: nightly\nstages:\n  - name: verify\n    command: make test\n"}
	if err := data.CreateTemplate(context.Background(), tpl); err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, srv.URL+"/pipelines", map[string]string{"session_id": owner.ID, "template_id": tpl.ID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start pipeline: status %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp, err = http.Get(srv.URL + "/sessions/" + owner.ID + "/worktree")
	if err != nil {
		t.Fatal(err)
	}
	if st := decodeJSON[contracts.WorktreeStatus](t, resp); !st.PipelineRunning || len(st.SharedWith) != 0 {
		t.Errorf("status pipeline_running = %v, shared_with %+v", st.PipelineRunning, st.SharedWith)
	}

	deleteSession(t, srv.URL, owner.ID)
	if _, err := os.Stat(filepath.Join(owner.WorktreeDir, "README.md")); err != nil {
		t.Errorf("folder removed under the running pipeline: %v", err)
	}
	if out := wtGit(t, repo, "branch", "--list", owner.WorktreeBranch); out == "" {
		t.Error("branch deleted under the running pipeline")
	}
}

// A Router-direct Land waits only briefly for a lock someone else holds
// (a pipeline merging into the folder, say), then refuses "locked"; and
// status, read-only, answers meanwhile rather than queue behind it.
func TestWorktreeRoutesDirectLandRefusesLocked(t *testing.T) {
	srv, data := newWorktreeServer(t)
	sess, repo := isolatedRecord(t, data, "Busy folder")
	agentWrote(t, data, sess, "feature.txt", "f\n")
	url := srv.URL + "/sessions/" + sess.ID
	if resp := postJSON(t, url+"/commit", contracts.CommitRequest{Message: "Feature"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("commit: status %d", resp.StatusCode)
	}
	release, err := sessionwt.AcquireSessionLock(context.Background(), repo, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	landed := make(chan contracts.LandResult, 1)
	go func() {
		resp := postJSON(t, url+"/land", struct{}{})
		landed <- decodeJSON[contracts.LandResult](t, resp)
	}()
	time.Sleep(200 * time.Millisecond) // Land is waiting on the lock, holding the wake lock
	start := time.Now()
	resp, err := http.Get(url + "/worktree")
	if err != nil {
		t.Fatal(err)
	}
	if st := decodeJSON[contracts.WorktreeStatus](t, resp); st.UnlandedCount != 1 || time.Since(start) > 3*time.Second {
		t.Errorf("status during a waiting Land = %+v after %s", st, time.Since(start))
	}
	select {
	case res := <-landed:
		if res.Outcome != contracts.LandRefused || res.Reason != contracts.LandLocked {
			t.Errorf("land = %+v, want refused locked", res)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Land waited on the lock without bound")
	}
}
