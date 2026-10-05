package sessionwt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/githook"
)

// taskBranch makes a task branch off the session branch, as the pipeline
// does, with files written and committed on it.
func taskBranch(t *testing.T, s *isolated, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "task")
	gitRun(t, s.folder, "worktree", "add", "-q", "-b", "mads/tsk_1", dir)
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "task work")
	return "mads/tsk_1"
}

func (s *isolated) taskMerge() *TaskMerge {
	t, _ := TargetFor(context.Background(), s.store.sess)
	return &TaskMerge{Store: s.store, Target: t, Poll: 10 * time.Millisecond}
}

// Overlap is what the merge would refuse over: dirty in the folder (staged,
// unstaged, untracked or renamed) and changed on the branch. A dirty file
// the branch never touched, and a branch file nobody edited, are not.
func TestOverlap(t *testing.T) {
	s := newIsolated(t)
	commitFile(t, s.folder, "old.txt", "old\n")
	commitFile(t, s.folder, "staged.txt", "s\n")
	branch := taskBranch(t, s, map[string]string{
		"README.md": "task\n", "new.txt": "task\n", "old.txt": "task\n", "staged.txt": "task\n", "clean.txt": "task\n",
	})
	writeFile(t, filepath.Join(s.folder, "README.md"), "agent\n")
	writeFile(t, filepath.Join(s.folder, "new.txt"), "agent\n")
	writeFile(t, filepath.Join(s.folder, "staged.txt"), "agent\n")
	gitRun(t, s.folder, "add", "staged.txt")
	gitRun(t, s.folder, "mv", "old.txt", "moved.txt")
	writeFile(t, filepath.Join(s.folder, "mine.txt"), "agent\n")

	got, err := Overlap(context.Background(), s.folder, branch)
	if err != nil {
		t.Fatal(err)
	}
	if want := "README.md,new.txt,old.txt,staged.txt"; strings.Join(got, ",") != want {
		t.Errorf("overlap = %v, want %s", got, want)
	}

	if got, _ := Overlap(context.Background(), s.primary, branch); len(got) != 0 {
		t.Errorf("a clean checkout overlaps nothing, got %v", got)
	}
}

// SaveEdits commits exactly the files given, as mads-session, with the
// Session trailer; other staged work stays staged and out of the commit;
// and the commit is recorded once — by SaveEdits, not again by the mads
// post-commit hook configured in the folder.
func TestSaveEditsCommitsOnlyThoseFilesAndRecordsOnce(t *testing.T) {
	s := newIsolated(t)
	commitFile(t, s.folder, "gone.txt", "x\n")

	var hookPosts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hookPosts.Add(1)
	}))
	defer srv.Close()
	ctx := context.Background()
	if err := githook.EnsureInstalled(ctx, s.primary); err != nil {
		t.Fatal(err)
	}
	if err := githook.SetWorktreeConfig(ctx, s.folder, githook.Config{SessionID: s.store.sess.ID, DataURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	// The hook does fire for an ordinary commit in this folder, or the
	// silence below would prove nothing.
	commitFile(t, s.folder, "probe.txt", "p\n")
	if hookPosts.Load() != 1 {
		t.Skipf("post-commit hook didn't report (curl missing?): %d posts", hookPosts.Load())
	}

	writeFile(t, filepath.Join(s.folder, "README.md"), "agent\n")
	writeFile(t, filepath.Join(s.folder, "new.txt"), "agent\n")
	if err := os.Remove(filepath.Join(s.folder, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(s.folder, "other.txt"), "user\n")
	gitRun(t, s.folder, "add", "other.txt")

	hash, err := s.taskMerge().SaveEdits(ctx, []string{"README.md", "gone.txt", "new.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if hash != head(t, s.folder, "HEAD") {
		t.Errorf("hash %s isn't the folder's HEAD", hash)
	}
	if got := gitOut(t, s.folder, "log", "-1", "--format=%an <%ae>|%cn|%s|%(trailers:key=Session,valueonly)"); strings.TrimSpace(got) != "mads-session <session@mads.local>|mads-session|"+SaveEditsMessage+"|"+s.store.sess.ID {
		t.Errorf("commit = %q", got)
	}
	if got := strings.Fields(gitOut(t, s.folder, "show", "--name-status", "--format=", "HEAD")); strings.Join(got, " ") != "M README.md D gone.txt A new.txt" {
		t.Errorf("commit changed %v", got)
	}
	if got := gitOut(t, s.folder, "status", "--porcelain"); strings.TrimSpace(got) != "A  other.txt" {
		t.Errorf("status after = %q, want only other.txt still staged", got)
	}
	if hookPosts.Load() != 1 {
		t.Errorf("hook reported the safety commit too: %d posts", hookPosts.Load())
	}
	if len(s.store.commits) != 1 || s.store.commits[0].Hash != hash || s.store.commits[0].Message != SaveEditsMessage {
		t.Errorf("recorded commits = %+v", s.store.commits)
	}
	if r := s.store.reports[0]; r.Source != contracts.CommitSourcePipelineSave || r.Files != 3 {
		t.Errorf("report = %+v, want source pipeline_save over 3 files", r)
	}
}

func TestWaitIdle(t *testing.T) {
	s := newIsolated(t)
	m := s.taskMerge()
	ctx := context.Background()

	if idle, err := m.WaitIdle(ctx, time.Now()); err != nil || !idle {
		t.Errorf("no agent state: idle=%v err=%v, want idle", idle, err)
	}

	s.store.mu.Lock()
	s.store.sess.AgentState = &contracts.AgentState{State: contracts.AgentWorking}
	s.store.mu.Unlock()
	start := time.Now()
	if idle, err := m.WaitIdle(ctx, start.Add(100*time.Millisecond)); err != nil || idle {
		t.Errorf("working past the deadline: idle=%v err=%v, want timed out", idle, err)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Errorf("gave up after %s, before the deadline", time.Since(start))
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		s.store.mu.Lock()
		s.store.sess.AgentState = &contracts.AgentState{State: contracts.AgentIdle}
		s.store.mu.Unlock()
	}()
	if idle, err := m.WaitIdle(ctx, time.Now().Add(10*time.Second)); err != nil || !idle {
		t.Errorf("agent went idle: idle=%v err=%v, want idle", idle, err)
	}
}
