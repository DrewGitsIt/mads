package sessionwt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// multi is a session started in a plain folder holding two repos, as when
// it was started in ~/repo and worked in ~/repo/mads: the folder has no
// repo, so the session's repos are the ones its tools changed files in.
type multi struct {
	parent, a, b string
	store        *memStore
	ops          Ops
}

func newMulti(t *testing.T) multi {
	t.Helper()
	parent := t.TempDir()
	a, b := filepath.Join(parent, "a"), filepath.Join(parent, "b")
	for _, dir := range []string{a, b} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, dir, "init", "-q", "-b", "main", ".")
		gitRun(t, dir, "config", "user.email", "t@t")
		gitRun(t, dir, "config", "user.name", "t")
		commitFile(t, dir, "README.md", "init\n")
	}
	st := &memStore{sess: contracts.Session{ID: "ses_multi", Name: "Multi", Dir: parent}}
	return multi{parent: parent, a: a, b: b, store: st, ops: Ops{Store: st}}
}

// edit writes files in dir and records them as the session's own tool edits.
func (m multi) edit(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
		m.store.rows = append(m.store.rows, contracts.FileChange{
			SessionID: m.store.sess.ID, Path: filepath.Join(dir, name), Change: "modified",
			Origin: contracts.OriginToolUse, Attributed: true,
		})
	}
}

func TestRepoTargetsFromLedger(t *testing.T) {
	m := newMulti(t)
	m.edit(t, m.b, map[string]string{"b.txt": "b\n"})
	m.edit(t, m.a, map[string]string{"x/a.txt": "a\n"})
	writeFile(t, filepath.Join(m.parent, "notes.md"), "plain\n")
	m.store.rows = append(m.store.rows,
		contracts.FileChange{Path: filepath.Join(m.parent, "notes.md"), Origin: contracts.OriginToolUse, Attributed: true},
		// Neither a task's row nor someone else's observed change makes a
		// repo the session's.
		contracts.FileChange{Path: filepath.Join(m.b, "task.txt"), TaskID: "tsk_1"},
	)

	_, primary, all, _, err := m.ops.targets(context.Background(), m.store.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if primary != nil {
		t.Fatalf("primary = %+v, want none for a plain folder", primary)
	}
	if len(all) != 2 || !samePath(all[0].Folder, m.a) || !samePath(all[1].Folder, m.b) {
		t.Fatalf("repos = %+v, want a then b", all)
	}
	for _, tg := range all {
		if tg.Isolated() || tg.Folder != tg.Primary || tg.SessionID != "ses_multi" {
			t.Errorf("target %+v, want in place in its own repo", tg)
		}
	}
}

func TestCommitInEachRepo(t *testing.T) {
	m := newMulti(t)
	ctx := context.Background()
	m.edit(t, m.a, map[string]string{"a.txt": "a\n"})
	m.edit(t, m.b, map[string]string{"b.txt": "b\n"})
	writeFile(t, filepath.Join(m.b, "theirs.txt"), "someone else\n")

	// Two repos and no folder repo: "" names nothing.
	if _, err := m.ops.Commit(ctx, m.store.sess.ID, contracts.CommitRequest{Message: "x"}); refusal(t, err).Code != contracts.CommitNoFolder {
		t.Errorf("commit with no repo named: %v, want no_folder", err)
	}
	if _, err := m.ops.Commit(ctx, m.store.sess.ID, contracts.CommitRequest{Repo: m.parent, Message: "x"}); refusal(t, err).Code != contracts.CommitUnknownRepo {
		t.Errorf("commit in the plain folder: %v, want unknown_repo", err)
	}

	st, err := m.ops.Status(ctx, m.store.sess.ID, m.b, Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(st.Folder, m.b) || len(st.Files) != 1 || st.Files[0].Path != "b.txt" {
		t.Errorf("status in b = %+v, want only the session's b.txt", st)
	}

	// The session's file, plus one it didn't change that the user picked.
	res, err := m.ops.Commit(ctx, m.store.sess.ID, contracts.CommitRequest{
		Repo: m.b, Paths: []string{"b.txt", "theirs.txt"}, Message: "b work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Paths) != 2 {
		t.Errorf("committed %+v, want both", res.Paths)
	}
	if out, _ := Git(ctx, m.b, "status", "--porcelain"); out != "" {
		t.Errorf("b still dirty after commit: %q", out)
	}
	if out, _ := Git(ctx, m.a, "status", "--porcelain"); out == "" {
		t.Error("a's file was committed by b's commit")
	}
	if len(m.store.commits) != 1 || !samePath(m.store.commits[0].Repo, m.b) {
		t.Errorf("recorded commits = %+v, want one in b", m.store.commits)
	}

	// Counts span both repos; b has nothing left.
	sum, repos, err := m.ops.CountAll(ctx, m.store.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Uncommitted != 1 || len(repos) != 1 || !samePath(repos[0].Root, m.a) || repos[0].Primary {
		t.Errorf("counts = %+v %+v, want 1 uncommitted, in a", sum, repos)
	}
}

// One repo and no folder repo: Commit and the header need no repo named.
func TestSingleRepoNeedsNoName(t *testing.T) {
	m := newMulti(t)
	ctx := context.Background()
	m.edit(t, m.a, map[string]string{"a.txt": "a\n"})
	if _, err := m.ops.Commit(ctx, m.store.sess.ID, contracts.CommitRequest{Message: "a work"}); err != nil {
		t.Fatal(err)
	}
	if err := m.ops.Recount(ctx, m.store.sess.ID); err != nil {
		t.Fatal(err)
	}
	p := m.store.patches[len(m.store.patches)-1]
	if *p.UncommittedCount != 0 || p.Repos == nil || len(*p.Repos) != 0 {
		t.Errorf("after committing everything: %+v, want zero and no repos", p)
	}
	if _, err := m.ops.Status(ctx, m.store.sess.ID, filepath.Join(m.parent, "nope"), Gates{}); !errors.Is(err, ErrUnknownRepo) {
		t.Errorf("status for a repo outside the set: %v, want ErrUnknownRepo", err)
	}
}

// A session working in a repo also counts a sibling repo it changed, and
// marks its own folder's repo primary.
func TestCountsPrimaryAndSibling(t *testing.T) {
	m := newMulti(t)
	m.store.sess.Dir = m.a
	m.edit(t, m.a, map[string]string{"a1.txt": "1\n", "a2.txt": "2\n"})
	m.edit(t, m.b, map[string]string{"b.txt": "b\n"})
	sum, repos, err := m.ops.CountAll(context.Background(), m.store.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	roots := []string{}
	for _, r := range repos {
		roots = append(roots, resolve(r.Root))
	}
	if sum.Uncommitted != 3 || !slices.Equal(roots, []string{resolve(m.a), resolve(m.b)}) ||
		!repos[0].Primary || repos[1].Primary || repos[0].UncommittedCount != 2 {
		t.Errorf("counts = %+v %+v, want 3 across a (primary, 2) and b (1)", sum, repos)
	}
}
