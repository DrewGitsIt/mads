package dataclient_test

import (
	"context"
	"errors"
	"fmt"
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

	// Session update + delete.
	sess.Name = "renamed"
	if err := c.UpdateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Session(ctx, sess.ID); err != nil || got.Name != "renamed" {
		t.Fatalf("session after update: %+v, %v", got, err)
	}
	if err := c.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Session(ctx, sess.ID); !errors.Is(err, dataclient.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}

	// Registry + seeded template.
	agent, err := c.Agent(ctx, "executor")
	if err != nil || agent.Prompt == "" {
		t.Fatalf("seeded agent: %+v, %v", agent, err)
	}
	seeded, err := c.Agents(ctx)
	if err != nil || len(seeded) < 6 {
		t.Fatalf("seeded agents list: %d, %v", len(seeded), err)
	}
	newAgent := contracts.Agent{Name: "custom-agent", Prompt: "do things"}
	if err := c.CreateAgent(ctx, &newAgent); err != nil || newAgent.ID == "" {
		t.Fatalf("create agent: %+v, %v", newAgent, err)
	}
	afterCreate, err := c.Agents(ctx)
	if err != nil || len(afterCreate) != len(seeded)+1 {
		t.Fatalf("agents after create: %d, %v", len(afterCreate), err)
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

// TestClientReclaimWireRoundTrip covers ClaimWithIdentity -> Leases ->
// ReclaimLease's JSON shapes against a real Data Service (reap-on-launch).
func TestClientReclaimWireRoundTrip(t *testing.T) {
	base := datatest.Start(t)
	ctx := context.Background()
	c := dataclient.New(base, "pipeline")

	sess := contracts.Session{Name: "reap"}
	if err := c.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	task := contracts.Task{Title: "A", SessionID: sess.ID, Source: "user"}
	if err := c.CreateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}

	identity := contracts.LeaseIdentity{MachineID: "m1", BootID: "b1", PID: 42, PIDStartTicks: 999}
	if err := c.ClaimWithIdentity(ctx, task.ID, time.Minute, identity); err != nil {
		t.Fatal(err)
	}

	leases, err := c.Leases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 || leases[0].TaskID != task.ID || leases[0].Identity == nil || *leases[0].Identity != identity {
		t.Fatalf("Leases = %+v, want one lease for %s with identity %+v", leases, task.ID, identity)
	}

	task.Status = contracts.StatusInProgress
	if err := c.UpdateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}

	ok, err := c.ReclaimLease(ctx, task.ID, identity, "provably dead")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ReclaimLease: want reclaimed=true for a matching identity")
	}
	got, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != contracts.StatusReady {
		t.Errorf("task status after reclaim = %s, want ready", got.Status)
	}

	leases, err = c.Leases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 0 {
		t.Errorf("Leases after reclaim = %+v, want none", leases)
	}
}

// TestClientHealth checks Health against a real running Data Service and
// against an unreachable one.
func TestClientHealth(t *testing.T) {
	base := datatest.Start(t)
	ctx := context.Background()
	c := dataclient.New(base, "router")
	if err := c.Health(ctx); err != nil {
		t.Fatalf("Health against running Data Service: %v", err)
	}

	unreachable := dataclient.New("http://127.0.0.1:1", "router")
	if err := unreachable.Health(ctx); err == nil {
		t.Fatal("Health against unreachable Data Service: want error, got nil")
	}
}

