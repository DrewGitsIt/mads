package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func newProposalStore(t *testing.T) (*Proposals, *Tasks, *Leases) {
	t.Helper()
	l := openMigratedDB(t)
	tasks := NewTasks(l)
	return NewProposals(l, tasks), tasks, NewLeases(l)
}

// The spec's primary target for add_note/update_task proposals is the
// active (in_progress) task — which, in the common case, still carries the
// executing engine's write lease for its whole run (services/pipeline/
// engine's heartbeat). Approve must reject cleanly (LeaseHeldError, no
// partial write, proposal left "proposed") rather than corrupt state, and
// must succeed once the lease frees up.
func TestApproveAddNoteRejectedWhileTaskActivelyLeased(t *testing.T) {
	proposals, tasks, leases := newProposalStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{Title: "Build login", Status: contracts.StatusInProgress})

	if _, err := leases.Claim(ctx, task.ID, "executor", time.Minute); err != nil {
		t.Fatal(err)
	}

	p := &contracts.Proposal{
		SessionID: task.SessionID, Type: contracts.ProposalAddNote, TaskID: task.ID,
		Note: &contracts.ProposedNote{Type: contracts.NoteRisk, Content: "no rate limiting yet"},
	}
	if err := proposals.Create(ctx, p); err != nil {
		t.Fatal(err)
	}

	var held *LeaseHeldError
	if _, err := proposals.Approve(ctx, p.ID); !errors.As(err, &held) || held.Holder != "executor" {
		t.Fatalf("Approve while leased: want LeaseHeldError(executor), got %v", err)
	}

	// No partial write, and the proposal is still pending retry.
	got, err := proposals.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.ProposalProposed {
		t.Fatalf("status = %s, want proposed (rejected approval must not corrupt it)", got.Status)
	}
	reloaded, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Notes) != 0 {
		t.Fatalf("notes = %+v, want none (rejected approval must not partially apply)", reloaded.Notes)
	}

	// Once the lease frees up, the same proposal can be approved.
	if err := leases.Release(ctx, task.ID, "executor"); err != nil {
		t.Fatal(err)
	}
	approved, err := proposals.Approve(ctx, p.ID)
	if err != nil {
		t.Fatalf("Approve after release: %v", err)
	}
	if approved.Status != contracts.ProposalApproved {
		t.Fatalf("status = %s, want approved", approved.Status)
	}
	reloaded, err = tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Notes) != 1 || reloaded.Notes[0].Source != "synthesizer" {
		t.Fatalf("notes = %+v, want one note from synthesizer", reloaded.Notes)
	}
}

func TestApproveUpdateTaskRejectedWhileTaskActivelyLeased(t *testing.T) {
	proposals, tasks, leases := newProposalStore(t)
	ctx := context.Background()
	task := mkTask(t, tasks, contracts.Task{
		Title: "Build login", Description: "original description", Status: contracts.StatusInProgress,
	})

	if _, err := leases.Claim(ctx, task.ID, "executor", time.Minute); err != nil {
		t.Fatal(err)
	}

	p := &contracts.Proposal{
		SessionID: task.SessionID, Type: contracts.ProposalUpdateTask, TaskID: task.ID,
		Update: &contracts.ProposedTaskUpdate{Description: "now covers rate limiting too"},
	}
	if err := proposals.Create(ctx, p); err != nil {
		t.Fatal(err)
	}

	var held *LeaseHeldError
	if _, err := proposals.Approve(ctx, p.ID); !errors.As(err, &held) || held.Holder != "executor" {
		t.Fatalf("Approve while leased: want LeaseHeldError(executor), got %v", err)
	}

	got, err := proposals.Get(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.ProposalProposed {
		t.Fatalf("status = %s, want proposed (rejected approval must not corrupt it)", got.Status)
	}
	reloaded, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Description != "original description" {
		t.Fatalf("description = %q, want unchanged (rejected approval must not partially apply)", reloaded.Description)
	}

	if err := leases.Release(ctx, task.ID, "executor"); err != nil {
		t.Fatal(err)
	}
	if _, err := proposals.Approve(ctx, p.ID); err != nil {
		t.Fatalf("Approve after release: %v", err)
	}
	reloaded, err = tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Description != "now covers rate limiting too" {
		t.Fatalf("description = %q, want updated", reloaded.Description)
	}
	// update_task must preserve fields it didn't touch, dependencies included.
	if reloaded.Title != "Build login" {
		t.Fatalf("title = %q, want unchanged", reloaded.Title)
	}
}

// update_task's approval reads the task fresh inside the same transaction
// as the write (getInTx), so dependencies set before the proposal was
// created must survive the round trip untouched.
func TestApproveUpdateTaskPreservesDependencies(t *testing.T) {
	proposals, tasks, _ := newProposalStore(t)
	ctx := context.Background()
	dep := mkTask(t, tasks, contracts.Task{Title: "Dependency"})
	task := mkTask(t, tasks, contracts.Task{Title: "Build login", Dependencies: []string{dep.ID}})

	p := &contracts.Proposal{
		SessionID: task.SessionID, Type: contracts.ProposalUpdateTask, TaskID: task.ID,
		Update: &contracts.ProposedTaskUpdate{Description: "now covers rate limiting too"},
	}
	if err := proposals.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := proposals.Approve(ctx, p.ID); err != nil {
		t.Fatal(err)
	}

	reloaded, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Dependencies) != 1 || reloaded.Dependencies[0] != dep.ID {
		t.Fatalf("dependencies = %+v, want [%s]", reloaded.Dependencies, dep.ID)
	}
}
