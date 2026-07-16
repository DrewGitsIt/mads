package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
)

// mergeResolveTemplate adds a merge_resolve agent stage guarded by `when:
// on_loop` right before the merge, wired as the merge stage's loop_back
// target: it costs nothing on the happy path (first pass), and only runs
// once the merge stage has already failed once.
const mergeResolveTemplate = `name: merge-resolve-demo
stages:
  - name: executor
    agent: executor
  - name: merge_resolve
    agent: merge_resolve
    when: on_loop
  - name: merge
    command: git merge --no-ff
    on_fail: { loop_back: merge_resolve, max_loops: 2 }
  - name: verify
    command: make test
    on_fail: { then: block }
`

// The item 3 acceptance criterion: a template with a merge_resolve stage
// guarded by `when: on_loop` validates, skips that stage on the first pass,
// and enters it after the merge stage fails (a real conflict, forced the
// same way TestConflictingPairBlocksSecond does).
func TestOnLoopValidatesSkipsThenEntersMergeResolve(t *testing.T) {
	h := newHarness(t)

	if _, verr := template.Check([]byte(mergeResolveTemplate), func(string) bool { return true }); verr != nil {
		t.Fatalf("template with on_loop merge_resolve stage failed to validate: %v", verr)
	}

	if err := h.data.CreateAgent(context.Background(), &contracts.Agent{
		Name: "merge_resolve", Prompt: "Resolve the merge conflict using both sides' intent.",
	}); err != nil {
		t.Fatal(err)
	}

	f := mustScenario(t,
		fake.Rule{Match: "Pipeline stage: executor\nTask: first", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "shared.txt", Content: "first version\n"}}},
		fake.Rule{Match: "Pipeline stage: executor\nTask: second", Reply: "done", DelayMs: 300,
			Tools: []fake.ToolStep{{Name: "write_file", Path: "shared.txt", Content: "second version\n"}}},
		fake.Rule{Match: "Pipeline stage: merge_resolve", Reply: "attempted a resolution\nSTAGE: pass"},
	)
	seeded := h.seed(contracts.Task{Title: "first"}, contracts.Task{Title: "second"})
	sum, _ := h.runIsolated(t, f, mergeResolveTemplate, 2)

	// Whether "second" ends up Done or Blocked depends on exactly what
	// merge_resolve's scripted reply does to the conflicted file (not this
	// criterion's concern) — what matters here is the on_loop wiring itself.
	if len(sum.Done)+len(sum.Blocked) != 2 {
		t.Fatalf("summary: %+v", sum)
	}
	second := h.task(t, seeded[1].ID)

	// merge_resolve was skipped on the first (non-loop) pass for both tasks.
	var skippedMergeResolve int
	for _, ev := range h.events(t, "stage.skipped") {
		if strings.Contains(string(ev.Payload), "merge_resolve") {
			skippedMergeResolve++
		}
	}
	if skippedMergeResolve != 2 {
		t.Errorf("stage.skipped for merge_resolve: %d, want 2 (one per task's first pass)", skippedMergeResolve)
	}

	// merge_resolve ran (was entered) for "second" after its merge stage
	// failed on the conflict — a stage.started for it, scoped to that task.
	var startedMergeResolve int
	for _, ev := range h.events(t, "stage.started") {
		if ev.TaskID == second.ID && strings.Contains(string(ev.Payload), "merge_resolve") {
			startedMergeResolve++
		}
	}
	if startedMergeResolve == 0 {
		t.Errorf("merge_resolve never entered for the conflicting task after its merge failed")
	}
}
