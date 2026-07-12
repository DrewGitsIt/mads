package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
)

// isolatedTemplate is the W3.2 shape: implement in the worktree, merge back,
// verify the merged tree.
const isolatedTemplate = `name: isolated
stages:
  - name: executor
    agent: executor
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: make test
    on_fail: { then: block }
`

func (h *harness) runIsolated(t *testing.T, p *fake.Fake, tpl string, concurrency int) *Summary {
	t.Helper()
	parsed, verr := template.Check([]byte(tpl), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: p, SessionID: h.sess.ID,
		Template: parsed, Cwd: h.cwd, Concurrency: concurrency,
		StageTimeout: 30 * time.Second,
		Isolated:     true, WorktreeRoot: t.TempDir(), DataURL: h.base,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func mustScenario(t *testing.T, rules ...fake.Rule) *fake.Fake {
	t.Helper()
	f, err := fake.NewWithScenario(fake.Scenario{Name: "wt", Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// The W3.2 done criterion, first half: two independent tasks, each doing its
// work in its own worktree, merge cleanly back into the primary in turn —
// and the worktrees and branches are gone afterward.
func TestIndependentTasksMergeCleanly(t *testing.T) {
	h := newHarness(t)
	f := mustScenario(t,
		fake.Rule{Match: "Task: alpha", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "alpha.txt", Content: "alpha\n"}}},
		fake.Rule{Match: "Task: beta", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "beta.txt", Content: "beta\n"}}},
	)
	seeded := h.seed(contracts.Task{Title: "alpha"}, contracts.Task{Title: "beta"})
	sum := h.runIsolated(t, f, isolatedTemplate, 2)

	if len(sum.Done) != 2 {
		t.Fatalf("summary: %+v; tasks: %+v %+v", sum, h.task(t, seeded[0].ID), h.task(t, seeded[1].ID))
	}
	// Both tasks' work landed on the primary trunk.
	for _, name := range []string{"alpha.txt", "beta.txt"} {
		if _, err := os.Stat(filepath.Join(h.cwd, name)); err != nil {
			t.Errorf("%s missing from merged trunk", name)
		}
	}
	// The trunk is clean and the merges are --no-ff merge commits carrying
	// task trailers (the auto-commit + engine merge message).
	status := gitOut(t, h.cwd, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Errorf("trunk dirty after merges:\n%s", status)
	}
	log := gitOut(t, h.cwd, "log", "--format=%s%n%b")
	for _, task := range seeded {
		if !strings.Contains(log, "Task: "+task.ID) {
			t.Errorf("no commit carries trailer for %s:\n%s", task.ID, log)
		}
	}
	// Worktrees and branches cleaned up.
	if wt := gitOut(t, h.cwd, "worktree", "list"); strings.Contains(wt, "mads/tsk") {
		t.Errorf("worktrees survived cleanup:\n%s", wt)
	}
	if branches := gitOut(t, h.cwd, "branch", "--list", "mads/*"); strings.TrimSpace(branches) != "" {
		t.Errorf("task branches survived cleanup:\n%s", branches)
	}
}

// The done criterion, second half: a same-lines conflict blocks the second
// task with the conflicted file named in its feedback, the merge is aborted
// (trunk stays clean), and the loser's worktree is kept for inspection.
func TestConflictingPairBlocksSecond(t *testing.T) {
	h := newHarness(t)
	// Both tasks must branch from the SAME base for their same-file writes
	// to conflict — run them concurrently (a sequential second task would
	// branch after the first merge and rewrite the file cleanly). The delay
	// makes "first" merge first, deterministically.
	f := mustScenario(t,
		fake.Rule{Match: "Task: first", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "shared.txt", Content: "first version\n"}}},
		fake.Rule{Match: "Task: second", Reply: "done", DelayMs: 300,
			Tools: []fake.ToolStep{{Name: "write_file", Path: "shared.txt", Content: "second version\n"}}},
	)
	h.seed(contracts.Task{Title: "first"}, contracts.Task{Title: "second"})
	sum := h.runIsolated(t, f, isolatedTemplate, 2)

	if len(sum.Done) != 1 || len(sum.Blocked) != 1 {
		t.Fatalf("summary: %+v", sum)
	}
	blocked := h.task(t, sum.Blocked[0])
	if blocked.Title != "second" || blocked.Status != contracts.StatusBlocked {
		t.Fatalf("blocked task: %+v", blocked)
	}
	var note *contracts.Note
	for i := range blocked.Notes {
		if blocked.Notes[i].Type == contracts.NoteBlocker {
			note = &blocked.Notes[i]
		}
	}
	if note == nil {
		t.Fatal("no blocker note on the conflicting task")
	}
	for _, want := range []string{"shared.txt", "conflict", "worktree is kept"} {
		if !strings.Contains(note.Content, want) {
			t.Errorf("blocker note missing %q:\n%s", want, note.Content)
		}
	}
	// The aborted merge left the trunk clean, holding the winner's version.
	if status := gitOut(t, h.cwd, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("trunk dirty after aborted merge:\n%s", status)
	}
	raw, err := os.ReadFile(filepath.Join(h.cwd, "shared.txt"))
	if err != nil || string(raw) != "first version\n" {
		t.Errorf("trunk shared.txt: %q, %v", raw, err)
	}
	// The loser's branch survives for manual merge-back.
	if branches := gitOut(t, h.cwd, "branch", "--list", "mads/*"); !strings.Contains(branches, "mads/"+blocked.ID) {
		t.Errorf("blocked task's branch is gone:\n%s", branches)
	}
}

