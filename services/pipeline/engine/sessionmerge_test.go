package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
)

// sessionWorld is the harness's repo as the user's checkout, plus an
// isolated session's worktree on top of it, recorded on the session the way
// the session runner records it (D1).
type sessionWorld struct {
	*harness
	folder string
	branch string
	merge  *sessionwt.TaskMerge
}

func newSessionWorld(t *testing.T) *sessionWorld {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	res, err := sessionwt.EnsureWithLock(ctx, sessionwt.Spec{
		Primary: h.cwd, Root: t.TempDir(), SessionID: h.sess.ID, Base: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	base := "main"
	if err := h.data.SetSessionWorktree(ctx, h.sess.ID, contracts.SessionWorktreePatch{
		WorktreeDir: &res.Dir, WorktreeBranch: &res.Branch, WorktreeBase: &base,
	}); err != nil {
		t.Fatal(err)
	}
	sess, err := h.data.Session(ctx, h.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess.Dir = h.cwd
	target, err := sessionwt.TargetFor(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	return &sessionWorld{
		harness: h, folder: res.Dir, branch: res.Branch,
		merge: &sessionwt.TaskMerge{Store: h.data, Target: target, Poll: 10 * time.Millisecond},
	}
}

// commit adds a file to the session branch, as the session's own work.
func (w *sessionWorld) commit(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(w.folder, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, w.folder, "add", name)
	gitRun(t, w.folder, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "session: "+name)
}

func (w *sessionWorld) setAgent(t *testing.T, state string) {
	t.Helper()
	if err := w.data.SetSessionAgentState(context.Background(), w.sess.ID, contracts.AgentState{State: state, Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func (w *sessionWorld) config(t *testing.T, p *fake.Fake, tpl string) Config {
	t.Helper()
	parsed, verr := template.Check([]byte(tpl), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	return Config{
		Data: w.data, Provider: p, SessionID: w.sess.ID,
		Template: parsed, Cwd: w.folder, Concurrency: 1,
		StageTimeout: 30 * time.Second,
		Isolated:     true, WorktreeRoot: t.TempDir(), DataURL: w.base,
		Session: w.merge,
	}
}

// start runs the engine in the background; the returned func waits for it.
func (w *sessionWorld) start(t *testing.T, cfg Config) func() *Summary {
	t.Helper()
	type result struct {
		sum *Summary
		err error
	}
	done := make(chan result, 1)
	go func() {
		sum, err := Run(context.Background(), cfg)
		done <- result{sum, err}
	}()
	return func() *Summary {
		t.Helper()
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatal(r.err)
			}
			return r.sum
		case <-time.After(60 * time.Second):
			t.Fatal("run did not finish")
			return nil
		}
	}
}

func payload(t *testing.T, ev contracts.Envelope) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// checkoutState is what a pipeline into a session folder must never change
// in the user's own checkout: HEAD, the index, and the working tree.
func checkoutState(t *testing.T, dir string) string {
	t.Helper()
	return gitOut(t, dir, "rev-parse", "HEAD") + gitOut(t, dir, "ls-files", "--stage") +
		gitOut(t, dir, "status", "--porcelain") + gitOut(t, dir, "diff")
}

// D8: an isolated session's tasks fork from the session branch and land on
// it, never on the user's checkout, and the header counts follow.
func TestSessionTasksLandOnSessionBranch(t *testing.T) {
	w := newSessionWorld(t)
	w.commit(t, "session.txt", "session\n")
	sessionHead := strings.TrimSpace(gitOut(t, w.folder, "rev-parse", "HEAD"))
	before := checkoutState(t, w.cwd)

	f := mustScenario(t,
		fake.Rule{Match: "Task: alpha", Reply: "done", Tools: []fake.ToolStep{{Name: "write_file", Path: "alpha.txt", Content: "alpha\n"}}},
		fake.Rule{Match: "Task: beta", Reply: "done", Tools: []fake.ToolStep{{Name: "write_file", Path: "beta.txt", Content: "beta\n"}}},
	)
	seeded := w.seed(contracts.Task{Title: "alpha"}, contracts.Task{Title: "beta"})
	sum := w.start(t, w.config(t, f, isolatedTemplate))()
	if len(sum.Done) != 2 {
		t.Fatalf("summary: %+v", sum)
	}

	for _, name := range []string{"alpha.txt", "beta.txt", "session.txt"} {
		if _, err := os.Stat(filepath.Join(w.folder, name)); err != nil {
			t.Errorf("%s missing from the session folder", name)
		}
	}
	if got := gitOut(t, w.cwd, "log", "--format=%s", w.branch); !strings.Contains(got, "Merge mads/"+seeded[0].ID) || !strings.Contains(got, "Merge mads/"+seeded[1].ID) {
		t.Errorf("session branch log lacks the task merges:\n%s", got)
	}
	if after := checkoutState(t, w.cwd); after != before {
		t.Errorf("the user's checkout changed:\nbefore %s\nafter  %s", before, after)
	}
	for _, name := range []string{"alpha.txt", "beta.txt", "session.txt"} {
		if _, err := os.Stat(filepath.Join(w.cwd, name)); err == nil {
			t.Errorf("%s leaked into the user's checkout", name)
		}
	}
	// The first task forked from the session branch, not from main.
	for _, ev := range w.events(t, "merge.attempted") {
		if ev.TaskID == seeded[0].ID {
			if p := payload(t, ev); p["fork_sha"] != sessionHead || p["target_branch"] != w.branch {
				t.Errorf("merge.attempted = %v, want fork_sha %s into %s", p, sessionHead, w.branch)
			}
		}
	}
	// Counts: 1 session commit + 2 task merges (each with its task commit).
	sess, err := w.data.Session(context.Background(), w.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(gitOut(t, w.cwd, "rev-list", "--count", "main.."+w.branch))
	if fmt.Sprint(sess.UnlandedCount) != want || sess.UnlandedCount < 3 {
		t.Errorf("unlanded_count = %d, want %s", sess.UnlandedCount, want)
	}
	if strings.TrimSpace(gitOut(t, w.folder, "status", "--porcelain")) != "" {
		t.Errorf("session folder left dirty:\n%s", gitOut(t, w.folder, "status", "--porcelain"))
	}
}

// notesTemplate's task edits the last line of notes.txt; the overlap tests
// have the agent edit its first line, so the two merge cleanly once the
// agent's edit is committed.
func notesFiles(t *testing.T, w *sessionWorld) (agentEdit, taskContent string) {
	t.Helper()
	lines := []string{"one", "two", "three", "four", "five", "six", "seven", "eight"}
	w.commit(t, "notes.txt", strings.Join(lines, "\n")+"\n")
	agent := append([]string{"ONE (agent)"}, lines[1:]...)
	task := append(append([]string{}, lines[:7]...), "EIGHT (task)")
	return strings.Join(agent, "\n") + "\n", strings.Join(task, "\n") + "\n"
}

func notesScenario(t *testing.T, taskContent string) *fake.Fake {
	return mustScenario(t, fake.Rule{Match: "Task: notes", Reply: "done",
		Tools: []fake.ToolStep{{Name: "write_file", Path: "notes.txt", Content: taskContent}}})
}

func saveCommits(t *testing.T, dir string) []string {
	t.Helper()
	out := gitOut(t, dir, "log", "--format=%H %an %s", "--grep", sessionwt.SaveEditsMessage)
	return splitLines(out)
}

// Agent idle: the agent's uncommitted edit to a file the task also changed
// is committed (safety identity, Session trailer, recorded once), then the
// task merges — both edits survive. An edit the task didn't touch stays
// uncommitted.
func TestSessionMergeSavesOverlappingEditsWhenIdle(t *testing.T) {
	w := newSessionWorld(t)
	agentEdit, taskContent := notesFiles(t, w)
	if err := os.WriteFile(filepath.Join(w.folder, "notes.txt"), []byte(agentEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.folder, "mine.txt"), []byte("not the task's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.setAgent(t, contracts.AgentIdle)

	w.seed(contracts.Task{Title: "notes"})
	sum := w.start(t, w.config(t, notesScenario(t, taskContent), isolatedTemplate))()
	if len(sum.Done) != 1 {
		t.Fatalf("summary: %+v; events: %v", sum, w.events(t, "stage.failed", "merge.conflicted"))
	}

	got, _ := os.ReadFile(filepath.Join(w.folder, "notes.txt"))
	if !strings.Contains(string(got), "ONE (agent)") || !strings.Contains(string(got), "EIGHT (task)") {
		t.Errorf("notes.txt lost an edit:\n%s", got)
	}
	saves := saveCommits(t, w.folder)
	if len(saves) != 1 || !strings.Contains(saves[0], " mads-session ") {
		t.Fatalf("save commits = %v, want one by mads-session", saves)
	}
	hash := strings.Fields(saves[0])[0]
	if tr := strings.TrimSpace(gitOut(t, w.folder, "show", "-s", "--format=%(trailers:key=Session,valueonly)", hash)); tr != w.sess.ID {
		t.Errorf("Session trailer = %q", tr)
	}
	if files := strings.TrimSpace(gitOut(t, w.folder, "show", "--name-only", "--format=", hash)); files != "notes.txt" {
		t.Errorf("save commit has %q, want notes.txt only", files)
	}
	if st := strings.TrimSpace(gitOut(t, w.folder, "status", "--porcelain")); st != "?? mine.txt" {
		t.Errorf("status = %q, want mine.txt still uncommitted", st)
	}
	commits, err := w.data.Commits(context.Background(), w.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range commits {
		if c.Hash == hash {
			n++
		}
	}
	if n != 1 {
		t.Errorf("save commit recorded %d times, want once: %+v", n, commits)
	}
	evs := w.events(t, "merge.completed")
	if len(evs) != 1 {
		t.Fatalf("merge.completed events: %d", len(evs))
	}
	p := payload(t, evs[0])
	if fmt.Sprint(p["auto_committed"]) != "[notes.txt]" || fmt.Sprint(p["auto_commits"]) != "["+hash+"]" || p["waited_out"] != nil || p["task_title"] != "notes" {
		t.Errorf("merge.completed = %v", p)
	}
}

// Agent working: the merge waits, holding no lock, until the agent goes
// idle — then saves and merges.
func TestSessionMergeWaitsForWorkingAgent(t *testing.T) {
	w := newSessionWorld(t)
	agentEdit, taskContent := notesFiles(t, w)
	if err := os.WriteFile(filepath.Join(w.folder, "notes.txt"), []byte(agentEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	w.setAgent(t, contracts.AgentWorking)

	seeded := w.seed(contracts.Task{Title: "notes"})
	wait := w.start(t, w.config(t, notesScenario(t, taskContent), isolatedTemplate))

	waitFor(t, func() bool { return len(w.events(t, "merge.attempted")) > 0 })
	time.Sleep(300 * time.Millisecond)
	if got := w.task(t, seeded[0].ID); got.Status == contracts.StatusDone {
		t.Fatal("merged while the agent was working")
	}
	if len(saveCommits(t, w.folder)) != 0 {
		t.Fatal("saved the agent's edits while it was working")
	}
	// Waiting holds no lock: a Commit could take the session lock now.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, err := w.merge.Lock(ctx)
	if err != nil {
		t.Fatalf("session lock held while waiting for the agent: %v", err)
	}
	release()

	w.setAgent(t, contracts.AgentIdle)
	if sum := wait(); len(sum.Done) != 1 {
		t.Fatalf("summary: %+v", sum)
	}
	if len(saveCommits(t, w.folder)) != 1 {
		t.Errorf("save commits = %v", saveCommits(t, w.folder))
	}
	if p := payload(t, w.events(t, "merge.completed")[0]); p["waited_out"] != nil {
		t.Errorf("merge.completed = %v, want no waited_out", p)
	}
}

// Agent still working at the merge stage's timeout: save and merge anyway,
// and say so on merge.completed with the files that changed under it.
func TestSessionMergeWaitTimesOut(t *testing.T) {
	w := newSessionWorld(t)
	agentEdit, taskContent := notesFiles(t, w)
	if err := os.WriteFile(filepath.Join(w.folder, "notes.txt"), []byte(agentEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	w.setAgent(t, contracts.AgentWorking)

	tpl := strings.Replace(isolatedTemplate, "command: git merge --no-ff", "command: git merge --no-ff\n    timeout: 500ms", 1)
	w.seed(contracts.Task{Title: "notes"})
	sum := w.start(t, w.config(t, notesScenario(t, taskContent), tpl))()
	if len(sum.Done) != 1 {
		t.Fatalf("summary: %+v; events: %v", sum, w.events(t, "stage.failed", "merge.conflicted"))
	}
	got, _ := os.ReadFile(filepath.Join(w.folder, "notes.txt"))
	if !strings.Contains(string(got), "ONE (agent)") || !strings.Contains(string(got), "EIGHT (task)") {
		t.Errorf("notes.txt lost an edit:\n%s", got)
	}
	p := payload(t, w.events(t, "merge.completed")[0])
	// files is everything the merge changed (the executor's mounted skills
	// ride along on the task branch too), which must name notes.txt.
	files, _ := p["files"].([]any)
	if p["waited_out"] != true || !slices.Contains(files, any("notes.txt")) || fmt.Sprint(p["auto_committed"]) != "[notes.txt]" {
		t.Errorf("merge.completed = %v, want waited_out with notes.txt among files", p)
	}
}

// lockTemplate parks the task, after its worktree exists and before its
// merge, until the test says go — so the test can take a lock first.
func lockTemplate(ready, goFile string) string {
	return fmt.Sprintf(`name: lock
stages:
  - name: executor
    agent: executor
  - name: park
    command: touch %s && while [ ! -f %s ]; do sleep 0.02; done
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: make test
`, ready, goFile)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// A merge into a session folder takes the session lock, not the repo
// lock: a Land holding mads-land.lock elsewhere in the repo doesn't hold
// it up, but the session's own Commit or Land does.
func TestSessionMergeLockChoice(t *testing.T) {
	for _, tc := range []struct {
		name    string
		acquire func(*sessionWorld) (func(), error)
		blocks  bool
	}{
		{"repo lock", func(w *sessionWorld) (func(), error) {
			return sessionwt.AcquireLock(context.Background(), w.cwd)
		}, false},
		{"session lock", func(w *sessionWorld) (func(), error) {
			return w.merge.Lock(context.Background())
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newSessionWorld(t)
			dir := t.TempDir()
			ready, goFile := filepath.Join(dir, "ready"), filepath.Join(dir, "go")
			f := mustScenario(t, fake.Rule{Match: "Task: alpha", Reply: "done",
				Tools: []fake.ToolStep{{Name: "write_file", Path: "alpha.txt", Content: "alpha\n"}}})
			seeded := w.seed(contracts.Task{Title: "alpha"})
			wait := w.start(t, w.config(t, f, lockTemplate(ready, goFile)))

			waitFor(t, func() bool { return exists(ready) })
			release, err := tc.acquire(w)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goFile, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.blocks {
				time.Sleep(500 * time.Millisecond)
				if got := w.task(t, seeded[0].ID); got.Status == contracts.StatusDone {
					t.Fatal("merged while the session lock was held")
				}
				release()
			} else {
				defer release()
			}
			if sum := wait(); len(sum.Done) != 1 {
				t.Fatalf("summary: %+v", sum)
			}
			if !exists(filepath.Join(w.folder, "alpha.txt")) {
				t.Error("alpha.txt didn't land in the session folder")
			}
		})
	}
}

// The agent is mid-merge in the session folder (resolving a Land conflict):
// the task merge waits for that to settle and, when it doesn't by the
// stage timeout, fails saying so — it never aborts the agent's merge, so
// MERGE_HEAD and the agent's half-done resolution survive.
func TestSessionMergeWaitsOutAgentMidMerge(t *testing.T) {
	w := newSessionWorld(t)
	id := []string{"-c", "user.email=t@t", "-c", "user.name=t"}
	w.commit(t, "clash.txt", "base\n")
	gitRun(t, w.folder, "branch", "side")
	w.commit(t, "clash.txt", "session\n")
	gitRun(t, w.folder, "checkout", "-q", "side")
	if err := os.WriteFile(filepath.Join(w.folder, "clash.txt"), []byte("side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, w.folder, append(id, "commit", "-qam", "side")...)
	gitRun(t, w.folder, "checkout", "-q", w.branch)
	cmd := exec.Command("git", append(id, "merge", "side")...)
	cmd.Dir = w.folder
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("merge did not conflict:\n%s", out)
	}
	resolution := "session and side, by the agent\n"
	if err := os.WriteFile(filepath.Join(w.folder, "clash.txt"), []byte(resolution), 0o644); err != nil {
		t.Fatal(err)
	}
	w.setAgent(t, contracts.AgentIdle)

	tpl := strings.Replace(isolatedTemplate, "command: git merge --no-ff", "command: git merge --no-ff\n    timeout: 500ms", 1)
	seeded := w.seed(contracts.Task{Title: "alpha"})
	f := mustScenario(t, fake.Rule{Match: "Task: alpha", Reply: "done",
		Tools: []fake.ToolStep{{Name: "write_file", Path: "alpha.txt", Content: "alpha\n"}}})
	sum := w.start(t, w.config(t, f, tpl))()
	if len(sum.Done) != 0 || w.task(t, seeded[0].ID).Status == contracts.StatusDone {
		t.Fatalf("merged into a folder mid-merge: %+v", sum)
	}
	if _, err := os.Stat(filepath.Join(w.folder, "alpha.txt")); err == nil {
		t.Error("the task's file reached the folder")
	}
	gitRun(t, w.folder, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	if got, _ := os.ReadFile(filepath.Join(w.folder, "clash.txt")); string(got) != resolution {
		t.Errorf("clash.txt = %q, want the agent's resolution kept", got)
	}
	said := false
	for _, ev := range w.events(t, "stage.failed") {
		said = said || strings.Contains(string(ev.Payload), "mid-merge")
	}
	if !said {
		t.Errorf("no stage.failed saying the folder is mid-merge: %v", w.events(t, "stage.failed"))
	}
}

// A merge into a tree someone else left mid-merge is refused by git, and
// worktrees.merge must not abort the merge it didn't start.
func TestMergeLeavesSomeoneElsesMergeAlone(t *testing.T) {
	h := newHarness(t)
	id := []string{"-c", "user.email=t@t", "-c", "user.name=t"}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(h.cwd, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("clash.txt", "base\n")
	gitRun(t, h.cwd, "add", "clash.txt")
	gitRun(t, h.cwd, append(id, "commit", "-qm", "base")...)
	gitRun(t, h.cwd, "branch", "side")
	gitRun(t, h.cwd, "branch", "task")
	write("clash.txt", "main\n")
	gitRun(t, h.cwd, append(id, "commit", "-qam", "main")...)
	gitRun(t, h.cwd, "checkout", "-q", "side")
	write("clash.txt", "side\n")
	gitRun(t, h.cwd, append(id, "commit", "-qam", "side")...)
	gitRun(t, h.cwd, "checkout", "-q", "-")
	cmd := exec.Command("git", append(id, "merge", "side")...)
	cmd.Dir = h.cwd
	if _, err := cmd.CombinedOutput(); err == nil {
		t.Fatal("merge did not conflict")
	}
	write("clash.txt", "resolved\n")

	wt := &worktrees{primary: h.cwd, root: t.TempDir()}
	head := strings.TrimSpace(gitOut(t, h.cwd, "rev-parse", "HEAD"))
	err := wt.merge(context.Background(), "git merge --no-ff", contracts.Task{ID: "t1", Title: "t"}, "task", head)
	if err == nil {
		t.Fatal("merged on top of someone else's merge")
	}
	var ce *ConflictError
	if errors.As(err, &ce) {
		t.Errorf("reported their conflict as ours: %v", ce.Files)
	}
	gitRun(t, h.cwd, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	if got, _ := os.ReadFile(filepath.Join(h.cwd, "clash.txt")); string(got) != "resolved\n" {
		t.Errorf("clash.txt = %q, want the resolution kept", got)
	}
}
