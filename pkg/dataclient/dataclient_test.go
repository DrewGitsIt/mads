package dataclient_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// The client round-trips against a REAL Data Service (datatest) — no mocks;
// this test is the wire-contract check for both sides.
func TestClientRoundTrip(t *testing.T) {
	base := datatest.Start(t)
	ctx := context.Background()
	c := dataclient.New(base, "pipeline")

	// Session + tasks with dependencies via import.
	sess := contracts.Session{Name: "run"}
	if err := c.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	batch := []contracts.Task{
		{Title: "core", SessionID: sess.ID, Source: "user"},
		{Title: "api", SessionID: sess.ID, Source: "user"},
	}
	imported, err := c.ImportTasks(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 2 || imported[0].ID == "" {
		t.Fatalf("import: %+v", imported)
	}

	// List, get, update, note.
	ready, err := c.Tasks(ctx, sess.ID, contracts.StatusReady)
	if err != nil || len(ready) != 2 {
		t.Fatalf("list ready: %d, %v", len(ready), err)
	}
	task := ready[0]
	task.Status = contracts.StatusInProgress
	if err := c.UpdateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	if err := c.AddNote(ctx, task.ID, &contracts.Note{
		Type: contracts.NoteObservation, Content: "started", Status: contracts.NoteStatusConfirmed,
	}); err != nil {
		t.Fatal(err)
	}

	// Leases: claim, conflicting claim from another actor, release.
	if err := c.Claim(ctx, task.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	other := dataclient.New(base, "someone-else")
	if err := other.Claim(ctx, task.ID, time.Minute); !errors.Is(err, dataclient.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if err := c.Release(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	// Registry + seeded template.
	agent, err := c.Agent(ctx, "executor")
	if err != nil || agent.Prompt == "" {
		t.Fatalf("seeded agent: %+v, %v", agent, err)
	}
	tpl, err := c.Template(ctx, "default-implementation")
	if err != nil || tpl.Source == "" {
		t.Fatalf("seeded template: %+v, %v", tpl, err)
	}

	// Events: append + filtered batch read.
	if err := c.Append(ctx, &contracts.Envelope{
		Type: contracts.PipelineStarted, SessionID: sess.ID,
	}); err != nil {
		t.Fatal(err)
	}
	evs, err := c.Events(ctx, 0, sess.ID, []string{"pipeline.*"})
	if err != nil || len(evs) != 1 || evs[0].Type != contracts.PipelineStarted {
		t.Fatalf("events: %+v, %v", evs, err)
	}

	// Typed errors.
	if _, err := c.Task(ctx, "tsk_missing"); !errors.Is(err, dataclient.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	var verr *contracts.ValidationError
	if err := c.CreateTask(ctx, &contracts.Task{}); !errors.As(err, &verr) {
		t.Fatalf("want ValidationError, got %v", err)
	}
}
