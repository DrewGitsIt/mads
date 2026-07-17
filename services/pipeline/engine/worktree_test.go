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

// isolatedTemplateWithBuild adds a build-named stage before merge, so the
// post-rebase sanity gate (buildStageCommand) has something to run.
const isolatedTemplateWithBuild = `name: isolated-build
stages:
  - name: executor
    agent: executor
  - name: build
    command: make build
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: make test
    on_fail: { then: block }
`

// runIsolated returns the summary and the worktree root, so tests can
// construct a specific task's worktree path (filepath.Join(root, task.ID))
// to inspect state left behind (e.g. unresolved conflict markers).
func (h *harness) runIsolated(t *testing.T, p *fake.Fake, tpl string, concurrency int) (*Summary, string) {
	t.Helper()
	parsed, verr := template.Check([]byte(tpl), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	root := t.TempDir()
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: p, SessionID: h.sess.ID,
		Template: parsed, Cwd: h.cwd, Concurrency: concurrency,
		StageTimeout: 30 * time.Second,
		Isolated:     true, WorktreeRoot: root, DataURL: h.base,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sum, root
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
	sum, _ := h.runIsolated(t, f, isolatedTemplate, 2)

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
	// Trailers must be REAL trailers (git-interpreted), not text that merely
	// looks like one — a %q-quoted multiline -m once baked literal \n into
	// the merge subject and this assertion is what catches that class of bug.
	trailers := gitOut(t, h.cwd, "log", "--format=%(trailers:key=Task,valueonly)")
	for _, task := range seeded {
		if !strings.Contains(trailers, task.ID) {
			t.Errorf("no commit carries a git-interpreted Task trailer for %s; trailers:\n%s\nfull log:\n%s",
				task.ID, trailers, gitOut(t, h.cwd, "log", "--format=%s%n%b"))
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
// task with the conflicted file (and the trunk task that caused it) named in
// its feedback, the conflict state is left unresolved in the worktree (not
// aborted), and the loser's worktree is kept for inspection.
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
	sum, worktreeRoot := h.runIsolated(t, f, isolatedTemplate, 2)

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
	// "first" is the trunk-side task whose merge landed the conflicting
	// change — the note must name it, not just the conflicted file.
	for _, want := range []string{"shared.txt", "conflict", "worktree is kept", "first"} {
		if !strings.Contains(note.Content, want) {
			t.Errorf("blocker note missing %q:\n%s", want, note.Content)
		}
	}
	// The trunk is clean and holds the winner's version — the conflict never
	// even reached the primary-side git merge.
	if status := gitOut(t, h.cwd, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("trunk dirty after aborted merge:\n%s", status)
	}
	raw, err := os.ReadFile(filepath.Join(h.cwd, "shared.txt"))
	if err != nil || string(raw) != "first version\n" {
		t.Errorf("trunk shared.txt: %q, %v", raw, err)
	}
	// The conflict state was left in the blocked task's own worktree, not
	// aborted: real conflict markers are still there.
	conflicted, err := os.ReadFile(filepath.Join(worktreeRoot, blocked.ID, "shared.txt"))
	if err != nil || !strings.Contains(string(conflicted), "<<<<<<<") {
		t.Errorf("blocked task's worktree shared.txt lacks conflict markers: %q, %v", conflicted, err)
	}
	// The loser's branch survives for manual merge-back.
	if branches := gitOut(t, h.cwd, "branch", "--list", "mads/*"); !strings.Contains(branches, "mads/"+blocked.ID) {
		t.Errorf("blocked task's branch is gone:\n%s", branches)
	}
}

// A stale-but-conflict-free branch (trunk moved while the task worked,
// touching a different file) auto-resolves inside the worktree via
// rebase-before-merge and merges without blocking — "trunk moved under me"
// is a non-event. The build-named stage's sanity gate runs as part of that.
func TestStaleBranchMergesWithoutBlocking(t *testing.T) {
	h := newHarness(t)
	f := mustScenario(t,
		fake.Rule{Match: "Task: first", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "first.txt", Content: "first\n"}}},
		fake.Rule{Match: "Task: second", Reply: "done", DelayMs: 300,
			Tools: []fake.ToolStep{{Name: "write_file", Path: "second.txt", Content: "second\n"}}},
	)
	h.seed(contracts.Task{Title: "first"}, contracts.Task{Title: "second"})
	sum, _ := h.runIsolated(t, f, isolatedTemplateWithBuild, 2)

	if len(sum.Done) != 2 || len(sum.Blocked) != 0 {
		t.Fatalf("summary: %+v", sum)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if _, err := os.Stat(filepath.Join(h.cwd, name)); err != nil {
			t.Errorf("%s missing from merged trunk", name)
		}
	}
	if status := gitOut(t, h.cwd, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("trunk dirty after merges:\n%s", status)
	}
}

// A HANDOFF.md left on a task branch never reaches trunk: the sweep removes
// it immediately before the trunk merge, and the task's real work still
// lands.
func TestHandoffMdSweptBeforeMerge(t *testing.T) {
	h := newHarness(t)
	f := mustScenario(t,
		fake.Rule{Match: "Task: leaves handoff", Reply: "done",
			Tools: []fake.ToolStep{
				{Name: "write_file", Path: "HANDOFF.md", Content: "architect notes\n"},
				{Name: "write_file", Path: "sub/HANDOFF.md", Content: "nested notes\n"},
				{Name: "write_file", Path: "work.txt", Content: "the actual work\n"},
			}},
	)
	seeded := h.seed(contracts.Task{Title: "leaves handoff"})
	sum, _ := h.runIsolated(t, f, isolatedTemplate, 1)

	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
	for _, name := range []string{"HANDOFF.md", "sub/HANDOFF.md"} {
		if _, err := os.Stat(filepath.Join(h.cwd, name)); !os.IsNotExist(err) {
			t.Errorf("%s reached trunk (err: %v)", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(h.cwd, "work.txt")); err != nil {
		t.Errorf("real work file missing from merged trunk: %v", err)
	}
	// The sweep didn't silently eat the merge: a merge commit for the task
	// still exists, carrying its Task trailer.
	trailers := gitOut(t, h.cwd, "log", "--format=%(trailers:key=Task,valueonly)")
	if !strings.Contains(trailers, seeded[0].ID) {
		t.Errorf("no merge commit carries the task trailer for %s; trailers:\n%s", seeded[0].ID, trailers)
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
	sum, _ := h.runIsolated(t, f, isolatedTemplate, 1)

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

// A task title carrying shell metacharacters must never break the merge:
// worktrees.merge composes the merge commit message from task data as
// direct argv, not through a shell (tsk_96de312d665dbdd0).
func TestHostileTaskTitleMergesCleanly(t *testing.T) {
	titles := []struct {
		name  string
		title string
	}{
		{"backtick", "Shell dock keyboard shortcut (Ctrl+`)"},
		{"command_substitution", "Rename $(whoami) helper"},
		{"mixed_quotes", `He said "hello" and it's fine`},
		{"embedded_newline", "Multi\nline title"},
		{"leading_dash", "-rf delete everything"},
	}
	for _, tc := range titles {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			f := mustScenario(t,
				fake.Rule{Match: tc.title, Reply: "done",
					Tools: []fake.ToolStep{
						{Name: "write_file", Path: "HANDOFF.md", Content: "architect notes\n"},
						{Name: "write_file", Path: "work.txt", Content: "the actual work\n"},
					}},
			)
			seeded := h.seed(contracts.Task{Title: tc.title})
			sum, _ := h.runIsolated(t, f, isolatedTemplate, 1)

			if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
				t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
			}
			if _, err := os.Stat(filepath.Join(h.cwd, "work.txt")); err != nil {
				t.Errorf("real work file missing from merged trunk: %v", err)
			}
			if _, err := os.Stat(filepath.Join(h.cwd, "HANDOFF.md")); !os.IsNotExist(err) {
				t.Errorf("HANDOFF.md reached trunk (err: %v)", err)
			}
			if status := gitOut(t, h.cwd, "status", "--porcelain"); strings.TrimSpace(status) != "" {
				t.Errorf("trunk dirty after merge:\n%s", status)
			}
		})
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
