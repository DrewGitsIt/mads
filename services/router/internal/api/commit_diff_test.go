package api_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

type fileDiffResp struct {
	Path     string `json:"path"`
	Original string `json:"original"`
	Modified string `json:"modified"`
}

func getCommitDiffThroughRouter(t *testing.T, srv, sessionID, hash, path string) *http.Response {
	t.Helper()
	u := srv + "/sessions/" + sessionID + "/commits/" + hash + "/diff?" + url.Values{"path": {path}}.Encode()
	resp, err := http.Get(u) //nolint:gosec,noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func gitRevParseHead(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// TestSessionCommitDiffAddedFile: a file added in a commit with no prior
// version in its parent returns an empty original and populated modified, 200.
func TestSessionCommitDiffAddedFile(t *testing.T) {
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
	hash := gitRevParseHead(t, repo)

	if _, err := data.RecordCommit(ctx, &dataclient.CommitReport{
		Commit: contracts.Commit{SessionID: sess.ID, Hash: hash, Repo: repo, Message: "add hello"},
	}); err != nil {
		t.Fatal(err)
	}

	resp := getCommitDiffThroughRouter(t, srv.URL, sess.ID, hash, "hello.txt")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "" || got.Modified != "hi\n" {
		t.Errorf("diff = %+v, want {Original:\"\" Modified:\"hi\\n\"}", got)
	}
}

// TestSessionCommitDiffModifiedFile: a file that existed in the parent and
// changed returns both sides populated and differing, 200.
func TestSessionCommitDiffModifiedFile(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("hi there\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "change hello")
	hash := gitRevParseHead(t, repo)

	if _, err := data.RecordCommit(ctx, &dataclient.CommitReport{
		Commit: contracts.Commit{SessionID: sess.ID, Hash: hash, Repo: repo, Message: "change hello"},
	}); err != nil {
		t.Fatal(err)
	}

	resp := getCommitDiffThroughRouter(t, srv.URL, sess.ID, hash, "hello.txt")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "hi\n" || got.Modified != "hi there\n" {
		t.Errorf("diff = %+v, want {Original:\"hi\\n\" Modified:\"hi there\\n\"}", got)
	}
}

// TestSessionCommitDiffUnknownCommit: a hash not linked to the session 404s, not 500.
func TestSessionCommitDiffUnknownCommit(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	ctx := context.Background()

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	resp := getCommitDiffThroughRouter(t, srv.URL, sess.ID, "abc1234", "hello.txt")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestSessionCommitDiffNoRepo: a commit recorded with no Repo 502s with an
// error body, not a silent 200.
func TestSessionCommitDiffNoRepo(t *testing.T) {
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

	resp := getCommitDiffThroughRouter(t, srv.URL, sess.ID, "abc1234", "hello.txt")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, resp)
	if body["error"] == "" {
		t.Error("expected non-empty error body")
	}
}

// TestSessionCommitDiffUnreachableRepo: a Repo path that doesn't exist 502s, not 500.
func TestSessionCommitDiffUnreachableRepo(t *testing.T) {
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

	resp := getCommitDiffThroughRouter(t, srv.URL, sess.ID, "abc1234", "hello.txt")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

// TestSessionCommitDiffMissingPath: missing path query param 400s.
func TestSessionCommitDiffMissingPath(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	ctx := context.Background()

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/sessions/" + sess.ID + "/commits/abc1234/diff") //nolint:gosec,noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
