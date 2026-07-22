package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/fake"
)

// dirtyGofmtSource is syntactically valid Go with an unsorted import block —
// real gofmt (not a script standing in for it) both flags it via `gofmt -l`
// and fixes it via `gofmt -w`, confirmed empirically before writing this
// test.
const dirtyGofmtSource = `package pkg

import (
	"fmt"
	"strings"
	"os"
)

func F() { fmt.Println(os.Args, strings.ToUpper("f")) }
`

// The land gate ("verify") runs real gofmt over the merged tree. Two
// individually-unremarkable tasks — "one" lands first, "two" forks from the
// same base but is delayed so its land attempt genuinely rebases over
// "one"'s already-landed change — combine such that "two"'s own gofmt-dirty
// file is what the post-rebase gate catches. The mechanical autofix pass
// (real `gofmt -w`, whitelisted) fixes it, commits it as its own labeled
// commit, and the full gate reruns green: the task lands with both its own
// work and the autofix commit present.
func TestLintAutofixLandsAfterGofmtCombination(t *testing.T) {
	h := newHarness(t)
	const tpl = `name: gate-lint-autofix
stages:
  - name: executor
    agent: executor
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: 'out=$(gofmt -l .); if [ -n "$out" ]; then for f in $out; do echo "$f:1:1: File is not gofmt-ed with -s (gofmt)"; done; exit 1; fi'
    on_fail: { then: block }
`
	f := mustScenario(t,
		fake.Rule{Match: "Task: one", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "one.txt", Content: "1\n"}}},
		fake.Rule{Match: "Task: two", Reply: "done", DelayMs: 300,
			Tools: []fake.ToolStep{{Name: "write_file", Path: "dirty.go", Content: dirtyGofmtSource}}},
	)
	seeded := h.seed(contracts.Task{Title: "one"}, contracts.Task{Title: "two"})
	sum, _ := h.runIsolated(t, f, tpl, 2)

	if len(sum.Done) != 2 || len(sum.Blocked) != 0 {
		t.Fatalf("summary: %+v; tasks: %+v %+v", sum, h.task(t, seeded[0].ID), h.task(t, seeded[1].ID))
	}
	two := h.task(t, seeded[1].ID)

	var autofixed *contracts.Envelope
	for _, ev := range h.events(t, "land.autofixed") {
		if ev.TaskID == two.ID {
			autofixed = &ev
		}
	}
	if autofixed == nil {
		t.Fatalf("no land.autofixed event for %s", two.ID)
	}
	var payload struct {
		Fixers      []string `json:"fixers"`
		DiffSummary string   `json:"diff_summary"`
		Stage       string   `json:"stage"`
	}
	if err := json.Unmarshal(autofixed.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, fx := range payload.Fixers {
		if fx == "gofmt" {
			found = true
		}
	}
	if !found {
		t.Errorf("land.autofixed fixers: %v, want gofmt present", payload.Fixers)
	}
	if payload.DiffSummary == "" {
		t.Error("land.autofixed diff_summary empty")
	}
	if payload.Stage != "verify" {
		t.Errorf("land.autofixed stage: %q, want verify", payload.Stage)
	}

	// The autofix commit is present on trunk, clearly labeled, distinct from
	// the task's own work.
	subjects := gitOut(t, h.cwd, "log", "--format=%s")
	if !strings.Contains(subjects, "land: mechanical lint fixes") {
		t.Errorf("no labeled autofix commit in trunk log:\n%s", subjects)
	}

	// The merged tree really is gofmt-clean now — the fix wasn't just
	// reported, it converged.
	if out := gitOut(t, h.cwd, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Errorf("trunk dirty after autofixed land:\n%s", out)
	}
	raw, err := os.ReadFile(filepath.Join(h.cwd, "dirty.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\"fmt\"\n\t\"os\"\n\t\"strings\"") {
		t.Errorf("dirty.go on trunk wasn't actually reformatted:\n%s", raw)
	}
}

// An errcheck-class (or any non-whitelisted-linter) post-rebase gate failure
// never gets a mechanical autofix attempt at all — "unused after merge" can
// mean the merge dropped a caller, and that's information for a human, not
// noise. It blocks immediately with the lint evidence, and — being
// deterministic, not flaky — never triggers the existing flake-retry path
// either.
func TestLintAutofixSkippedForNonWhitelistedLinter(t *testing.T) {
	h := newHarness(t)
	const tpl = `name: gate-lint-errcheck
stages:
  - name: executor
    agent: executor
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: 'echo "foo.go:3:2: Error return value is not checked (errcheck)"; exit 1'
    on_fail: { then: block }
`
	f := mustScenario(t,
		fake.Rule{Match: "Task: errcheck-task", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "work.txt", Content: "work\n"}}},
	)
	seeded := h.seed(contracts.Task{Title: "errcheck-task"})
	sum, _ := h.runIsolated(t, f, tpl, 1)

	if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
	if n := len(h.events(t, "land.autofixed")); n != 0 {
		t.Errorf("land.autofixed fired for a non-whitelisted-linter failure: %d", n)
	}
	if n := len(h.events(t, "land.retried")); n != 0 {
		t.Errorf("land.retried fired for a deterministic lint failure: %d", n)
	}

	blocked := h.task(t, seeded[0].ID)
	var note *contracts.Note
	for i := range blocked.Notes {
		if blocked.Notes[i].Type == contracts.NoteBlocker && strings.Contains(blocked.Notes[i].Content, "verify") {
			note = &blocked.Notes[i]
		}
	}
	if note == nil {
		t.Fatalf("blocked without a verify blocker note: %+v", blocked.Notes)
	}
	for _, want := range []string{"classification=lint", "errcheck", "deterministic, never flake-retried"} {
		if !strings.Contains(note.Content, want) {
			t.Errorf("blocker note missing %q:\n%s", want, note.Content)
		}
	}
}

