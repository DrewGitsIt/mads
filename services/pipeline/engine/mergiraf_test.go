package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/fake"
)

// mergirafOrSkip locates the mergiraf binary on PATH, or skips the test —
// mergiraf is an external dependency (spec: "detect availability... fall
// back cleanly when absent"), so tests exercising its auto-resolution can't
// hard-require it in every environment. Pinned version for this machine:
// mergiraf 0.18.0, installed via `cargo install --locked mergiraf`.
func mergirafOrSkip(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("mergiraf")
	if err != nil {
		t.Skip("mergiraf not found on PATH; skipping (see mergiraf.org for install)")
	}
	return path
}

// withMergiraf sets Config.MergirafPath for a runIsolated call.
func withMergiraf(path string) func(*Config) {
	return func(cfg *Config) { cfg.MergirafPath = path }
}

const mergirafAdjacentGo = "package sample\n\nfunc Sample() {\n\tx := 1\n\ty := 2\n\t_ = x\n\t_ = y\n}\n"
const mergirafAdjacentGoFirst = "package sample\n\nfunc Sample() {\n\tx := 100\n\ty := 2\n\t_ = x\n\t_ = y\n}\n"
const mergirafAdjacentGoSecond = "package sample\n\nfunc Sample() {\n\tx := 1\n\ty := 200\n\t_ = x\n\t_ = y\n}\n"
const mergirafAdjacentGoWant = "package sample\n\nfunc Sample() {\n\tx := 100\n\ty := 200\n\t_ = x\n\t_ = y\n}\n"

const mergirafAdjacentTS = "export function sample(): void {\n  const x = 1;\n  const y = 2;\n  void x;\n  void y;\n}\n"
const mergirafAdjacentTSFirst = "export function sample(): void {\n  const x = 100;\n  const y = 2;\n  void x;\n  void y;\n}\n"
const mergirafAdjacentTSSecond = "export function sample(): void {\n  const x = 1;\n  const y = 200;\n  void x;\n  void y;\n}\n"
const mergirafAdjacentTSWant = "export function sample(): void {\n  const x = 100;\n  const y = 200;\n  void x;\n  void y;\n}\n"

// seedAdjacentFixture commits the common-ancestor Go and TS files both
// fixture tasks fork from.
func seedAdjacentFixture(t *testing.T, h *harness) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.cwd, "sample.go"), []byte(mergirafAdjacentGo), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.cwd, "sample.ts"), []byte(mergirafAdjacentTS), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, h.cwd, "-c", "user.email=t@t", "-c", "user.name=t", "add", ".")
	gitRun(t, h.cwd, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "seed adjacent-edit fixture")
}

