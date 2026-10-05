package sessionwt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// memStore is an in-memory Store: one session record, its change list, and
// everything the operations wrote.
type memStore struct {
	mu      sync.Mutex
	sess    contracts.Session
	rows    []contracts.FileChange
	commits []contracts.Commit
	deleted []string
	patches []contracts.SessionWorktreePatch
	events  []contracts.Envelope
}

func (m *memStore) Session(context.Context, string) (contracts.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sess, nil
}

func (m *memStore) FileChanges(context.Context, string) ([]contracts.FileChange, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.rows), nil
}

func (m *memStore) RecordCommit(_ context.Context, rep *dataclient.CommitReport) (contracts.Commit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commits = append(m.commits, rep.Commit)
	return rep.Commit, nil
}

func (m *memStore) DeleteCommit(_ context.Context, _, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, hash)
	return nil
}

func (m *memStore) SetSessionWorktree(_ context.Context, _ string, p contracts.SessionWorktreePatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.patches = append(m.patches, p)
	return nil
}

func (m *memStore) Append(_ context.Context, env *contracts.Envelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, *env)
	return nil
}

func (m *memStore) touch(folder string, paths ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range paths {
		m.rows = append(m.rows, contracts.FileChange{Repo: folder, Path: p, Change: "modified", Origin: contracts.OriginToolUse})
	}
}

func (m *memStore) eventTypes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, e := range m.events {
		out = append(out, string(e.Type))
	}
	return out
}

func (m *memStore) lastEvent(t *testing.T, typ contracts.EventType) map[string]any {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.events) - 1; i >= 0; i-- {
		if m.events[i].Type == typ {
			var p map[string]any
			if err := json.Unmarshal(m.events[i].Payload, &p); err != nil {
				t.Fatal(err)
			}
			return p
		}
	}
	t.Fatalf("no %s event among %v", typ, m.events)
	return nil
}

func (m *memStore) lastCounts(t *testing.T) (uncommitted, unlanded int) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.patches) == 0 {
		t.Fatal("counts never published")
	}
	p := m.patches[len(m.patches)-1]
	return *p.UncommittedCount, *p.UnlandedCount
}

// isolated is an isolated session over a fresh repo: its worktree made,
// its record written the way the runner writes it.
type isolated struct {
	primary, folder string
	store           *memStore
	ops             Ops
}

func newIsolated(t *testing.T) *isolated {
	t.Helper()
	s := newSpec(t)
	res := ensure(t, s)
	st := &memStore{sess: contracts.Session{
		ID: s.SessionID, Name: "fix the thing", Dir: s.Primary,
		WorktreeDir: res.Dir, WorktreeBranch: res.Branch, WorktreeBase: "main",
	}}
	return &isolated{primary: s.Primary, folder: res.Dir, store: st, ops: Ops{Store: st}}
}

// work writes files in the session's folder and lists them as touched.
func (s *isolated) work(t *testing.T, files map[string]string) {
	t.Helper()
	for name, content := range files {
		writeFile(t, filepath.Join(s.folder, name), content)
		s.store.touch(s.folder, name)
	}
}

func (s *isolated) commit(t *testing.T, req contracts.CommitRequest) contracts.CommitResult {
	t.Helper()
	res, err := s.ops.Commit(context.Background(), s.store.sess.ID, req)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return res
}

func refusal(t *testing.T, err error) contracts.CommitError {
	t.Helper()
	var r *CommitRefusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want a *CommitRefusal", err)
	}
	return r.Body
}

// cleanTree is the user's checkout state Land must never disturb on a
// failure: HEAD, index, porcelain status and the working files' content.
func cleanTree(t *testing.T, dir string) string {
	t.Helper()
	return head(t, dir, "HEAD") + "|" + gitOut(t, dir, "ls-files", "--stage") + "|" +
		gitOut(t, dir, "status", "--porcelain") + "|" + gitOut(t, dir, "diff")
}

