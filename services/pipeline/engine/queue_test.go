package engine

// W3.3 — the live work queue: mid-run inserts join the running pipeline;
// cycle-introducing inserts are rejected loudly.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
)

// A task created through the API while the run is active is picked up and
// executed — the queue is live, not a launch-time snapshot.
func TestMidRunTaskPickup(t *testing.T) {
	h := newHarness(t)
	// The seeded task streams slowly enough to keep the run open while the
	// insert happens.
	f := mustScenario(t, fake.Rule{
		Match: "Task: long-runner", Chunks: []string{"working ", "still ", "done"}, DelayMs: 300,
	})
	seeded := h.seed(contracts.Task{Title: "long-runner"})

	src := "name: solo\nstages:\n  - name: executor\n    agent: executor\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}

	type outcome struct {
		sum *Summary
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		sum, err := Run(context.Background(), Config{
			Data: h.data, Provider: f, SessionID: h.sess.ID,
			Template: tpl, Cwd: h.cwd, Concurrency: 2,
			StageTimeout: 30 * time.Second,
		})
		done <- outcome{sum, err}
	}()

	// Wait for the engine to actually claim and start the seeded task
	// before inserting mid-run — a fixed sleep can't be sized reliably
	// once the suite runs under load.
	h.waitStatus(t, seeded[0].ID, contracts.StatusInProgress, 5*time.Second)
	late := contracts.Task{Title: "late arrival", SessionID: h.sess.ID, Source: "pipeline"}
	if err := h.data.CreateTask(context.Background(), &late); err != nil {
		t.Fatal(err)
	}

	res := <-done
	if res.err != nil {
		t.Fatal(res.err)
	}
	if len(res.sum.Done) != 2 {
		t.Fatalf("mid-run insert not picked up: %+v", res.sum)
	}
	if got := h.task(t, late.ID); got.Status != contracts.StatusDone {
		t.Errorf("late task status: %s", got.Status)
	}
}

// A mid-run insert whose dependencies form a cycle is rejected from the
// queue: blocked with a note naming the cycle, announced as an event.
func TestMidRunCycleRejected(t *testing.T) {
	h := newHarness(t)
	f := mustScenario(t, fake.Rule{
		Match: "Task: long-runner", Chunks: []string{"working ", "still ", "done"}, DelayMs: 300,
	})
	seeded := h.seed(contracts.Task{Title: "long-runner"})

	src := "name: solo\nstages:\n  - name: executor\n    agent: executor\n"
	tpl, _ := template.Check([]byte(src), func(string) bool { return true })

	done := make(chan *Summary, 1)
	go func() {
		sum, err := Run(context.Background(), Config{
			Data: h.data, Provider: f, SessionID: h.sess.ID,
			Template: tpl, Cwd: h.cwd, Concurrency: 2,
			StageTimeout: 30 * time.Second,
		})
		if err != nil {
			t.Error(err)
		}
		done <- sum
	}()

	h.waitStatus(t, seeded[0].ID, contracts.StatusInProgress, 5*time.Second)
	// Import as drafts: a draft is excluded from runnable() (engine.go)
	// regardless of its dependency state, so neither task can be picked up
	// as valid work during the two writes below that build the cycle.
	// Confirming afterward makes them live mid-run inserts — the real
	// scenario under test — without ever exposing a half-built-cycle,
	// fully-runnable task to the scheduler.
	pair, err := h.data.ImportTasks(context.Background(), []contracts.Task{
		{Title: "chicken", SessionID: h.sess.ID, Source: "pipeline", Draft: true},
		{Title: "egg", SessionID: h.sess.ID, Source: "pipeline", Draft: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, b := pair[0], pair[1]
	a.Dependencies = []string{b.ID}
	b.Dependencies = []string{a.ID}
	for _, task := range []*contracts.Task{&a, &b} {
		if err := h.data.UpdateTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{a.ID, b.ID} {
		if err := h.data.ConfirmTask(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}

	sum := <-done
	if len(sum.Done) != 1 || len(sum.Blocked) != 2 {
		t.Fatalf("summary: %+v", sum)
	}
	blocked := h.task(t, a.ID)
	if blocked.Status != contracts.StatusBlocked {
		t.Fatalf("cycle participant status: %s", blocked.Status)
	}
	foundNote := false
	for _, n := range blocked.Notes {
		if n.Type == contracts.NoteBlocker && strings.Contains(n.Content, "cycle") {
			foundNote = true
		}
	}
	if !foundNote {
		t.Errorf("no cycle blocker note: %+v", blocked.Notes)
	}
	// The rejection is announced: a status-changed event for the participant.
	found := false
	for _, ev := range h.events(t, "task.status_changed") {
		if ev.TaskID == a.ID && strings.Contains(string(ev.Payload), "blocked") {
			found = true
		}
	}
	if !found {
		t.Error("cycle rejection produced no task.status_changed event")
	}
}
