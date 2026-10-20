package engine

import (
	"context"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// setStatusAs moves the task to status as actor (the X-Mads-Actor its
// task.status_changed records).
func setStatusAs(t *testing.T, h *harness, actor string, id string, status contracts.TaskStatus) {
	t.Helper()
	c := dataclient.New(h.base, actor)
	task, err := c.Task(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	task.Status = status
	if err := c.UpdateTask(context.Background(), &task); err != nil {
		t.Fatal(err)
	}
}

// pipelineStarted moves the task to in_progress the way the engine does
// (setStatus under actor "pipeline"), as a run that then died would have.
func pipelineStarted(t *testing.T, h *harness, task contracts.Task) {
	t.Helper()
	setStatusAs(t, h, "pipeline", task.ID, contracts.StatusInProgress)
}

// Interrupted is a pipeline-only concept: only a task the pipeline moved to
// in_progress is picked up. One a person or the synthesizer set in progress
// is theirs — never run and never counted — and the most recent move into
// in_progress decides.
func TestOnlyPipelineStartedTasksArePickedUp(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	seeded := h.seed(
		contracts.Task{Title: "user-set"},
		contracts.Task{Title: "pipeline-set"},
		contracts.Task{Title: "pipeline then user"},
		contracts.Task{Title: "synthesizer-set"},
	)
	setStatusAs(t, h, "user", seeded[0].ID, contracts.StatusInProgress)
	pipelineStarted(t, h, seeded[1])
	pipelineStarted(t, h, seeded[2])
	setStatusAs(t, h, "user", seeded[2].ID, contracts.StatusBlocked)
	setStatusAs(t, h, "user", seeded[2].ID, contracts.StatusInProgress)
	// An approved synthesizer proposal applies through the task API under
	// actor "synthesizer".
	setStatusAs(t, h, "synthesizer", seeded[3].ID, contracts.StatusInProgress)

	p := newPickupProvider(pickupFake(t), "")
	sum := w.run(t, w.config(t, p, isolatedTemplate, 1, "run_1"))
	if len(sum.Done) != 1 || sum.Done[0] != seeded[1].ID {
		t.Errorf("done = %v, want just the pipeline-set task", sum.Done)
	}
	if len(sum.Skipped)+len(sum.LeftOut)+len(sum.Blocked) != 0 {
		t.Errorf("summary = %+v, want the others not counted", sum)
	}
	for _, i := range []int{0, 2, 3} {
		if got := h.task(t, seeded[i].ID).Status; got != contracts.StatusInProgress {
			t.Errorf("%s status = %s, want in_progress (untouched)", seeded[i].Title, got)
		}
		if n := len(p.callsMatching("Task: " + seeded[i].Title + "\n")); n != 0 {
			t.Errorf("%s ran %d agent calls, want none", seeded[i].Title, n)
		}
	}
	if picks := stageEvents(t, h, contracts.StagePickedUp, seeded[1].ID); len(picks) != 1 {
		t.Errorf("pipeline-set task pickups = %d, want 1", len(picks))
	}
}