// TestClientDraftConfirmDiscard exercises the draft lifecycle wrapper
// methods (ConfirmTask/DiscardTask/ConfirmNote/DiscardNote) against a real
// Data Service, checking both the resulting state and the emitted events.
func TestClientDraftConfirmDiscard(t *testing.T) {
	base := datatest.Start(t)
	ctx := context.Background()
	c := dataclient.New(base, "synthesizer")

	sess := contracts.Session{Name: "drafts"}
	if err := c.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}

	// Task confirm: draft -> confirmed, task.confirmed event, no longer a draft.
	toConfirm := contracts.Task{
		Title: "maybe do this", SessionID: sess.ID, Source: "synthesizer", Draft: true,
	}
	if err := c.CreateTask(ctx, &toConfirm); err != nil {
		t.Fatal(err)
	}
	if err := c.ConfirmTask(ctx, toConfirm.ID); err != nil {
		t.Fatal(err)
	}
	got, err := c.Task(ctx, toConfirm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Draft {
		t.Errorf("confirmed task still marked draft: %+v", got)
	}

	// Task discard: draft task is removed entirely.
	toDiscard := contracts.Task{
		Title: "discard this", SessionID: sess.ID, Source: "synthesizer", Draft: true,
	}
	if err := c.CreateTask(ctx, &toDiscard); err != nil {
		t.Fatal(err)
	}
	if err := c.DiscardTask(ctx, toDiscard.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Task(ctx, toDiscard.ID); !errors.Is(err, dataclient.ErrNotFound) {
		t.Fatalf("want ErrNotFound after discard, got %v", err)
	}

	// Note confirm/discard against a confirmed parent task.
	parent := contracts.Task{Title: "parent", SessionID: sess.ID, Source: "user"}
	if err := c.CreateTask(ctx, &parent); err != nil {
		t.Fatal(err)
	}
	noteToConfirm := contracts.Note{
		Type: contracts.NoteDecision, Content: "maybe this",
		Source: "synthesizer", Status: contracts.NoteStatusDraft,
	}
	if err := c.AddNote(ctx, parent.ID, &noteToConfirm); err != nil {
		t.Fatal(err)
	}
	if err := c.ConfirmNote(ctx, parent.ID, noteToConfirm.ID); err != nil {
		t.Fatal(err)
	}

	noteToDiscard := contracts.Note{
		Type: contracts.NoteObservation, Content: "discard this",
		Source: "synthesizer", Status: contracts.NoteStatusDraft,
	}
	if err := c.AddNote(ctx, parent.ID, &noteToDiscard); err != nil {
		t.Fatal(err)
	}
	if err := c.DiscardNote(ctx, parent.ID, noteToDiscard.ID); err != nil {
		t.Fatal(err)
	}

	// Second confirm/discard on the same draft fails: no longer a draft.
	if err := c.ConfirmTask(ctx, toConfirm.ID); err == nil {
		t.Error("want error confirming an already-confirmed task")
	}
	if err := c.ConfirmNote(ctx, parent.ID, noteToConfirm.ID); err == nil {
		t.Error("want error confirming an already-confirmed note")
	}

	evs, err := c.Events(ctx, 0, sess.ID, []string{
		"task.confirmed", "task.discarded", "note.confirmed", "note.discarded",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := map[contracts.EventType]int{
		contracts.TaskConfirmed: 1, contracts.TaskDiscarded: 1,
		contracts.NoteConfirmed: 1, contracts.NoteDiscarded: 1,
	}
	gotTypes := map[contracts.EventType]int{}
	for _, e := range evs {
		gotTypes[e.Type]++
	}
	for typ, want := range wantTypes {
		if gotTypes[typ] != want {
			t.Errorf("event %s: got %d, want %d (all events: %+v)", typ, gotTypes[typ], want, evs)
		}
	}
}

// TestClientDelegateWireRoundTrip covers PutDelegate -> SessionDelegates ->
// Delegates(state) against a real Data Service: the path is the key, the
// store owns identity, and the cross-session running list is one call.
func TestClientDelegateWireRoundTrip(t *testing.T) {
	base := datatest.Start(t)
	ctx := context.Background()
	c := dataclient.New(base, "session")

	sess := contracts.Session{Name: "parent"}
	if err := c.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC()
	got, err := c.PutDelegate(ctx, contracts.Delegate{
		SessionID: sess.ID, HarnessID: "a7f5", ToolUseID: "toolu_1", Type: "general-purpose",
		State: contracts.DelegateRunning, Runs: 1, StartedAt: start,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.State != contracts.DelegateRunning {
		t.Fatalf("PutDelegate = %+v", got)
	}

	ended := start.Add(time.Second)
	got.State, got.Reason, got.EndedAt = contracts.DelegateFailed, "failed", &ended
	if _, err := c.PutDelegate(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, err := c.SessionDelegates(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != contracts.DelegateFailed || list[0].EndedAt == nil || !list[0].EndedAt.Equal(ended) {
		t.Fatalf("SessionDelegates = %+v", list)
	}
	running, err := c.Delegates(ctx, contracts.DelegateRunning)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 0 {
		t.Errorf("Delegates(running) = %+v, want none", running)
	}

	// The rail under a fade window: running ones always, finished ones only
	// when they ended at or after the cutoff.
	if _, err := c.PutDelegate(ctx, contracts.Delegate{
		SessionID: sess.ID, HarnessID: "b2", State: contracts.DelegateRunning, Runs: 1, StartedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	ids := func(list []contracts.Delegate) (out []string) {
		for _, d := range list {
			out = append(out, d.HarnessID)
		}
		return out
	}
	if got, err := c.DelegatesEndedAfter(ctx, ended); err != nil || fmt.Sprint(ids(got)) != "[a7f5 b2]" {
		t.Errorf("DelegatesEndedAfter(the end) = %v, %v; want both", ids(got), err)
	}
	if got, err := c.DelegatesEndedAfter(ctx, ended.Add(time.Nanosecond)); err != nil || fmt.Sprint(ids(got)) != "[b2]" {
		t.Errorf("DelegatesEndedAfter(just after the end) = %v, %v; want only the running one", ids(got), err)
	}
}