func hasMergeHead(dir string) bool {
	_, err := Git(context.Background(), dir, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	return err == nil
}

// Status lists exactly the session's files git sees changed, with kinds:
// not files the agent touched and put back, not other files in the tree.
func TestStatusListsSessionFiles(t *testing.T) {
	s := newIsolated(t)
	commitFile(t, s.folder, "gone.txt", "x\n")
	commitFile(t, s.folder, "reverted.txt", "same\n")
	s.work(t, map[string]string{"README.md": "changed\n", "new/n.txt": "new\n", "reverted.txt": "same\n"})
	s.store.touch(s.folder, "gone.txt")
	if err := os.Remove(filepath.Join(s.folder, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(s.folder, "not-mine.txt"), "other\n")

	st, err := s.ops.Status(context.Background(), s.store.sess.ID, Gates{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range st.Files {
		got = append(got, f.Path+":"+f.Kind)
	}
	slices.Sort(got)
	if want := "README.md:modified,gone.txt:deleted,new/n.txt:added"; strings.Join(got, ",") != want {
		t.Errorf("files = %v, want %s", got, want)
	}
	if !st.Isolated || st.Base != "main" || st.Branch != "mads/session/ses_test" || st.UncommittedCount != 3 || st.UnlandedCount != 2 {
		t.Errorf("status = %+v", st)
	}
	if st.LandBlocked == nil || st.LandBlocked.Reason != contracts.LandDirty || !slices.Contains(st.LandBlocked.Files, "not-mine.txt") {
		t.Errorf("land blocked = %+v, want dirty naming every file in the folder", st.LandBlocked)
	}
	if st.LastCommit == nil || st.LastCommit.Subject != "add reverted.txt" || st.LastCommit.Undoable {
		t.Errorf("last commit = %+v, want the hand-made commit, not undoable", st.LastCommit)
	}
}

// Commit: the user's identity (never mads-session), the Session trailer,
// the hook silenced, the hash recorded against the primary repo, the
// counts republished.
func TestCommitHappyPath(t *testing.T) {
	s := newIsolated(t)
	gitRun(t, s.primary, "config", "user.name", "Drew User")
	gitRun(t, s.primary, "config", "user.email", "drew@example.com")
	// The mads hook reads mads.sessionId; a post-commit hook standing in
	// for it records what it saw.
	gitRun(t, s.folder, "config", "mads.sessionId", "ses_someone_else")
	seen := filepath.Join(t.TempDir(), "hook-saw")
	writeFile(t, filepath.Join(s.primary, ".git", "hooks", "post-commit"),
		"#!/bin/sh\ngit config mads.sessionId > "+seen+"\n")
	if err := os.Chmod(filepath.Join(s.primary, ".git", "hooks", "post-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.work(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})

	res := s.commit(t, contracts.CommitRequest{Paths: []string{"a.txt"}, Message: "Add a\n\nBecause."})
	if res.Hash != head(t, s.folder, "HEAD") || !res.Undoable {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Paths) != 1 || res.Paths[0] != (contracts.CommitPath{Path: "a.txt", Status: contracts.CommitPathCommitted}) {
		t.Errorf("paths = %+v", res.Paths)
	}
	if who := strings.TrimSpace(gitOut(t, s.folder, "log", "-1", "--format=%an <%ae>|%cn")); who != "Drew User <drew@example.com>|Drew User" {
		t.Errorf("identity = %q, want the user's own", who)
	}
	if body := gitOut(t, s.folder, "log", "-1", "--format=%B"); !strings.Contains(body, "Add a\n\nBecause.\n\nSession: ses_test") {
		t.Errorf("message = %q, want the user's message then the trailer", body)
	}
	if files := strings.TrimSpace(gitOut(t, s.folder, "show", "--name-only", "--format=", "HEAD")); files != "a.txt" {
		t.Errorf("committed %q, want only a.txt", files)
	}
	if out, _ := os.ReadFile(seen); strings.TrimSpace(string(out)) != "" {
		t.Errorf("hook saw mads.sessionId=%q, want it blanked", out)
	}
	if len(s.store.commits) != 1 {
		t.Fatalf("recorded %d commits, want 1", len(s.store.commits))
	}
	c := s.store.commits[0]
	if c.Hash != res.Hash || c.Message != "Add a" || !samePath(c.Repo, s.primary) || c.SessionID != "ses_test" {
		t.Errorf("recorded %+v", c)
	}
	if u, l := s.store.lastCounts(t); u != 1 || l != 1 {
		t.Errorf("counts = %d uncommitted, %d unlanded; want 1, 1", u, l)
	}
}

// Every way a path could reach outside the session's own files is refused,
// and nothing is committed.
func TestCommitRejectsEscapes(t *testing.T) {
	s := newIsolated(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "secret\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(s.folder, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.folder, "linkdir")); err != nil {
		t.Fatal(err)
	}
	s.store.touch(s.folder, "link.txt", "linkdir/secret.txt")
	s.work(t, map[string]string{"ok.txt": "ok\n", "unlisted.txt": "x\n"})
	s.store.rows = slices.DeleteFunc(s.store.rows, func(r contracts.FileChange) bool { return r.Path == "unlisted.txt" })
	writeFile(t, filepath.Join(s.folder, "task.txt"), "task\n")
	s.store.rows = append(s.store.rows, contracts.FileChange{Repo: s.folder, Path: "task.txt", TaskID: "tsk_1"})
	before := head(t, s.folder, "HEAD")

	_, err := s.ops.Commit(context.Background(), "ses_test", contracts.CommitRequest{
		Paths:   []string{"ok.txt", "../" + filepath.Base(s.primary) + "/README.md", "/etc/passwd", "link.txt", "linkdir/secret.txt", "unlisted.txt", "task.txt"},
		Message: "sneaky",
	})
	body := refusal(t, err)
	if body.Code != contracts.CommitInvalidPaths || len(body.Selection) != 7 {
		t.Fatalf("refusal = %+v", body)
	}
	reasons := map[string]string{}
	for _, p := range body.Paths {
		reasons[p.Path] = p.Reason
	}
	want := map[string]string{
		"../" + filepath.Base(s.primary) + "/README.md": "outside_folder",
		"/etc/passwd":        "outside_folder",
		"link.txt":           "outside_folder",
		"linkdir/secret.txt": "outside_folder",
		"unlisted.txt":       "not_session_file",
		"task.txt":           "pipeline_task_file",
	}
	for p, r := range want {
		if reasons[p] != r {
			t.Errorf("%s: reason %q, want %q", p, reasons[p], r)
		}
	}
	if _, ok := reasons["ok.txt"]; ok {
		t.Errorf("ok.txt was rejected: %+v", body.Paths)
	}
	if head(t, s.folder, "HEAD") != before || len(s.store.commits) != 0 {
		t.Error("a refused request committed something")
	}
}

// A deleted session file stages and commits as a deletion — including one
// the agent already staged with `git rm`. Anything else the user staged
// stays staged and out of the commit.
func TestCommitStagesDeletion(t *testing.T) {
	s := newIsolated(t)
	commitFile(t, s.folder, "old.txt", "old\n")
	commitFile(t, s.folder, "staged-gone.txt", "x\n")
	s.store.touch(s.folder, "old.txt", "staged-gone.txt")
	if err := os.Remove(filepath.Join(s.folder, "old.txt")); err != nil {
		t.Fatal(err)
	}
	gitRun(t, s.folder, "rm", "-q", "staged-gone.txt")
	writeFile(t, filepath.Join(s.folder, "users.txt"), "theirs\n")
	gitRun(t, s.folder, "add", "users.txt")

	s.commit(t, contracts.CommitRequest{Message: "Remove old files"})
	if got := strings.TrimSpace(gitOut(t, s.folder, "show", "--name-status", "--format=", "HEAD")); got != "D\told.txt\nD\tstaged-gone.txt" {
		t.Errorf("commit = %q, want both deletions only", got)
	}
	if got := gitOut(t, s.folder, "status", "--porcelain"); got != "A  users.txt\n" {
		t.Errorf("status after = %q, want the user's staged file untouched", got)
	}
}

// A selected file with nothing left to commit is reported per path; the
// rest still commit. All of them clean is a refusal.
func TestCommitAlreadyCommittedPerPath(t *testing.T) {
	s := newIsolated(t)
	s.work(t, map[string]string{"done.txt": "done\n", "todo.txt": "todo\n"})
	gitRun(t, s.folder, "add", "done.txt")
	gitRun(t, s.folder, "commit", "-q", "-m", "pipeline merged it")

	res := s.commit(t, contracts.CommitRequest{Paths: []string{"done.txt", "todo.txt"}, Message: "Todo"})
	got := map[string]string{}
	for _, p := range res.Paths {
		got[p.Path] = p.Status
	}
	if got["done.txt"] != contracts.CommitPathAlreadyCommitted || got["todo.txt"] != contracts.CommitPathCommitted {
		t.Errorf("paths = %+v", res.Paths)
	}

	_, err := s.ops.Commit(context.Background(), "ses_test", contracts.CommitRequest{Paths: []string{"done.txt"}, Message: "again"})
	if body := refusal(t, err); body.Code != contracts.CommitNothingToCommit || body.Paths[0].Status != contracts.CommitPathAlreadyCommitted {
		t.Errorf("refusal = %+v", body)
	}
}

// A failing pre-commit hook comes back word for word, with the selection,
// and nothing is committed or recorded.
func TestCommitHookFailureReturnsOutput(t *testing.T) {
	s := newIsolated(t)
	hook := filepath.Join(s.primary, ".git", "hooks", "pre-commit")
	writeFile(t, hook, "#!/bin/sh\necho 'lint: a.txt:1 trailing whitespace' >&2\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	s.work(t, map[string]string{"a.txt": "a \n"})
	before := head(t, s.folder, "HEAD")

	_, err := s.ops.Commit(context.Background(), "ses_test", contracts.CommitRequest{Paths: []string{"a.txt"}, Message: "Add a"})
	body := refusal(t, err)
	if body.Code != contracts.CommitGitFailed || !strings.Contains(body.Detail, "lint: a.txt:1 trailing whitespace") {
		t.Errorf("refusal = %+v, want git_failed with the hook's words", body)
	}
	if !slices.Equal(body.Selection, []string{"a.txt"}) {
		t.Errorf("selection = %v, want it kept", body.Selection)
	}
	if head(t, s.folder, "HEAD") != before || len(s.store.commits) != 0 {
		t.Error("a refused commit happened anyway")
	}
}

// No message is refused (the Router drafts one first when asked to).
func TestCommitRequiresMessage(t *testing.T) {
	s := newIsolated(t)
	s.work(t, map[string]string{"a.txt": "a\n"})
	_, err := s.ops.Commit(context.Background(), "ses_test", contracts.CommitRequest{Message: "  "})
	if body := refusal(t, err); body.Code != contracts.CommitMessageRequired || !slices.Equal(body.Selection, []string{"a.txt"}) {
		t.Errorf("refusal = %+v", body)
	}
}

// A session working in place commits in its checkout, on its branch.
func TestCommitInPlace(t *testing.T) {
	primary := initRepo(t)
	st := &memStore{sess: contracts.Session{ID: "ses_inplace", Dir: filepath.Join(primary, "sub")}}
	writeFile(t, filepath.Join(primary, "sub", "x.txt"), "x\n")
	st.rows = []contracts.FileChange{{Repo: filepath.Join(primary, "sub"), Path: "x.txt"}}
	ops := Ops{Store: st}

	status, err := ops.Status(context.Background(), "ses_inplace", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if status.Isolated || len(status.Files) != 1 || status.Files[0].Path != "sub/x.txt" ||
		status.LandBlocked == nil || status.LandBlocked.Reason != contracts.LandNotIsolated {
		t.Fatalf("status = %+v", status)
	}
	res, err := ops.Commit(context.Background(), "ses_inplace", contracts.CommitRequest{Message: "Add x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Hash != head(t, primary, "main") {
		t.Errorf("commit %s not on main (%s)", res.Hash, head(t, primary, "main"))
	}
}

// Undo takes back the tip while it's a session Commit, leaving its changes
// staged and dropping the record; anything else is refused with a reason.
func TestUndo(t *testing.T) {
	s := newIsolated(t)
	s.work(t, map[string]string{"a.txt": "a\n"})
	first := s.commit(t, contracts.CommitRequest{Message: "A"})
	s.work(t, map[string]string{"b.txt": "b\n"})
	second := s.commit(t, contracts.CommitRequest{Paths: []string{"b.txt"}, Message: "B"})

	res, err := s.ops.Undo(context.Background(), "ses_test", first.Hash)
	if err != nil || res.Undone || res.Reason != "not_tip" {
		t.Fatalf("undo non-tip = %+v, %v; want refused not_tip", res, err)
	}
	res, err = s.ops.Undo(context.Background(), "ses_test", second.Hash[:12])
	if err != nil || !res.Undone || res.Hash != second.Hash {
		t.Fatalf("undo tip = %+v, %v", res, err)
	}
	if head(t, s.folder, "HEAD") != first.Hash {
		t.Error("HEAD didn't go back to the previous commit")
	}
	if got := gitOut(t, s.folder, "status", "--porcelain", "--", "b.txt"); got != "A  b.txt\n" {
		t.Errorf("b.txt after undo = %q, want staged", got)
	}
	if !slices.Equal(s.store.deleted, []string{second.Hash}) {
		t.Errorf("deleted records = %v", s.store.deleted)
	}

	gitRun(t, s.folder, "commit", "-q", "-m", "by hand")
	res, _ = s.ops.Undo(context.Background(), "ses_test", head(t, s.folder, "HEAD"))
	if res.Undone || res.Reason != "not_session_commit" {
		t.Errorf("undo of a hand-made commit = %+v", res)
	}
}

// Land: a --no-ff merge commit in the user's checkout with the user's
// identity and the trailer, recorded; the session branch fast-forwards
// after, so nothing is left unlanded.
func TestLandHappyPath(t *testing.T) {
	s := newIsolated(t)
	s.work(t, map[string]string{"feature.txt": "f\n"})
	s.commit(t, contracts.CommitRequest{Message: "Feature"})
	commitFile(t, s.primary, "elsewhere.txt", "base moved on\n")

	res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.LandLanded || res.MergeCommit != head(t, s.primary, "main") || !res.FastForwarded {
		t.Fatalf("result = %+v", res)
	}
	if parents := strings.Fields(gitOut(t, s.primary, "log", "-1", "--format=%P")); len(parents) != 2 {
		t.Errorf("merge commit has parents %v, want 2 (--no-ff)", parents)
	}
	if body := gitOut(t, s.primary, "log", "-1", "--format=%B"); !strings.HasPrefix(body, "Land fix the thing\n\nSession: ses_test") {
		t.Errorf("merge message = %q", body)
	}
	if who := strings.TrimSpace(gitOut(t, s.primary, "log", "-1", "--format=%an")); who != "t" {
		t.Errorf("merge author = %q, want the user's", who)
	}
	if head(t, s.folder, "HEAD") != res.MergeCommit {
		t.Error("session branch didn't fast-forward to the landed base")
	}
	if _, err := os.Stat(filepath.Join(s.primary, "feature.txt")); err != nil {
		t.Error("the work didn't land in the checkout")
	}
	if _, err := os.Stat(s.folder); err != nil {
		t.Error("Land removed the session's folder")
	}
	if u, l := s.store.lastCounts(t); u != 0 || l != 0 {
		t.Errorf("counts = %d, %d; want 0, 0", u, l)
	}
	last := s.store.commits[len(s.store.commits)-1]
	if last.Hash != res.MergeCommit || last.Message != "Land fix the thing" || !samePath(last.Repo, s.primary) {
		t.Errorf("recorded %+v", last)
	}
	if got := strings.Join(s.store.eventTypes(), ","); got != "merge.attempted,merge.completed" {
		t.Errorf("events = %s", got)
	}
	if p := s.store.lastEvent(t, contracts.MergeCompleted); p["merge_commit"] != res.MergeCommit || p["kind"] != "land" || p["base"] != "main" {
		t.Errorf("merge.completed = %v", p)
	}
}

// A conflict bringing the base into the session folder aborts there and
// names the files; the user's checkout is untouched.
func TestLandConflictInStep1(t *testing.T) {
	s := newIsolated(t)
	s.work(t, map[string]string{"README.md": "session version\n"})
	s.commit(t, contracts.CommitRequest{Message: "Session edit"})
	commitFile(t, s.primary, "README.md", "base version\n")
	primaryBefore := cleanTree(t, s.primary)
	folderBefore := head(t, s.folder, "HEAD")

	res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.LandConflicted || !slices.Equal(res.Files, []string{"README.md"}) {
		t.Fatalf("result = %+v", res)
	}
	if cleanTree(t, s.primary) != primaryBefore {
		t.Error("a conflict in step 1 changed the user's checkout")
	}
	if hasMergeHead(s.folder) || head(t, s.folder, "HEAD") != folderBefore || gitOut(t, s.folder, "status", "--porcelain") != "" {
		t.Error("the conflicted merge wasn't aborted in the session folder")
	}
	if p := s.store.lastEvent(t, contracts.MergeConflicted); p["step"] != "base" {
		t.Errorf("merge.conflicted = %v", p)
	}
}

// The base moves between the steps: Land starts over and still lands
// everything; a base that never stops moving is refused.
func TestLandBaseMovedBetweenSteps(t *testing.T) {
	s := newIsolated(t)
	s.work(t, map[string]string{"feature.txt": "f\n"})
	s.commit(t, contracts.CommitRequest{Message: "Feature"})

	calls := 0
	afterStep1 = func() {
		calls++
		if calls == 1 {
			commitFile(t, s.primary, "racer.txt", "landed meanwhile\n")
		}
	}
	t.Cleanup(func() { afterStep1 = nil })
	res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.LandLanded || calls != 2 {
		t.Fatalf("result = %+v after %d step-1 runs, want landed after 2", res, calls)
	}
	if _, err := Git(context.Background(), s.primary, "merge-base", "--is-ancestor", "HEAD~1", head(t, s.folder, "HEAD")); err != nil {
		t.Error("landed without the commit that raced in")
	}

	s.work(t, map[string]string{"more.txt": "m\n"})
	s.commit(t, contracts.CommitRequest{Paths: []string{"more.txt"}, Message: "More"})
	n := 0
	afterStep1 = func() {
		n++
		commitFile(t, s.primary, "racer.txt", strings.Repeat("x", n)+"\n")
	}
	res, err = s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil || res.Outcome != contracts.LandRefused || res.Reason != contracts.LandBaseMoving || n != landTries {
		t.Fatalf("ever-moving base = %+v, %v after %d tries", res, err, n)
	}
}

// The user's checkout refuses the merge: aborted every time, the
// checkout left exactly as it was, and the reason named.
func TestLandPrimaryRefusals(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, s *isolated)
		reason string
		files  []string
		detail string
	}{
		{
			name: "dirty overlap",
			setup: func(t *testing.T, s *isolated) {
				writeFile(t, filepath.Join(s.primary, "README.md"), "user's own edit\n")
			},
			reason: contracts.LandPrimaryDirty, files: []string{"README.md"},
		},
		{
			name: "untracked in the way",
			setup: func(t *testing.T, s *isolated) {
				writeFile(t, filepath.Join(s.primary, "feature.txt"), "user's scratch\n")
			},
			reason: contracts.LandPrimaryUntracked, files: []string{"feature.txt"},
		},
		{
			name: "pre-merge-commit hook",
			setup: func(t *testing.T, s *isolated) {
				hook := filepath.Join(s.primary, ".git", "hooks", "pre-merge-commit")
				writeFile(t, hook, "#!/bin/sh\necho 'no merges on fridays' >&2\nexit 1\n")
				if err := os.Chmod(hook, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			reason: contracts.LandPrimaryRefused, detail: "no merges on fridays",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newIsolated(t)
			s.work(t, map[string]string{"README.md": "session\n", "feature.txt": "f\n"})
			s.commit(t, contracts.CommitRequest{Message: "Work"})
			tc.setup(t, s)
			before := cleanTree(t, s.primary)

			res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != contracts.LandRefused || res.Reason != tc.reason || !slices.Equal(res.Files, tc.files) ||
				!strings.Contains(res.Detail, tc.detail) {
				t.Fatalf("result = %+v", res)
			}
			if hasMergeHead(s.primary) {
				t.Error("the checkout was left mid-merge")
			}
			if cleanTree(t, s.primary) != before {
				t.Errorf("the checkout changed:\nbefore %s\nafter  %s", before, cleanTree(t, s.primary))
			}
			if got := strings.Join(s.store.eventTypes(), ","); got != "merge.attempted,merge.refused" {
				t.Errorf("events = %s", got)
			}
		})
	}
}

// Every gate refuses before anything runs, with its reason and an event.
func TestLandGates(t *testing.T) {
	cases := []struct {
		name   string
		gates  Gates
		setup  func(t *testing.T, s *isolated)
		reason string
	}{
		{name: "busy", gates: Gates{Busy: true}, reason: contracts.LandBusy},
		{name: "pipeline", gates: Gates{Pipeline: true}, reason: contracts.LandPipeline},
		{name: "mid merge", reason: contracts.LandMidMerge, setup: func(t *testing.T, s *isolated) {
			gitRun(t, s.folder, "checkout", "-q", "-b", "side", "HEAD~1")
			commitFile(t, s.folder, "README.md", "side\n")
			gitRun(t, s.folder, "checkout", "-q", "mads/session/ses_test")
			commitFile(t, s.folder, "README.md", "session again\n")
			_, _ = Git(context.Background(), s.folder, "merge", "side")
		}},
		{name: "off branch", reason: contracts.LandOffBranch, setup: func(t *testing.T, s *isolated) {
			gitRun(t, s.folder, "checkout", "-q", "-b", "agent-made")
		}},
		{name: "dirty", reason: contracts.LandDirty, setup: func(t *testing.T, s *isolated) {
			writeFile(t, filepath.Join(s.folder, "scratch.txt"), "untracked\n")
		}},
		{name: "base not checked out", reason: contracts.LandBaseNotCheckedOut, setup: func(t *testing.T, s *isolated) {
			gitRun(t, s.primary, "checkout", "-q", "-b", "other")
		}},
		{name: "primary mid merge", reason: contracts.LandPrimaryMidMerge, setup: func(t *testing.T, s *isolated) {
			gitRun(t, s.primary, "checkout", "-q", "-b", "theirs")
			commitFile(t, s.primary, "c.txt", "theirs\n")
			gitRun(t, s.primary, "checkout", "-q", "main")
			commitFile(t, s.primary, "c.txt", "mine\n")
			_, _ = Git(context.Background(), s.primary, "merge", "theirs")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newIsolated(t)
			s.work(t, map[string]string{"README.md": "session\n"})
			s.commit(t, contracts.CommitRequest{Message: "Work"})
			if tc.setup != nil {
				tc.setup(t, s)
			}
			before := cleanTree(t, s.primary)
			res, err := s.ops.Land(context.Background(), "ses_test", tc.gates)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != contracts.LandRefused || res.Reason != tc.reason {
				t.Fatalf("result = %+v, want refused %s", res, tc.reason)
			}
			if cleanTree(t, s.primary) != before {
				t.Error("a gate refusal changed the checkout")
			}
			// An off-branch folder also gets ensure's worktree.ensured card.
			merges := slices.DeleteFunc(s.store.eventTypes(), func(typ string) bool { return !strings.HasPrefix(typ, "merge.") })
			if got := strings.Join(merges, ","); got != "merge.refused" {
				t.Errorf("events = %s, want just merge.refused", got)
			}
			if p := s.store.lastEvent(t, contracts.MergeRefused); p["reason"] != tc.reason {
				t.Errorf("merge.refused = %v", p)
			}
		})
	}

	t.Run("nothing to land", func(t *testing.T) {
		s := newIsolated(t)
		res, _ := s.ops.Land(context.Background(), "ses_test", Gates{})
		if res.Reason != contracts.LandNothingToLand {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("not isolated", func(t *testing.T) {
		st := &memStore{sess: contracts.Session{ID: "ses_inplace", Dir: initRepo(t)}}
		res, _ := Ops{Store: st}.Land(context.Background(), "ses_inplace", Gates{})
		if res.Reason != contracts.LandNotIsolated {
			t.Errorf("result = %+v", res)
		}
	})
}

// Land brings back a lost folder before it starts (D2), and says so.
func TestLandEnsuresLostFolder(t *testing.T) {
	s := newIsolated(t)
	s.work(t, map[string]string{"feature.txt": "f\n"})
	s.commit(t, contracts.CommitRequest{Message: "Feature"})
	if err := os.RemoveAll(s.folder); err != nil {
		t.Fatal(err)
	}
	res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil || res.Outcome != contracts.LandLanded {
		t.Fatalf("land = %+v, %v", res, err)
	}
	if p := s.store.lastEvent(t, contracts.WorktreeEnsured); p["action"] != string(ActionReattached) {
		t.Errorf("worktree.ensured = %v", p)
	}
}

// Delete cleanup removes the folder, the branch and the lock file; a
// session that was never isolated is left alone.
func TestCleanup(t *testing.T) {
	s := newIsolated(t)
	s.work(t, map[string]string{"wip.txt": "uncommitted\n"})
	s.commit(t, contracts.CommitRequest{Message: "Unlanded"})
	writeFile(t, filepath.Join(s.folder, "more.txt"), "dirty\n")
	lock, err := SessionLockPath(context.Background(), s.primary, "ses_test")
	if err != nil {
		t.Fatal(err)
	}

	Cleanup(context.Background(), s.store.sess)
	if _, err := os.Stat(s.folder); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("folder still there: %v", err)
	}
	if refExists(context.Background(), s.primary, "refs/heads/mads/session/ses_test") {
		t.Error("branch still there")
	}
	if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("lock file still there: %v", err)
	}
	if list := gitOut(t, s.primary, "worktree", "list"); strings.Count(list, "\n") != 1 {
		t.Errorf("worktree still registered:\n%s", list)
	}

	primary := initRepo(t)
	Cleanup(context.Background(), contracts.Session{ID: "ses_inplace", Dir: primary})
	if _, err := os.Stat(filepath.Join(primary, "README.md")); err != nil {
		t.Error("cleanup touched an in-place session's checkout")
	}
}

// The draft's diff covers tracked changes and new files, and is cut to
// the limit.
func TestDraftDiff(t *testing.T) {
	s := newIsolated(t)
	s.work(t, map[string]string{"README.md": "changed line\n", "new.txt": "brand new content\n"})
	diff, truncated, err := DraftDiff(context.Background(), s.folder, []string{"README.md", "new.txt"}, DraftDiffLimit)
	if err != nil || truncated {
		t.Fatalf("diff err=%v truncated=%v", err, truncated)
	}
	for _, want := range []string{"+changed line", "+brand new content", "new.txt"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff lacks %q:\n%s", want, diff)
		}
	}
	short, truncated, _ := DraftDiff(context.Background(), s.folder, []string{"README.md", "new.txt"}, 20)
	if !truncated || len(short) != 20 {
		t.Errorf("cut diff = %d bytes, truncated %v", len(short), truncated)
	}
}
