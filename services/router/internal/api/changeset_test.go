package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// changeSetRepo makes a committed repo (canonical path) holding files and
// a session whose folder it is, with a baselined shadow store.
func changeSetRepo(t *testing.T, data *dataclient.Client, files map[string]string) (contracts.Session, string) {
	t.Helper()
	sess, store := shadowSession(t, data)
	repo := shadow.RealPath(t.TempDir())
	gitCommitTestRun(t, repo, "init", "-q", "-b", "main")
	for rel, content := range files {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCommitTestRun(t, repo, "add", "-A")
	gitCommitTestRun(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "seed")
	store.Baseline(repo)
	sess.Dir = repo
	if err := data.UpdateSession(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	return sess, repo
}

func getChangeSet(t *testing.T, srv, sessionID, vs string) *http.Response {
	t.Helper()
	u := srv + "/sessions/" + sessionID + "/changeset"
	if vs != "" {
		u += "?vs=" + url.QueryEscape(vs)
	}
	resp, err := http.Get(u) //nolint:gosec,noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionChangeSet(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, repo := changeSetRepo(t, data, map[string]string{"a.go": "package a\n"})

	mustWrite(t, filepath.Join(repo, "a.go"), "package a\n\nvar X = 1\n")
	mustWrite(t, filepath.Join(repo, "tmp_try.py"), "print(1)\n")
	recordChange(t, data, sess.ID, "", filepath.Join(repo, "a.go"), "modified")

	resp := getChangeSet(t, srv.URL, sess.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	set := decodeJSON[contracts.ChangeSet](t, resp)
	if set.Baseline != contracts.BaselineSessionStart || !set.Git || len(set.Files) != 2 {
		t.Fatalf("set = %+v", set)
	}
	a, tmp := set.Files[0], set.Files[1]
	if a.Display != "a.go" || a.Tier != contracts.TierSource || !a.Attributed || a.Added != 2 {
		t.Errorf("a.go = %+v", a)
	}
	if tmp.Display != "tmp_try.py" || tmp.Tier != contracts.TierScratch || tmp.Attributed {
		t.Errorf("tmp_try.py = %+v", tmp)
	}
	if len(set.Baselines) != 3 {
		t.Errorf("baselines = %+v", set.Baselines)
	}

	// No worktree base: base is unavailable → 400. Unknown ids → 400.
	for _, vs := range []string{"base", "bogus"} {
		if resp := getChangeSet(t, srv.URL, sess.ID, vs); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("vs=%s status = %d, want 400", vs, resp.StatusCode)
		}
	}
	if resp := getChangeSet(t, srv.URL, "nope", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown session status = %d, want 404", resp.StatusCode)
	}
}

// The working-diff gate admits a file the ledger never recorded when it's
// in the session's change set, and still refuses anything that isn't.
func TestSessionWorkingDiffChangeSetGate(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, repo := changeSetRepo(t, data, map[string]string{"a.txt": "a\n", "same.txt": "same\n"})

	unrecorded := filepath.Join(repo, "a.txt")
	mustWrite(t, unrecorded, "a2\n") // changed on disk, never in the ledger

	resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, unrecorded, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unrecorded changed file: status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "a\n" || got.Modified != "a2\n" {
		t.Errorf("diff = %+v", got)
	}

	for _, p := range []string{
		filepath.Join(repo, "same.txt"),     // in the repo, unchanged
		filepath.Join(repo, "..", "a.txt"),  // escape attempt
		filepath.Join(repo, ".git", "HEAD"), // inside the repo's git dir
		"/etc/hosts",
	} {
		if resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, p, ""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", p, resp.StatusCode)
		}
	}
	// task_id scoping stays ledger-only.
	if resp := getWorkingDiffThroughRouter(t, srv.URL, sess.ID, unrecorded, "tsk_x"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("task-scoped unrecorded: status = %d, want 404", resp.StatusCode)
	}
}

// vs=base diffs against the merge base with the worktree's base branch.
func TestSessionWorkingDiffVsBase(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, repo := changeSetRepo(t, data, map[string]string{"a.txt": "a\n"})

	wt := filepath.Join(shadow.RealPath(t.TempDir()), "wt")
	gitCommitTestRun(t, repo, "worktree", "add", "-q", "-b", "mads/session/x", wt)
	mustWrite(t, filepath.Join(wt, "a.txt"), "a\ncommitted\n")
	gitCommitTestRun(t, wt, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "-am", "work")
	mustWrite(t, filepath.Join(wt, "a.txt"), "a\ncommitted\nuncommitted\n")
	wtDir, wtBase := wt, "main"
	if err := data.SetSessionWorktree(context.Background(), sess.ID, contracts.SessionWorktreePatch{WorktreeDir: &wtDir, WorktreeBase: &wtBase}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(wt, "a.txt")
	q := url.Values{"path": {path}, "vs": {"base"}}
	resp, err := http.Get(srv.URL + "/sessions/" + sess.ID + "/changes?" + q.Encode()) //nolint:gosec,noctx // local test server
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[fileDiffResp](t, resp)
	if got.Original != "a\n" || got.Modified != "a\ncommitted\nuncommitted\n" {
		t.Errorf("diff = %+v, want the merge-base original", got)
	}

	q.Set("vs", "main")                                                               // same merge base as base → unavailable
	resp, err = http.Get(srv.URL + "/sessions/" + sess.ID + "/changes?" + q.Encode()) //nolint:gosec,noctx // local test server
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("vs=main status = %d, want 400", resp.StatusCode)
	}
}

func postDeleteScratch(t *testing.T, srv, sessionID string, paths []string) contracts.DeleteScratchResult {
	t.Helper()
	body, _ := json.Marshal(contracts.DeleteScratchRequest{Paths: paths})
	resp, err := http.Post(srv+"/sessions/"+sessionID+"/changeset/delete-scratch", "application/json", bytes.NewReader(body)) //nolint:gosec,noctx // local test server
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	return decodeJSON[contracts.DeleteScratchResult](t, resp)
}

func TestSessionDeleteScratch(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, repo := changeSetRepo(t, data, map[string]string{"keep.go": "package a\n", "tmp_old.go": "package a\n"})

	scratch := filepath.Join(repo, "tmp_x.go")
	mustWrite(t, scratch, "package a\n")
	recordChange(t, data, sess.ID, "", scratch, "added")
	mustWrite(t, filepath.Join(repo, "keep.go"), "package a\n// edited\n")
	mustWrite(t, filepath.Join(repo, "tmp_old.go"), "package a\n// edited\n") // pre-existing: not scratch
	link := filepath.Join(repo, "tmp_link.go")
	if err := os.Symlink(filepath.Join(repo, "keep.go"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dir := filepath.Join(repo, "tmp_dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "inside.txt"), "x\n")

	res := postDeleteScratch(t, srv.URL, sess.ID, []string{
		scratch, filepath.Join(repo, "keep.go"), filepath.Join(repo, "tmp_old.go"), link, dir, "/etc/hosts",
	})
	if len(res.Deleted) != 1 || res.Deleted[0] != scratch {
		t.Errorf("deleted = %v, want only %s", res.Deleted, scratch)
	}
	if len(res.Skipped) != 5 {
		t.Errorf("skipped = %+v, want 5", res.Skipped)
	}
	if _, err := os.Lstat(scratch); !errors.Is(err, os.ErrNotExist) {
		t.Error("scratch file not deleted")
	}
	for _, p := range []string{filepath.Join(repo, "keep.go"), filepath.Join(repo, "tmp_old.go"), link, dir, filepath.Join(dir, "inside.txt")} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}

	// The deletion is in the ledger, as an observed change.
	ledger, err := data.FileChanges(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := ledger[len(ledger)-1]
	if last.Path != scratch || last.Change != "deleted" || last.Origin != contracts.OriginObserved {
		t.Errorf("last ledger row = %+v, want an observed deletion of %s", last, scratch)
	}

	// And it's gone from the change set.
	set := decodeJSON[contracts.ChangeSet](t, getChangeSet(t, srv.URL, sess.ID, ""))
	for _, f := range set.Files {
		if f.Path == scratch {
			t.Error("deleted scratch file still in the change set")
		}
	}
}
