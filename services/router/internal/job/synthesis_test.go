package job_test

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

const synthesisFixtureReply = `{
  "create_tasks":[{"title":"Add rate limiting","description":"Protect the login endpoint"}],
  "add_notes":[{"type":"risk","content":"Login endpoint has no rate limiting yet"}],
  "update_tasks":[{"description":"Now also covers the rate limiter follow-up"}]
}`

// A fixture Fast Model returning all three proposal kinds must persist each
// as a Proposal record (never a direct task/note write) alongside its own
// synthesis.proposal_created event.
func TestSynthesizeSessionPersistsProposalsAndEvents(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := scriptedFastModel(t, fake.Scenario{Name: "synthesis-job", Rules: []fake.Rule{{Reply: synthesisFixtureReply}}})

	sess := mustCreateSession(t, data, contracts.Session{Name: "synthesis session"})
	active := contracts.Task{SessionID: sess.ID, Title: "Build login", Source: "user", Status: contracts.StatusInProgress}
	if err := data.CreateTask(context.Background(), &active); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	runner := job.New(data, fm, registryclient.New(""))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := runner.Spawn(ctx, job.SynthesisKind, sess.ID,
		job.SynthesizeSession(sess.ID, []string{"user: add a rate limiter\nassistant: sure\ntool: edit login.go\n"})); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	eventually(t, 5*time.Second, func() bool {
		evs, err := data.Events(ctx, 0, sess.ID, []string{"job.completed"})
		return err == nil && len(evs) == 1
	})

	proposals, err := data.Proposals(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Proposals: %v", err)
	}
	if len(proposals) != 3 {
		t.Fatalf("len(proposals) = %d, want 3: %+v", len(proposals), proposals)
	}
	byType := map[contracts.ProposalType]contracts.Proposal{}
	for _, p := range proposals {
		if p.Status != contracts.ProposalProposed {
			t.Errorf("proposal %s status = %s, want proposed (no auto_approve)", p.ID, p.Status)
		}
		if p.Source != "synthesizer" {
			t.Errorf("proposal %s source = %q, want synthesizer", p.ID, p.Source)
		}
		byType[p.Type] = p
	}
	if ct, ok := byType[contracts.ProposalCreateTask]; !ok || ct.Task == nil || ct.Task.Title != "Add rate limiting" {
		t.Errorf("create_task proposal = %+v", ct)
	}
	if an, ok := byType[contracts.ProposalAddNote]; !ok || an.TaskID != active.ID || an.Note == nil || an.Note.Type != contracts.NoteRisk {
		t.Errorf("add_note proposal = %+v, want task_id %s", an, active.ID)
	}
	if ut, ok := byType[contracts.ProposalUpdateTask]; !ok || ut.TaskID != active.ID || ut.Update == nil {
		t.Errorf("update_task proposal = %+v, want task_id %s", ut, active.ID)
	}

	created, err := data.Events(ctx, 0, sess.ID, []string{"synthesis.proposal_created"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("len(synthesis.proposal_created) = %d, want 3", len(created))
	}
	for _, ev := range created {
		if ev.Actor != "synthesizer" {
			t.Errorf("synthesis.proposal_created actor = %q, want synthesizer", ev.Actor)
		}
	}

	// No auto_approve: applying the proposals must be untouched so far.
	tasks, err := data.Tasks(ctx, sess.ID, "")
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1 (only the pre-existing active task)", len(tasks))
	}
}

