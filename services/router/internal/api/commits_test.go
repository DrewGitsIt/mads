package api_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

type commitFileResp struct {
	Path   string `json:"path"`
	Change string `json:"change"`
}

type commitResp struct {
	contracts.Commit
	Files []commitFileResp `json:"files,omitempty"`
}

func gitCommitTestRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func getCommitsThroughRouter(t *testing.T, srv, sessionID string) []commitResp {
	t.Helper()
	resp, err := http.Get(srv + "/sessions/" + sessionID + "/commits") //nolint:gosec,noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /sessions/%s/commits status = %d, want 200", sessionID, resp.StatusCode)
	}
	return decodeJSON[[]commitResp](t, resp)
}

// TestSessionCommitsEnrichesWithFiles: a commit recorded with a real,
// reachable Repo gets its file list resolved by the Router via `git show`.
func TestSessionCommitsEnrichesWithFiles(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	ctx := context.Background()

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	gitCommitTestRun(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "add hello")
	hashOut, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(hashOut))

	if _, err := data.RecordCommit(ctx, &dataclient.CommitReport{
		Commit: contracts.Commit{SessionID: sess.ID, Hash: hash, Repo: repo, Message: "add hello"},
	}); err != nil {
		t.Fatal(err)
	}

	got := getCommitsThroughRouter(t, srv.URL, sess.ID)
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1", len(got))
	}
	if len(got[0].Files) != 1 || got[0].Files[0].Path != "hello.txt" || got[0].Files[0].Change != "added" {
		t.Errorf("files = %+v, want [{hello.txt added}]", got[0].Files)
	}
}

// TestSessionCommitsDegradesWithoutRepo: a commit with no Repo recorded
// (pre-existing data, or a hook that couldn't resolve one) still returns
// 200 with no files, not an error.
func TestSessionCommitsDegradesWithoutRepo(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	ctx := context.Background()

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := data.RecordCommit(ctx, &dataclient.CommitReport{
		Commit: contracts.Commit{SessionID: sess.ID, Hash: "abc1234", Message: "no repo"},
	}); err != nil {
		t.Fatal(err)
	}

	got := getCommitsThroughRouter(t, srv.URL, sess.ID)
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1", len(got))
	}
	if len(got[0].Files) != 0 {
		t.Errorf("files = %+v, want none", got[0].Files)
	}
}

// TestSessionCommitsDegradesWithUnreachableRepo: a Repo path that doesn't
// exist also degrades to no files, not an error.
func TestSessionCommitsDegradesWithUnreachableRepo(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	ctx := context.Background()

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := data.RecordCommit(ctx, &dataclient.CommitReport{
		Commit: contracts.Commit{SessionID: sess.ID, Hash: "abc1234", Repo: "/nonexistent/repo/path", Message: "unreachable"},
	}); err != nil {
		t.Fatal(err)
	}

	got := getCommitsThroughRouter(t, srv.URL, sess.ID)
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1", len(got))
	}
	if len(got[0].Files) != 0 {
		t.Errorf("files = %+v, want none", got[0].Files)
	}
}

// TestSessionCommitsTaskLinkageRoundTrips: task_id/linked_via resolved by
// the Data Service's trailer linkage round-trip through the Router's new
// proxy unchanged.
func TestSessionCommitsTaskLinkageRoundTrips(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	ctx := context.Background()

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	task := contracts.Task{SessionID: sess.ID, Title: "linked", Source: "user"}
	if err := data.CreateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	if _, err := data.RecordCommit(ctx, &dataclient.CommitReport{
		Commit:        contracts.Commit{SessionID: sess.ID, Hash: "abc1234", Message: "linked commit"},
		TrailerTaskID: task.ID,
	}); err != nil {
		t.Fatal(err)
	}

	got := getCommitsThroughRouter(t, srv.URL, sess.ID)
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1", len(got))
	}
	if got[0].TaskID != task.ID || got[0].LinkedVia != contracts.LinkedViaTrailer {
		t.Errorf("linkage: %+v", got[0])
	}
}