func mergeDriverOf(t *testing.T, ev contracts.Envelope) string {
	t.Helper()
	var payload struct {
		MergeDriver string `json:"merge_driver"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("merge.attempted payload: %v", err)
	}
	return payload.MergeDriver
}

// Acceptance criterion: "two branches editing adjacent regions of the same
// Go and TS files rebase clean through the driver." Both branches fork from
// the same commit and each edits a different, adjacent (non-overlapping)
// line in both files — plain git's 3-way merge conflicts on this (no shared
// context between the edits, but git still refuses without help); mergiraf,
// registered as the merge driver, resolves it structurally.
func TestMergirafResolvesAdjacentEdits(t *testing.T) {
	path := mergirafOrSkip(t)
	h := newHarness(t)
	seedAdjacentFixture(t, h)
	f := mustScenario(t,
		fake.Rule{Match: "Task: first", Reply: "done", Tools: []fake.ToolStep{
			{Name: "write_file", Path: "sample.go", Content: mergirafAdjacentGoFirst},
			{Name: "write_file", Path: "sample.ts", Content: mergirafAdjacentTSFirst},
		}},
		fake.Rule{Match: "Task: second", Reply: "done", DelayMs: 300, Tools: []fake.ToolStep{
			{Name: "write_file", Path: "sample.go", Content: mergirafAdjacentGoSecond},
			{Name: "write_file", Path: "sample.ts", Content: mergirafAdjacentTSSecond},
		}},
	)
	h.seed(contracts.Task{Title: "first"}, contracts.Task{Title: "second"})
	sum, _ := h.runIsolated(t, f, isolatedTemplate, 2, withMergiraf(path))

	if len(sum.Done) != 2 || len(sum.Blocked) != 0 {
		t.Fatalf("summary: %+v", sum)
	}
	goGot, err := os.ReadFile(filepath.Join(h.cwd, "sample.go"))
	if err != nil || string(goGot) != mergirafAdjacentGoWant {
		t.Errorf("sample.go after merge: %q, want %q (err %v)", goGot, mergirafAdjacentGoWant, err)
	}
	tsGot, err := os.ReadFile(filepath.Join(h.cwd, "sample.ts"))
	if err != nil || string(tsGot) != mergirafAdjacentTSWant {
		t.Errorf("sample.ts after merge: %q, want %q (err %v)", tsGot, mergirafAdjacentTSWant, err)
	}
	for _, ev := range h.events(t, "merge.attempted") {
		if got := mergeDriverOf(t, ev); got != "mergiraf" {
			t.Errorf("task %s merge.attempted merge_driver: %q, want mergiraf", ev.TaskID, got)
		}
	}
}

// Acceptance criterion: "two branches editing the same lines still conflict
// and produce the attribution event" — even with mergiraf active, genuinely
// overlapping edits must NOT be silently resolved; mergiraf's cautious
// defaults leave them conflicted and the existing merge.conflicted
// attribution path fires unchanged.
func TestMergirafStillConflictsOnOverlappingEdits(t *testing.T) {
	path := mergirafOrSkip(t)
	h := newHarness(t)
	f := mustScenario(t,
		fake.Rule{Match: "Task: first", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "shared.txt", Content: "first version\n"}}},
		fake.Rule{Match: "Task: second", Reply: "done", DelayMs: 300,
			Tools: []fake.ToolStep{{Name: "write_file", Path: "shared.txt", Content: "second version\n"}}},
	)
	h.seed(contracts.Task{Title: "first"}, contracts.Task{Title: "second"})
	sum, worktreeRoot := h.runIsolated(t, f, isolatedTemplate, 2, withMergiraf(path))

	if len(sum.Done) != 1 || len(sum.Blocked) != 1 {
		t.Fatalf("summary: %+v", sum)
	}
	blocked := h.task(t, sum.Blocked[0])
	conflicted, err := os.ReadFile(filepath.Join(worktreeRoot, blocked.ID, "shared.txt"))
	if err != nil || !strings.Contains(string(conflicted), "<<<<<<<") {
		t.Errorf("blocked task's worktree shared.txt lacks conflict markers: %q, %v", conflicted, err)
	}
	conflicts := h.events(t, "merge.conflicted")
	if len(conflicts) != 1 {
		t.Fatalf("merge.conflicted events: %d, want 1", len(conflicts))
	}
	if conflicts[0].TaskID != blocked.ID {
		t.Errorf("merge.conflicted TaskID: %q, want %q", conflicts[0].TaskID, blocked.ID)
	}
	var attempted *contracts.Envelope
	for _, ev := range h.events(t, "merge.attempted") {
		if ev.TaskID == blocked.ID {
			ev := ev
			attempted = &ev
		}
	}
	if attempted == nil {
		t.Fatal("no merge.attempted event for the blocked task")
	}
	if got := mergeDriverOf(t, *attempted); got != "mergiraf" {
		t.Errorf("merge.attempted merge_driver: %q, want mergiraf", got)
	}
}

// Acceptance criterion: "engine works identically (minus auto-resolutions)
// when the binary is absent; driver choice visible in land events." Forcing
// a nonexistent MergirafPath (rather than relying on PATH absence) makes
// this deterministic regardless of whether mergiraf happens to be installed
// on the machine running the suite: the same adjacent-edit scenario that
// TestMergirafResolvesAdjacentEdits shows resolving cleanly instead
// conflicts under plain git, and the event names "git" as the driver.
func TestMergirafAbsentFallsBackToPlainGit(t *testing.T) {
	h := newHarness(t)
	seedAdjacentFixture(t, h)
	f := mustScenario(t,
		fake.Rule{Match: "Task: first", Reply: "done", Tools: []fake.ToolStep{
			{Name: "write_file", Path: "sample.go", Content: mergirafAdjacentGoFirst},
		}},
		fake.Rule{Match: "Task: second", Reply: "done", DelayMs: 300, Tools: []fake.ToolStep{
			{Name: "write_file", Path: "sample.go", Content: mergirafAdjacentGoSecond},
		}},
	)
	h.seed(contracts.Task{Title: "first"}, contracts.Task{Title: "second"})
	missing := filepath.Join(t.TempDir(), "no-mergiraf-here")
	sum, _ := h.runIsolated(t, f, isolatedTemplate, 2, withMergiraf(missing))

	if len(sum.Done) != 1 || len(sum.Blocked) != 1 {
		t.Fatalf("summary: %+v, want plain git to conflict without mergiraf", sum)
	}
	for _, ev := range h.events(t, "merge.attempted") {
		if got := mergeDriverOf(t, ev); got != "git" {
			t.Errorf("task %s merge.attempted merge_driver: %q, want git", ev.TaskID, got)
		}
	}
}

// Regression: ensureMergirafDriver used to join `git rev-parse
// --git-common-dir`'s output onto w.primary unconditionally. That's only
// correct when w.primary is a normal checkout, where --git-common-dir prints
// a path relative to it (e.g. ".git"). When w.primary is ITSELF a linked
// worktree — e.g. the pipeline's own workspace was created via `git worktree
// add`, pointing at a main checkout elsewhere — --git-common-dir prints an
// ABSOLUTE path, and joining that onto w.primary silently produced a bogus
// nested path nobody ever reads: registration no-op'd without returning an
// error, leaving the merge.attempted event's merge_driver field claiming
// "mergiraf" while git never actually ran it.
func TestEnsureMergirafDriverInLinkedWorktreeTopology(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "main")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, main, "init", "-q", "-b", "main")
	gitRun(t, main, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	linked := filepath.Join(root, "linked")
	gitRun(t, main, "worktree", "add", "-q", "-b", "feature", linked)

	w := &worktrees{primary: linked, mergirafPath: "mergiraf"}
	if err := w.ensureMergirafDriver(context.Background()); err != nil {
		t.Fatalf("ensureMergirafDriver: %v", err)
	}

	// The attributes file must land in the REAL common dir (main/.git/info),
	// not get lost under a bogus double-joined path.
	attrPath := filepath.Join(main, ".git", "info", "attributes")
	raw, err := os.ReadFile(attrPath)
	if err != nil {
		t.Fatalf("attributes file missing at the real common dir %s: %v", attrPath, err)
	}
	if !strings.Contains(string(raw), "* merge=mergiraf") {
		t.Errorf("attributes file %s missing merge=mergiraf rule: %q", attrPath, raw)
	}

	// The double-join bug's exact symptom: linked/<main>/.git/info.
	bogus := filepath.Join(linked, main, ".git", "info")
	if _, err := os.Stat(bogus); !os.IsNotExist(err) {
		t.Errorf("bogus double-joined path exists (the regression this test guards against): %s", bogus)
	}

	driver := gitOut(t, linked, "config", "--get", "merge.mergiraf.driver")
	if !strings.Contains(driver, "mergiraf merge --git") {
		t.Errorf("merge.mergiraf.driver: %q", driver)
	}
}