// Approving a create_task proposal applies it through the Tasks store (actor
// "synthesizer"), not as a side effect of the proposal record itself.
func TestApproveCreateTaskProposalAppliesViaTaskAPIs(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	ctx := context.Background()

	sess := mustCreateSession(t, data, contracts.Session{Name: "approve session"})
	p := &contracts.Proposal{
		SessionID: sess.ID, Type: contracts.ProposalCreateTask,
		Task: &contracts.ProposedTask{Title: "Add rate limiting", Description: "Protect the login endpoint"},
	}
	if err := data.CreateProposal(ctx, p); err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}

	approved, err := data.ApproveProposal(ctx, p.ID)
	if err != nil {
		t.Fatalf("ApproveProposal: %v", err)
	}
	if approved.Status != contracts.ProposalApproved {
		t.Fatalf("status = %s, want approved", approved.Status)
	}
	if approved.AppliedTaskID == "" {
		t.Fatal("applied_task_id is empty")
	}

	task, err := data.Task(ctx, approved.AppliedTaskID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if task.Title != "Add rate limiting" || task.SessionID != sess.ID {
		t.Errorf("applied task = %+v", task)
	}

	evs, err := data.Events(ctx, 0, sess.ID, []string{"task.created"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 1 || evs[0].Actor != "synthesizer" {
		t.Fatalf("task.created events = %+v, want one with actor synthesizer", evs)
	}

	approvedEvs, err := data.Events(ctx, 0, sess.ID, []string{"synthesis.proposal_approved"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(approvedEvs) != 1 {
		t.Fatalf("len(synthesis.proposal_approved) = %d, want 1", len(approvedEvs))
	}
}

// Approving an add_note proposal attaches the note to its target task
// (actor "synthesizer").
func TestApproveAddNoteProposalAppliesViaTaskAPIs(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	ctx := context.Background()

	sess := mustCreateSession(t, data, contracts.Session{Name: "approve note session"})
	active := contracts.Task{SessionID: sess.ID, Title: "Build login", Source: "user", Status: contracts.StatusInProgress}
	if err := data.CreateTask(ctx, &active); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	p := &contracts.Proposal{
		SessionID: sess.ID, Type: contracts.ProposalAddNote, TaskID: active.ID,
		Note: &contracts.ProposedNote{Type: contracts.NoteRisk, Content: "no rate limiting yet"},
	}
	if err := data.CreateProposal(ctx, p); err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}

	if _, err := data.ApproveProposal(ctx, p.ID); err != nil {
		t.Fatalf("ApproveProposal: %v", err)
	}

	task, err := data.Task(ctx, active.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if len(task.Notes) != 1 || task.Notes[0].Content != "no rate limiting yet" || task.Notes[0].Source != "synthesizer" {
		t.Fatalf("notes = %+v", task.Notes)
	}
}

// Approving an update_task proposal replaces only the fields it specified,
// leaving the rest of the task untouched.
func TestApproveUpdateTaskProposalAppliesViaTaskAPIs(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	ctx := context.Background()

	sess := mustCreateSession(t, data, contracts.Session{Name: "approve update session"})
	active := contracts.Task{
		SessionID: sess.ID, Title: "Build login", Description: "original description",
		Source: "user", Status: contracts.StatusInProgress,
	}
	if err := data.CreateTask(ctx, &active); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	p := &contracts.Proposal{
		SessionID: sess.ID, Type: contracts.ProposalUpdateTask, TaskID: active.ID,
		Update: &contracts.ProposedTaskUpdate{Description: "now covers rate limiting too"},
	}
	if err := data.CreateProposal(ctx, p); err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}

	if _, err := data.ApproveProposal(ctx, p.ID); err != nil {
		t.Fatalf("ApproveProposal: %v", err)
	}

	task, err := data.Task(ctx, active.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if task.Title != "Build login" {
		t.Errorf("title = %q, want unchanged", task.Title)
	}
	if task.Description != "now covers rate limiting too" {
		t.Errorf("description = %q, want updated", task.Description)
	}
}

// Dismissing a proposal must never apply any change.
func TestDismissProposalAppliesNothing(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	ctx := context.Background()

	sess := mustCreateSession(t, data, contracts.Session{Name: "dismiss session"})
	p := &contracts.Proposal{
		SessionID: sess.ID, Type: contracts.ProposalCreateTask,
		Task: &contracts.ProposedTask{Title: "Should never be created"},
	}
	if err := data.CreateProposal(ctx, p); err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}

	dismissed, err := data.DismissProposal(ctx, p.ID)
	if err != nil {
		t.Fatalf("DismissProposal: %v", err)
	}
	if dismissed.Status != contracts.ProposalDismissed {
		t.Fatalf("status = %s, want dismissed", dismissed.Status)
	}
	if dismissed.AppliedTaskID != "" {
		t.Fatalf("applied_task_id = %q, want empty", dismissed.AppliedTaskID)
	}

	tasks, err := data.Tasks(ctx, sess.ID, "")
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("len(tasks) = %d, want 0", len(tasks))
	}

	evs, err := data.Events(ctx, 0, sess.ID, []string{"synthesis.proposal_dismissed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("len(synthesis.proposal_dismissed) = %d, want 1", len(evs))
	}
}

// synthesis_auto_approve (spec §4's power-user preference) applies every
// proposal immediately on creation, no manual review step.
func TestSynthesizeSessionAutoApproveAppliesImmediately(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	ctx := context.Background()

	cfg, err := data.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.SynthesisAutoApprove = true
	if err := data.UpdateConfig(ctx, &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	fm := scriptedFastModel(t, fake.Scenario{Name: "synthesis-job-auto-approve", Rules: []fake.Rule{{
		Reply: `{"create_tasks":[{"title":"Add rate limiting"}]}`,
	}}})
	sess := mustCreateSession(t, data, contracts.Session{Name: "auto approve session"})

	runner := job.New(data, fm, registryclient.New(""))
	spawnCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := runner.Spawn(spawnCtx, job.SynthesisKind, sess.ID,
		job.SynthesizeSession(sess.ID, []string{"user: add a rate limiter\ntool: edit login.go\n"})); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	eventually(t, 5*time.Second, func() bool {
		evs, err := data.Events(ctx, 0, sess.ID, []string{"job.completed"})
		return err == nil && len(evs) == 1
	})

	proposals, err := data.Proposals(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Proposals: %v", err)
	}
	if len(proposals) != 1 || proposals[0].Status != contracts.ProposalApproved {
		t.Fatalf("proposals = %+v, want one approved", proposals)
	}
	tasks, err := data.Tasks(ctx, sess.ID, "")
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Title != "Add rate limiting" {
		t.Fatalf("tasks = %+v, want the auto-approved task", tasks)
	}
}
