package api_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// The commit plan for a session started in a plain folder (as one started
// in ~/repo by accident) lists each repo its tools changed files in, with
// the session's own files marked and everyone else's named where the
// ledger can, plus the files it changed outside every repo.
func TestCommitPlanAcrossRepos(t *testing.T) {
	srv, data := newWorktreeServer(t)
	ctx := context.Background()
	parent := t.TempDir()
	a, b := filepath.Join(parent, "a"), filepath.Join(parent, "b")
	for _, dir := range []string{a, b} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		wtGit(t, dir, "init", "-q", "-b", "main", ".")
		wtGit(t, dir, "config", "user.email", "t@t")
		wtGit(t, dir, "config", "user.name", "t")
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		wtGit(t, dir, "add", ".")
		wtGit(t, dir, "commit", "-q", "-m", "init")
	}
	a, b = wtGit(t, a, "rev-parse", "--show-toplevel"), wtGit(t, b, "rev-parse", "--show-toplevel")

	sess := contracts.Session{Name: "Accidental", Provider: "fake", Dir: parent, Isolation: contracts.IsolationCurrentDir}
	other := contracts.Session{Name: "Neighbour", Provider: "fake", Dir: b, Isolation: contracts.IsolationCurrentDir}
	for _, s := range []*contracts.Session{&sess, &other} {
		if err := data.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	wrote := func(who contracts.Session, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := data.RecordFileChange(ctx, &contracts.FileChange{
			SessionID: who.ID, Path: path, Change: "added", Origin: contracts.OriginToolUse,
		}); err != nil {
			t.Fatal(err)
		}
	}
	wrote(sess, filepath.Join(a, "mine-a.txt"))
	wrote(sess, filepath.Join(b, "mine-b.txt"))
	wrote(other, filepath.Join(b, "theirs.txt"))
	if err := os.WriteFile(filepath.Join(a, "stray.txt"), []byte("?\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrote(sess, filepath.Join(parent, "notes.md"))

	plan := getPlan(t, srv.URL, sess.ID)
	if len(plan.Repos) != 2 || plan.Repos[0].Root != a || plan.Repos[1].Root != b {
		t.Fatalf("repos = %+v, want a then b", plan.Repos)
	}
	byPath := func(r contracts.CommitPlanRepo) map[string]contracts.CommitPlanFile {
		m := map[string]contracts.CommitPlanFile{}
		for _, f := range r.Files {
			m[f.Path] = f
		}
		return m
	}
	ra, rb := byPath(plan.Repos[0]), byPath(plan.Repos[1])
	if !ra["mine-a.txt"].Mine || ra["stray.txt"].Mine || ra["stray.txt"].By != nil || len(ra) != 2 {
		t.Errorf("a = %+v, want mine-a.txt mine and stray.txt nobody's", ra)
	}
	if !rb["mine-b.txt"].Mine || rb["theirs.txt"].Mine || rb["theirs.txt"].By == nil ||
		rb["theirs.txt"].By.ID != other.ID || rb["theirs.txt"].By.Name != "Neighbour" {
		t.Errorf("b = %+v, want theirs.txt named as Neighbour's", rb)
	}
	if plan.Repos[0].Branch != "main" || plan.Repos[0].Primary || plan.Repos[0].Isolated {
		t.Errorf("a's header = %+v, want main, in place, not primary", plan.Repos[0])
	}
	if len(plan.Outside) != 1 || filepath.Base(plan.Outside[0].Path) != "notes.md" {
		t.Errorf("outside = %+v, want notes.md", plan.Outside)
	}

	// The dialog commits b's files, the neighbour's included, in b only.
	resp := postJSON(t, srv.URL+"/sessions/"+sess.ID+"/commit", contracts.CommitRequest{
		Repo: b, Paths: []string{"mine-b.txt", "theirs.txt"}, Message: "b work",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("commit in b: status %d", resp.StatusCode)
	}
	plan = getPlan(t, srv.URL, sess.ID)
	if len(plan.Repos) != 1 || plan.Repos[0].Root != a {
		t.Errorf("after committing b: repos = %+v, want only a", plan.Repos)
	}
	rec, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.UncommittedCount != 1 || len(rec.Repos) != 1 || rec.Repos[0].Root != a {
		t.Errorf("record counts = %d %+v, want 1 in a", rec.UncommittedCount, rec.Repos)
	}
}

func getPlan(t *testing.T, base, id string) contracts.CommitPlan {
	t.Helper()
	resp, err := http.Get(base + "/sessions/" + id + "/commit-plan")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("commit plan: status %d", resp.StatusCode)
	}
	return decodeJSON[contracts.CommitPlan](t, resp)
}
