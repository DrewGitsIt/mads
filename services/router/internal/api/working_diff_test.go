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

// TestSessionWorkingDiffAfterSessionCommit: once the agent commits its own
// edit, HEAD catches up to disk — the diff must fall back to the session
// base (parent of the session's first recorded commit) instead of showing
// an empty change for a file the session demonstrably rewrote.
func TestSessionWorkingDiffAfterSessionCommit(t *testing.T) {
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
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "session edit")
	if _, err := data.RecordCommit(context.Background(), &dataclient.CommitReport{
		Commit: contracts.Commit{SessionID: sess.ID, Hash: gitHeadHash(t, repo), Repo: repo, Message: "session edit"},
	}); err != nil {
		t.Fatal(err)
	}

	resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, path, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "hi\n" || got.Modified != "hi there\n" {
		t.Errorf("diff = %+v, want {Original:\"hi\\n\" Modified:\"hi there\\n\"}", got)
	}
}

// TestSessionWorkingDiffSessionCreatedAndCommitted: a file the session both
// created and committed diffs from the base rev, where it didn't exist —
// empty original, full content as modified.
func TestSessionWorkingDiffSessionCreatedAndCommitted(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	gitCommitTestRun(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "seed")

	path := filepath.Join(repo, "new.txt")
	if err := os.WriteFile(path, []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordChange(t, data, sess.ID, "", path, "added")
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "session add")
	if _, err := data.RecordCommit(context.Background(), &dataclient.CommitReport{
		Commit: contracts.Commit{SessionID: sess.ID, Hash: gitHeadHash(t, repo), Repo: repo, Message: "session add"},
	}); err != nil {
		t.Fatal(err)
	}

	resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, path, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "" || got.Modified != "fresh\n" {
		t.Errorf("diff = %+v, want {Original:\"\" Modified:\"fresh\\n\"}", got)
	}
}

func gitHeadHash(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
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

// TestSessionWorkingDiffPrefersShadowBaseline: with a shadow store
// baselined at session start, "original" is the baseline content even
// after the user repo's HEAD has caught up to the session's edit AND the
// session recorded no commits — the case the sessionBaseRev heuristic
// cannot resolve (it falls back to HEAD, erasing the diff).
func TestSessionWorkingDiffPrefersShadowBaseline(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, store := shadowSession(t, data)

	repo := t.TempDir()
	gitCommitTestRun(t, repo, "init", "-q")
	path := filepath.Join(repo, "hello.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "seed")
	store.Baseline(repo)

	// The session edits and commits (no recorded session commit): HEAD now
	// matches disk, so the heuristic would produce an empty diff.
	if err := os.WriteFile(path, []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "edit")
	recordChange(t, data, sess.ID, "", path, "modified")

	resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, path, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "base\n" || got.Modified != "edited\n" {
		t.Errorf("diff = %+v, want {Original:\"base\\n\" Modified:\"edited\\n\"} from the shadow baseline", got)
	}
}

// TestSessionWorkingDiffNonGitRoot: a file outside any git repo has no
// shadow baseline (the store never bulk-scans a non-repo root), so "original"
// is the pre-image captured before the session's tool call changed it. A
// file the tool created has an empty original.
func TestSessionWorkingDiffNonGitRoot(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, store := shadowSession(t, data)

	dir := t.TempDir()
	path := filepath.Join(dir, "plain.txt")
	created := filepath.Join(dir, "created.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.Baseline(dir) // provisioning-time call: must be a no-op here
	store.Capture("toolu_edit", path)
	store.Capture("toolu_create", created)
	if err := os.WriteFile(path, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(created, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordChange(t, data, sess.ID, "", path, "modified")
	recordChange(t, data, sess.ID, "", created, "added")

	for _, tc := range []struct{ path, original, modified string }{
		{path, "before\n", "after\n"},
		{created, "", "new\n"},
	} {
		resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, tc.path, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tc.path, resp.StatusCode)
		}
		got := decodeJSON[fileDiffResp](t, resp)
		if got.Original != tc.original || got.Modified != tc.modified {
			t.Errorf("%s: diff = %+v, want original %q modified %q", tc.path, got, tc.original, tc.modified)
		}
	}
}

// TestSessionWorkingDiffShadowFallbackToHeuristic: a session whose record
// carries a shadow_dir but whose root has no usable baseline (still
// running, or failed) falls back to the pre-shadow behavior — HEAD (or the
// session-base rev) resolves the original.
func TestSessionWorkingDiffShadowFallbackToHeuristic(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, _ := shadowSession(t, data) // shadow_dir recorded, but nothing baselined

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
		t.Errorf("diff = %+v, want the HEAD-based fallback {Original:\"hi\\n\" Modified:\"hi there\\n\"}", got)
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

// TestSessionWorkingDiffThroughSymlinkedRoot: a ledger path that reaches the
// workspace through a symlink still finds the shadow baseline. Roots come
// from git, which always answers with the physical path, so a symlinked
// ledger path used to send filepath.Rel up and out of the root with ../..;
// the baseline could not contain that name, which read as "the session added
// this file" and served an empty original — a modified file rendered as
// wholly new content. macOS hit this on every path under /var or /tmp, but
// any symlinked workspace does (shadow.RealPath).
func TestSessionWorkingDiffThroughSymlinkedRoot(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, store := shadowSession(t, data)

	repo := t.TempDir()
	gitCommitTestRun(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "seed")
	store.Baseline(repo)

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Edit and commit, so the pre-shadow heuristic would read HEAD and show
	// nothing: only the baseline can answer, and only if the path resolves.
	if err := os.WriteFile(filepath.Join(repo, "hello.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "edit")

	symlinked := filepath.Join(link, "hello.txt")
	recordChange(t, data, sess.ID, "", symlinked, "modified")

	resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, symlinked, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "base\n" || got.Modified != "edited\n" {
		t.Errorf("diff = %+v, want {Original:\"base\\n\" Modified:\"edited\\n\"} through the symlink", got)
	}
}
