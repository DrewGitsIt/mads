package api_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

func getWorkingDiffThroughRouter(t *testing.T, srv, sessionID, path, taskID string) *http.Response {
	t.Helper()
	q := url.Values{"path": {path}}
	if taskID != "" {
		q.Set("task_id", taskID)
	}
	u := srv + "/sessions/" + sessionID + "/changes?" + q.Encode()
	resp, err := http.Get(u) //nolint:gosec,noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func recordChange(t *testing.T, data *dataclient.Client, sessionID, taskID, path, kind string) {
	t.Helper()
	fc := &contracts.FileChange{
		SessionID: sessionID, TaskID: taskID, Path: path, Change: kind, Origin: contracts.OriginToolUse,
	}
	if err := data.RecordFileChange(context.Background(), fc); err != nil {
		t.Fatal(err)
	}
}

// TestSessionWorkingDiffModifiedFile: a committed file with uncommitted edits
// returns HEAD content as original and the on-disk content as modified.
func TestSessionWorkingDiffModifiedFile(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	gitCommitTestRun(t, repo, "init", "-q")
	path := filepath.Join(repo, "hello.txt")
	if err := os.WriteFile(path, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "add hello")
	if err := os.WriteFile(path, []byte("hi there\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordChange(t, data, sess.ID, "", path, "modified")

	resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, path, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "hi\n" || got.Modified != "hi there\n" {
		t.Errorf("diff = %+v, want {Original:\"hi\\n\" Modified:\"hi there\\n\"}", got)
	}
}

// TestSessionWorkingDiffAddedFile: an untracked new file has an empty
// original and populated modified — not an error.
func TestSessionWorkingDiffAddedFile(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	gitCommitTestRun(t, repo, "init", "-q")
	path := filepath.Join(repo, "new.txt")
	if err := os.WriteFile(path, []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordChange(t, data, sess.ID, "", path, "added")

	resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, path, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "" || got.Modified != "fresh\n" {
		t.Errorf("diff = %+v, want {Original:\"\" Modified:\"fresh\\n\"}", got)
	}
}

// TestSessionWorkingDiffDeletedFile: a committed file removed from disk
// returns HEAD content as original and an empty modified — not a 502.
func TestSessionWorkingDiffDeletedFile(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	gitCommitTestRun(t, repo, "init", "-q")
	path := filepath.Join(repo, "hello.txt")
	if err := os.WriteFile(path, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "add hello")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	recordChange(t, data, sess.ID, "", path, "deleted")

	resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, path, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "hi\n" || got.Modified != "" {
		t.Errorf("diff = %+v, want {Original:\"hi\\n\" Modified:\"\"}", got)
	}
}

// TestSessionWorkingDiffUnrecordedPath: a path outside the session's
// file-change ledger 404s — the ledger is the security gate, this must
// never read an arbitrary disk file.
func TestSessionWorkingDiffUnrecordedPath(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, "/etc/hostname", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestSessionWorkingDiffTaskScope: task_id scopes the ledger lookup — a
// change recorded under another task 404s, the matching task 200s.
func TestSessionWorkingDiffTaskScope(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(path, []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordChange(t, data, sess.ID, "tsk_a", path, "added")

	resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, path, "tsk_b")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong-task status = %d, want 404", resp.StatusCode)
	}
	resp = getWorkingDiffThroughRouter(t, srv.URL, sess.ID, path, "tsk_a")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("matching-task status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "" || got.Modified != "data\n" {
		t.Errorf("diff = %+v, want {Original:\"\" Modified:\"data\\n\"} (non-git dir has no HEAD side)", got)
	}
}

// TestSessionWorkingDiffMissingPath: missing path query param 400s.
func TestSessionWorkingDiffMissingPath(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/sessions/" + sess.ID + "/changes") //nolint:gosec,noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
