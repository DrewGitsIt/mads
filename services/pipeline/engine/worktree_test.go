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
func (h *harness) runIsolated(t *testing.T, p *fake.Fake, tpl string, concurrency int, opts ...func(*Config)) (*Summary, string) {
	t.Helper()
	parsed, verr := template.Check([]byte(tpl), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	root := t.TempDir()
	cfg := Config{
		Data: h.data, Provider: p, SessionID: h.sess.ID,
		Template: parsed, Cwd: h.cwd, Concurrency: concurrency,
		StageTimeout: 30 * time.Second,
		Isolated:     true, WorktreeRoot: root, DataURL: h.base,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	sum, err := Run(context.Background(), cfg)
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

	// Fork SHA recorded at worktree creation is visible in the land
	// attempt's own events (merge.attempted and merge.completed), per task.
	for _, task := range seeded {
		var attempted, completed *contracts.Envelope
		for _, ev := range h.events(t, "merge.attempted") {
			if ev.TaskID == task.ID {
				attempted = &ev
			}
		}
		for _, ev := range h.events(t, "merge.completed") {
			if ev.TaskID == task.ID {
				completed = &ev
			}
		}
		if attempted == nil || completed == nil {
			t.Fatalf("task %s: merge.attempted=%v merge.completed=%v", task.ID, attempted, completed)
		}
		var attemptedPayload, completedPayload struct {
			ForkSHA   string `json:"fork_sha"`
			TrunkHead string `json:"trunk_head"`
		}
		if err := json.Unmarshal(attempted.Payload, &attemptedPayload); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(completed.Payload, &completedPayload); err != nil {
			t.Fatal(err)
		}
		if attemptedPayload.ForkSHA == "" {
			t.Errorf("task %s: merge.attempted fork_sha empty", task.ID)
		}
		if completedPayload.ForkSHA != attemptedPayload.ForkSHA {
			t.Errorf("task %s: fork_sha differs between merge.attempted (%s) and merge.completed (%s)",
				task.ID, attemptedPayload.ForkSHA, completedPayload.ForkSHA)
		}
		if completedPayload.TrunkHead == "" {
			t.Errorf("task %s: merge.completed trunk_head empty", task.ID)
		}
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

	// merge.conflicted names both task ids (winner via attribution, loser as
	// the event's own TaskID), the conflicted file, and at least one hunk —
	// the structured attribution a future merge_resolve stage and the Merge
	// Queue UI consume (spec: "acceptance criterion, not a nice-to-have").
	winner := h.task(t, sum.Done[0])
	conflicts := h.events(t, "merge.conflicted")
	if len(conflicts) != 1 {
		t.Fatalf("merge.conflicted events: %d, want 1", len(conflicts))
	}
	var payload struct {
		Branch        string         `json:"branch"`
		Files         []string       `json:"files"`
		Hunks         []ConflictHunk `json:"hunks"`
		LandedTaskIDs []string       `json:"landed_task_ids"`
		ForkSHA       string         `json:"fork_sha"`
		TrunkHead     string         `json:"trunk_head"`
	}
	if err := json.Unmarshal(conflicts[0].Payload, &payload); err != nil {
		t.Fatalf("merge.conflicted payload: %v", err)
	}
	if conflicts[0].TaskID != blocked.ID {
		t.Errorf("merge.conflicted TaskID: %q, want the blocked task %q", conflicts[0].TaskID, blocked.ID)
	}
	if len(payload.Files) != 1 || payload.Files[0] != "shared.txt" {
		t.Errorf("merge.conflicted files: %v, want [shared.txt]", payload.Files)
	}
	if len(payload.LandedTaskIDs) != 1 || payload.LandedTaskIDs[0] != winner.ID {
		t.Errorf("merge.conflicted landed_task_ids: %v, want [%s]", payload.LandedTaskIDs, winner.ID)
	}
	if len(payload.Hunks) == 0 {
		t.Errorf("merge.conflicted hunks: empty, want at least one")
	}
	if payload.ForkSHA == "" {
		t.Errorf("merge.conflicted fork_sha: empty")
	}
	if payload.TrunkHead == "" {
		t.Errorf("merge.conflicted trunk_head: empty")
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
	var note *contracts.Note
	for i := range blocked.Notes {
		if blocked.Notes[i].Type == contracts.NoteBlocker && strings.Contains(blocked.Notes[i].Content, "verify") {
			note = &blocked.Notes[i]
		}
	}
	if note == nil {
		t.Fatalf("blocked without a verify blocker note: %+v", blocked.Notes)
	}
	// The gate ran post-rebase, before landing (Not Rocket Science Rule): the
	// suspect set is explicit rather than requiring bisection. Concurrency 1
	// here means "two" forked after "one" already landed, so trunk hasn't
	// moved since — the regression is fully attributable to "two"'s own
	// change combined with the trunk state it forked from.
	for _, want := range []string{"gates passed at fork", "trunk unchanged since fork"} {
		if !strings.Contains(note.Content, want) {
			t.Errorf("blocker note missing %q:\n%s", want, note.Content)
		}
	}
	// The bug this redesign fixes: the gate must never let a quiet conflict
	// reach trunk first. Trunk still holds only "one"'s landed work — "two"'s
	// merge never happened because its post-rebase gate failed first.
	if _, err := os.Stat(filepath.Join(h.cwd, "two.marker")); !os.IsNotExist(err) {
		t.Errorf("two.marker reached trunk despite the failed gate (err: %v)", err)
	}
	if status := gitOut(t, h.cwd, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("trunk dirty after blocked land attempt:\n%s", status)
	}
}

// The other half of the quiet-conflict suspect set: at concurrency 2 both
// tasks fork from the SAME trunk state, so the second one's land attempt
// really does rebase over trunk movement (the first task's own land) between
// its fork and its gate — the block note must name that task explicitly
// rather than the "trunk unchanged since fork" variant above.
func TestVerifyCatchesQuietConflictNamesSuspectSet(t *testing.T) {
	h := newHarness(t)
	makefile := "build:\n\t@true\ntest:\n\t@test $$(ls *.marker 2>/dev/null | wc -l) -le 1\n"
	if err := os.WriteFile(filepath.Join(h.cwd, "Makefile"), []byte(makefile), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, h.cwd, "add", "Makefile")
	gitRun(t, h.cwd, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "tighten test")

	f := mustScenario(t,
		fake.Rule{Match: "Task: one", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "one.marker", Content: "1"}}},
		fake.Rule{Match: "Task: two", Reply: "done", DelayMs: 300,
			Tools: []fake.ToolStep{{Name: "write_file", Path: "two.marker", Content: "2"}}},
	)
	seeded := h.seed(contracts.Task{Title: "one"}, contracts.Task{Title: "two"})
	sum, worktreeRoot := h.runIsolated(t, f, isolatedTemplate, 2)

	if len(sum.Done) != 1 || len(sum.Blocked) != 1 {
		t.Fatalf("summary: %+v; tasks: %+v %+v", sum, h.task(t, seeded[0].ID), h.task(t, seeded[1].ID))
	}
	winner := h.task(t, sum.Done[0])
	blocked := h.task(t, sum.Blocked[0])
	if blocked.Title != "two" {
		t.Fatalf("expected the delayed task to lose the race and block, got %q", blocked.Title)
	}
	// Direct proof the gate never gates on a stale/cached trunk: "two"'s own
	// worktree (kept, since it's blocked) shows BOTH markers — its post-rebase
	// gate really did see "one"'s already-landed change, not just its own
	// work in isolation.
	if _, err := os.Stat(filepath.Join(worktreeRoot, blocked.ID, "one.marker")); err != nil {
		t.Errorf("blocked task's worktree lacks the winner's rebased-in change: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreeRoot, blocked.ID, "two.marker")); err != nil {
		t.Errorf("blocked task's worktree lacks its own change: %v", err)
	}
	var note *contracts.Note
	for i := range blocked.Notes {
		if blocked.Notes[i].Type == contracts.NoteBlocker && strings.Contains(blocked.Notes[i].Content, "verify") {
			note = &blocked.Notes[i]
		}
	}
	if note == nil {
		t.Fatalf("blocked without a verify blocker note: %+v", blocked.Notes)
	}
	for _, want := range []string{"gates passed at fork", "failed after rebasing over", winner.Title} {
		if !strings.Contains(note.Content, want) {
			t.Errorf("blocker note missing %q:\n%s", want, note.Content)
		}
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

// The gate ("verify") stage's command runs inside the merge stage's land
// attempt, post-rebase — but its own on_fail (loop_back included) must still
// govern what happens when the command fails; the merge stage's on_fail is a
// different, unrelated route. Silently ignoring the gate's own on_fail would
// leave a template author's "loop back to fix it" configuration dead —
// exactly the mismatch between what the template editor shows and what the
// engine does that the redesign must not introduce.
func TestGateFailureHonorsVerifyOwnOnFailLoopBack(t *testing.T) {
	h := newHarness(t)
	makefile := "build:\n\t@true\ntest:\n\t@grep -q PASS status.txt\n"
	if err := os.WriteFile(filepath.Join(h.cwd, "Makefile"), []byte(makefile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.cwd, "status.txt"), []byte("PASS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, h.cwd, "add", "Makefile", "status.txt")
	gitRun(t, h.cwd, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "seed gate")

	const tpl = `name: gate-loop
stages:
  - name: executor
    agent: executor
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: make test
    on_fail: { loop_back: executor, max_loops: 2 }
`
	f := mustScenario(t,
		// Checked first: only matches once a loop-back retry's feedback (the
		// gate's own suspect-set message) has landed in the prompt.
		fake.Rule{Match: "gates passed at fork", Reply: "fixed it\nSTAGE: pass",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "status.txt", Content: "PASS\n"}}},
		fake.Rule{Match: "Pipeline stage: executor\nTask: gate-loop-task", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "status.txt", Content: "FAIL\n"}}},
	)
	seeded := h.seed(contracts.Task{Title: "gate-loop-task"})
	sum, _ := h.runIsolated(t, f, tpl, 1)

	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}

	var gateFails, gateStarts, mergeFails int
	for _, ev := range h.events(t, "stage.failed") {
		if ev.TaskID != seeded[0].ID {
			continue
		}
		switch {
		case strings.Contains(string(ev.Payload), `"stage":"verify"`):
			gateFails++
		case strings.Contains(string(ev.Payload), `"stage":"merge"`):
			mergeFails++
		}
	}
	for _, ev := range h.events(t, "stage.started") {
		if ev.TaskID == seeded[0].ID && strings.Contains(string(ev.Payload), `"stage":"verify"`) {
			gateStarts++
		}
	}
	if gateFails != 1 {
		t.Errorf("stage.failed for verify: %d, want 1 (the first gate run, before the loop-back retry)", gateFails)
	}
	if gateStarts != 2 {
		t.Errorf("stage.started for verify: %d, want 2 (initial failure + retry after loop-back)", gateStarts)
	}
	// The gate failure must route through the gate's own on_fail, never the
	// merge stage's — and never get double-reported under the merge stage's
	// name.
	if mergeFails != 0 {
		t.Errorf("stage.failed misattributed to the merge stage: %d, want 0", mergeFails)
	}
}

// The gate ("verify") stage's `when:` guard must be honored by the land
// attempt exactly as the normal stage loop would honor it — landAttempt runs
// the gate command directly instead of going through the per-stage loop, and
// a prior pass ran it unconditionally regardless of `when:`, which left the
// template editor's "when" field on a verify stage live in appearance but
// dead in effect. Here the guard evaluates false for the task, so the gate
// command (which would fail — there's no Makefile) must be skipped, not run.
func TestGateHonorsOwnWhenGuard(t *testing.T) {
	h := newHarness(t)
	const tpl = `name: gate-when
stages:
  - name: executor
    agent: executor
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: make test
    when: contains(labels, "needs-verify")
`
	f := mustScenario(t,
		fake.Rule{Match: "Pipeline stage: executor", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "gated.txt", Content: "gated\n"}}},
	)
	seeded := h.seed(contracts.Task{Title: "gate-when-task"})
	sum, _ := h.runIsolated(t, f, tpl, 1)

	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
	if _, err := os.Stat(filepath.Join(h.cwd, "gated.txt")); err != nil {
		t.Errorf("gated.txt missing from merged trunk: %v", err)
	}

	var gateSkipped, gateStarted bool
	for _, ev := range h.events(t, "stage.skipped") {
		if ev.TaskID == seeded[0].ID && strings.Contains(string(ev.Payload), `"stage":"verify"`) {
			gateSkipped = true
		}
	}
	for _, ev := range h.events(t, "stage.started") {
		if ev.TaskID == seeded[0].ID && strings.Contains(string(ev.Payload), `"stage":"verify"`) {
			gateStarted = true
		}
	}
	if !gateSkipped {
		t.Errorf("no stage.skipped for verify; the gate's when: guard was not evaluated")
	}
	if gateStarted {
		t.Errorf("verify stage.started fired; the gate ran despite its when: guard evaluating false")
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
	// The commit was made in the task worktree (dir), distinct from the
	// primary (w.primary/h.cwd) — Repo must resolve back to the primary,
	// proving --git-common-dir reaches through the linked worktree.
	wantRepo, err := filepath.EvalSymlinks(w.primary)
	if err != nil {
		t.Fatal(err)
	}
	gotRepo, err := filepath.EvalSymlinks(got.Repo)
	if err != nil {
		t.Fatalf("Repo %q does not resolve: %v", got.Repo, err)
	}
	if gotRepo != wantRepo {
		t.Errorf("Repo = %q (resolved %q), want %q", got.Repo, gotRepo, wantRepo)
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