// A quiet conflict — both merges clean, but the combination breaks — is
// caught by the verify stage on the merged tree.
func TestVerifyCatchesQuietConflict(t *testing.T) {
	h := newHarness(t)
	// The primary's test rule fails once more than one .marker file exists:
	// each task is fine alone, together they break.
	makefile := "build:\n\t@true\ntest:\n\t@test $$(ls *.marker 2>/dev/null | wc -l) -le 1\n"
	if err := os.WriteFile(filepath.Join(h.cwd, "Makefile"), []byte(makefile), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, h.cwd, "add", "Makefile")
	gitRun(t, h.cwd, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "tighten test")

	f := mustScenario(t,
		fake.Rule{Match: "Task: one", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "one.marker", Content: "1"}}},
		fake.Rule{Match: "Task: two", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "two.marker", Content: "2"}}},
	)
	seeded := h.seed(contracts.Task{Title: "one"}, contracts.Task{Title: "two"})
	sum := h.runIsolated(t, f, isolatedTemplate, 1)

	if len(sum.Done) != 1 || len(sum.Blocked) != 1 {
		t.Fatalf("summary: %+v; tasks: %+v %+v", sum, h.task(t, seeded[0].ID), h.task(t, seeded[1].ID))
	}
	blocked := h.task(t, sum.Blocked[0])
	found := false
	for _, n := range blocked.Notes {
		if n.Type == contracts.NoteBlocker && strings.Contains(n.Content, "verify") {
			found = true
		}
	}
	if !found {
		t.Errorf("blocked without a verify blocker note: %+v", blocked.Notes)
	}
}

// The installed post-commit hook reports commits from a task worktree to the
// Data Service with the task trailer, and the linkage chain resolves it
// (spec §5). curl talks to the real datatest server.
func TestPostCommitHookReportsCommit(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl on PATH (the hook depends on it)")
	}
	h := newHarness(t)
	seeded := h.seed(contracts.Task{Title: "hooked"})
	w := &worktrees{primary: h.cwd, root: t.TempDir(), dataURL: h.base, sessionID: h.sess.ID}
	dir, _, err := w.create(context.Background(), seeded[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m",
		"do the work\n\nTask: "+seeded[0].ID)

	commits := struct {
		list []contracts.Commit
	}{}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := hGet(h, "/sessions/"+h.sess.ID+"/commits", &commits.list); err == nil && len(commits.list) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(commits.list) != 1 {
		t.Fatalf("hook never reported the commit")
	}
	got := commits.list[0]
	if got.TaskID != seeded[0].ID || got.LinkedVia != contracts.LinkedViaTrailer {
		t.Errorf("linkage: %+v", got)
	}
	if got.Message != "do the work" {
		t.Errorf("message: %q", got.Message)
	}
}

// hGet is a bare GET against the Data Service for endpoints the engine's
// client doesn't wrap.
func hGet(h *harness, path string, out any) error {
	resp, err := http.Get(h.base + path)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return json.NewDecoder(resp.Body).Decode(out)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = gitOut(t, dir, args...)
}