// The mechanical autofix pass is bounded to exactly one attempt per land
// attempt: a real gofmt-dirty file gets genuinely fixed and committed once
// (land.autofixed fires once), but a gate that keeps reporting a
// whitelisted-linter failure regardless (never actually converging) falls
// through to a block rather than looping — proven by a counter file showing
// the gate ran exactly twice (the original failure + the one post-autofix
// rerun), never a third time.
func TestLintAutofixBoundedFallsThroughToBlock(t *testing.T) {
	h := newHarness(t)
	const tpl = `name: gate-lint-nonconvergent
stages:
  - name: executor
    agent: executor
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: 'c=$(cat count 2>/dev/null || echo 0); c=$((c+1)); echo $c > count; for f in $(gofmt -l .); do echo "$f:1:1: File is not gofmt-ed with -s (gofmt)"; done; echo "residual.go:1:1: still not converged (gofmt)"; exit 1'
    on_fail: { then: block }
`
	f := mustScenario(t,
		fake.Rule{Match: "Task: nonconvergent-task", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "dirty.go", Content: dirtyGofmtSource}}},
	)
	seeded := h.seed(contracts.Task{Title: "nonconvergent-task"})
	sum, worktreeRoot := h.runIsolated(t, f, tpl, 1)

	if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
	if n := len(h.events(t, "land.autofixed")); n != 1 {
		t.Errorf("land.autofixed events: %d, want exactly 1 (bounded)", n)
	}
	if n := len(h.events(t, "land.retried")); n != 0 {
		t.Errorf("land.retried fired for a lint failure: %d", n)
	}

	// The gate command ran exactly twice: the attempt that triggered the
	// autofix, and the one rerun after it — never a second autofix attempt,
	// never a third gate run.
	count, err := os.ReadFile(filepath.Join(worktreeRoot, seeded[0].ID, "count"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(count)) != "2" {
		t.Errorf("gate invocation count: %q, want 2", count)
	}

	blocked := h.task(t, seeded[0].ID)
	var note *contracts.Note
	for i := range blocked.Notes {
		if blocked.Notes[i].Type == contracts.NoteBlocker && strings.Contains(blocked.Notes[i].Content, "verify") {
			note = &blocked.Notes[i]
		}
	}
	if note == nil {
		t.Fatalf("blocked without a verify blocker note: %+v", blocked.Notes)
	}
	if !strings.Contains(note.Content, "classification=lint, linters=gofmt") {
		t.Errorf("blocker note missing lint classification evidence:\n%s", note.Content)
	}
}

// Rung 1 of the ladder: a lint-shaped failure in an ordinary pre-land stage
// (not the land queue's "verify" gate) fails that stage and blocks the task
// before it ever reaches the land queue — the new lint-classification and
// mechanical-autofix machinery is scoped entirely to landAttempt's gate, and
// must never engage for an in-diff failure the agent stage itself should
// have caught.
func TestPreLandStageCatchesLintBeforeLandQueue(t *testing.T) {
	h := newHarness(t)
	const tpl = `name: pre-land-lint
stages:
  - name: executor
    agent: executor
  - name: check
    command: 'echo "foo.go:3:2: Error return value is not checked (errcheck)"; exit 1'
    on_fail: { then: block }
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: exit 1
    on_fail: { then: block }
`
	f := mustScenario(t,
		fake.Rule{Match: "Task: pre-land-check-task", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "work.txt", Content: "work\n"}}},
	)
	seeded := h.seed(contracts.Task{Title: "pre-land-check-task"})
	sum, _ := h.runIsolated(t, f, tpl, 1)

	if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
	if n := len(h.events(t, "merge.attempted")); n != 0 {
		t.Errorf("merge.attempted fired: %d, want 0 — the land queue was never reached", n)
	}
	if n := len(h.events(t, "land.autofixed")); n != 0 {
		t.Errorf("land.autofixed fired: %d, want 0 — lint autofix is land-gate-only", n)
	}
	var verifyFailed bool
	for _, ev := range h.events(t, "stage.failed") {
		if ev.TaskID == seeded[0].ID && strings.Contains(string(ev.Payload), `"stage":"verify"`) {
			verifyFailed = true
		}
	}
	if verifyFailed {
		t.Error("verify stage.failed fired; the land gate ran despite the pre-land check stage blocking first")
	}

	blocked := h.task(t, seeded[0].ID)
	var note *contracts.Note
	for i := range blocked.Notes {
		if blocked.Notes[i].Type == contracts.NoteBlocker {
			note = &blocked.Notes[i]
		}
	}
	if note == nil {
		t.Fatalf("no blocker note: %+v", blocked.Notes)
	}
	if !strings.Contains(note.Content, "stage check:") || !strings.Contains(note.Content, "errcheck") {
		t.Errorf("blocker note missing plain pre-land stage attribution:\n%s", note.Content)
	}
	// The land-gate's own auto-triage evidence format must not appear —
	// proof this is the ordinary stage-failure path, not landAttempt's.
	if strings.Contains(note.Content, "classification=lint") || strings.Contains(note.Content, "gates passed at fork") {
		t.Errorf("blocker note carries land-gate auto-triage wording for an ordinary pre-land stage:\n%s", note.Content)
	}
}
