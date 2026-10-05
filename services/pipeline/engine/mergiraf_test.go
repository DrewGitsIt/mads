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

// mergirafOrSkip locates the mergiraf binary — $MADS_TEST_MERGIRAF, else
// PATH — or skips the test: mergiraf is an optional runtime tool (spec:
// "detect availability... fall back cleanly when absent"), so tests
// exercising its auto-resolution can't hard-require it in every
// environment. Install it with `brew install mergiraf` or the release
// binary from https://codeberg.org/mergiraf/mergiraf/releases.
func mergirafOrSkip(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MADS_TEST_MERGIRAF"); p != "" {
		if _, err := exec.LookPath(p); err != nil {
			t.Fatalf("MADS_TEST_MERGIRAF=%s: %v", p, err)
		}
		return p
	}
	path, err := exec.LookPath("mergiraf")
	if err != nil {
		t.Skip("mergiraf not found (set MADS_TEST_MERGIRAF or put it on PATH); skipping")
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

// mergirafRepoState is everything a merge-driver registration could write
// to the repo: any merge.* config (the hook setup writes other config) and
// the shared attributes file.
func mergirafRepoState(t *testing.T, repo string) string {
	t.Helper()
	cfg, _ := exec.Command("git", "-C", repo, "config", "--local", "--get-regexp", `^merge\.`).Output()
	attrs, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "attributes"))
	return string(cfg) + "\x00" + string(attrs)
}

// The pipeline's merges go through mergiraf as per-command flags only: the
// repo's config and attributes are the same after the run, and the user's
// own `git merge` of the same kind of adjacent edits is plain git's.
func TestMergirafLeavesRepoStateAlone(t *testing.T) {
	path := mergirafOrSkip(t)
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
	before := mergirafRepoState(t, h.cwd)
	sum, _ := h.runIsolated(t, f, isolatedTemplate, 2, withMergiraf(path))
	if len(sum.Done) != 2 || len(sum.Blocked) != 0 {
		t.Fatalf("summary: %+v, want mergiraf to have merged both", sum)
	}
	if after := mergirafRepoState(t, h.cwd); after != before {
		t.Errorf("the run wrote merge-driver state to the repo:\n%q\nwas\n%q", after, before)
	}

	git := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		cmd.Dir = h.cwd
		return cmd.Run()
	}
	if err := os.WriteFile(filepath.Join(h.cwd, "user.go"), []byte(mergirafAdjacentGo), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, h.cwd, "-c", "user.email=t@t", "-c", "user.name=t", "add", "user.go")
	gitRun(t, h.cwd, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "user base")
	gitRun(t, h.cwd, "checkout", "-q", "-b", "user-side")
	if err := os.WriteFile(filepath.Join(h.cwd, "user.go"), []byte(mergirafAdjacentGoFirst), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, h.cwd, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-am", "user side")
	gitRun(t, h.cwd, "checkout", "-q", "-")
	if err := os.WriteFile(filepath.Join(h.cwd, "user.go"), []byte(mergirafAdjacentGoSecond), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, h.cwd, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-am", "trunk side")
	if err := git("merge", "-q", "--no-edit", "user-side"); err == nil {
		t.Error("the user's own git merge resolved adjacent edits: it went through mergiraf")
	}
	_ = git("merge", "--abort")
}

// What an earlier build registered in the shared repo is removed at setup
// — including when the workspace is itself a linked worktree, where
// --git-common-dir is ABSOLUTE and joining it onto the worktree path
// would name a bogus nested path (the regression the old registration
// test guarded against). The user's own attributes lines stay.
func TestEnsureSetupCleansLegacyRegistrationInLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "main")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, main, "init", "-q", "-b", "main")
	gitRun(t, main, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	gitRun(t, main, "config", "merge.mergiraf.name", "mergiraf")
	gitRun(t, main, "config", "merge.mergiraf.driver", "/opt/homebrew/bin/mergiraf merge --git %O %A %B -s %S -x %X -y %Y -p %P -l %L")
	attrPath := filepath.Join(main, ".git", "info", "attributes")
	if err := os.WriteFile(attrPath, []byte("*.png binary\n* merge=mergiraf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	gitRun(t, main, "worktree", "add", "-q", "-b", "feature", linked)

	w := &worktrees{primary: linked, root: filepath.Join(root, "wt"), mergirafPath: "mergiraf"}
	if err := w.ensureSetup(context.Background()); err != nil {
		t.Fatalf("ensureSetup: %v", err)
	}
	if raw, _ := os.ReadFile(attrPath); string(raw) != "*.png binary\n" {
		t.Errorf("attributes at the real common dir = %q, want the user's line only", raw)
	}
	if out, err := exec.Command("git", "-C", linked, "config", "--get", "merge.mergiraf.driver").Output(); err == nil {
		t.Errorf("merge.mergiraf.driver still set: %q", out)
	}
	bogus := filepath.Join(linked, main, ".git", "info")
	if _, err := os.Stat(bogus); !os.IsNotExist(err) {
		t.Errorf("bogus double-joined path exists: %s", bogus)
	}
}
