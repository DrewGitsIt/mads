package engine

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/procid"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
)

// TestReapOnLaunchReclaimsAndResumes is the end-to-end reap-on-launch path
// (spec: reap-on-launch): a dead engine's lease — provable via a
// definitely-gone pid, same machine/boot — is reclaimed at the next engine
// launch, before the first scan, and the task resumes fresh, discarding
// the dead run's leftover worktree/branch.
func TestReapOnLaunchReclaimsAndResumes(t *testing.T) {
	h := newHarness(t)
	seeded := h.seed(contracts.Task{Title: "orphaned"})
	task := seeded[0]
	ctx := context.Background()

	own, ok := procid.Current(procid.Default)
	if !ok {
		t.Fatal("procid.Current: could not establish this process's own identity")
	}

	// A definitely-dead pid: run a subprocess to completion so Go reaps it
	// and /proc/<pid> is gone by the time Evaluate runs.
	dead := exec.Command("sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	deadPID := dead.Process.Pid

	identity := contracts.LeaseIdentity{
		MachineID: own.MachineID, BootID: own.BootID,
		PID: deadPID, PIDStartTicks: 999999,
	}
	if err := h.data.ClaimWithIdentity(ctx, task.ID, time.Minute, identity); err != nil {
		t.Fatal(err)
	}
	task.Status = contracts.StatusInProgress
	if err := h.data.UpdateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}

	// Simulate the dead run's leftover worktree + branch.
	root := t.TempDir()
	dir := filepath.Join(root, task.ID)
	branch := "mads/" + task.ID
	leftover := exec.Command("git", "worktree", "add", "-b", branch, dir)
	leftover.Dir = h.cwd
	if out, err := leftover.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add (leftover): %v\n%s", err, out)
	}

	tpl, verr := template.Check([]byte(isolatedTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	sum, err := Run(ctx, Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: 2,
		StageTimeout: 30 * time.Second,
		Isolated:     true, WorktreeRoot: root, DataURL: h.base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 || sum.Done[0] != task.ID {
		t.Fatalf("done: %v, want just %s", sum.Done, task.ID)
	}

	var foundEvidence bool
	for _, e := range h.events(t, "task.status_changed") {
		var payload struct{ From, To, Reason string }
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.To == "ready" && strings.Contains(payload.Reason, "provably dead") &&
			strings.Contains(payload.Reason, "not found") {
			foundEvidence = true
		}
	}
	if !foundEvidence {
		t.Error("no reclaim evidence event (payload.reason mentioning \"provably dead\" and \"not found\") found")
	}
}

// TestReapOnLaunchLiveSiblingNoReclaim is the required sibling test: a lease
// held by this same process (alive, matching start-ticks) must never be
// reclaimed, even by an engine launch for a different session under the
// same default actor.
func TestReapOnLaunchLiveSiblingNoReclaim(t *testing.T) {
	h := newHarness(t)
	seeded := h.seed(contracts.Task{Title: "held"})
	task := seeded[0]
	ctx := context.Background()

	own, ok := procid.Current(procid.Default)
	if !ok {
		t.Fatal("procid.Current: could not establish this process's own identity")
	}
	if err := h.data.ClaimWithIdentity(ctx, task.ID, time.Minute, own); err != nil {
		t.Fatal(err)
	}
	task.Status = contracts.StatusInProgress
	if err := h.data.UpdateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}

	other := contracts.Session{Name: "other launch", Workspace: "test"}
	if err := h.data.CreateSession(ctx, &other); err != nil {
		t.Fatal(err)
	}
	src := "name: solo\nstages:\n  - name: executor\n    agent: executor\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	if _, err := Run(ctx, Config{
		Data: h.data, Provider: fake.New(), SessionID: other.ID,
		Template: tpl, Cwd: h.cwd, StageTimeout: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	if got := h.task(t, task.ID); got.Status != contracts.StatusInProgress {
		t.Errorf("task status after sibling launch = %s, want in_progress (untouched)", got.Status)
	}
	leases, err := h.data.Leases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 || leases[0].TaskID != task.ID {
		t.Errorf("leases after sibling launch = %+v, want the original lease untouched", leases)
	}
}

// TestReapOnLaunchForeignMachineIDNoReclaim covers the same-process,
// different-recorded-machine_id case: mismatched machine_id alone makes
// nothing provable, regardless of every other field matching.
func TestReapOnLaunchForeignMachineIDNoReclaim(t *testing.T) {
	h := newHarness(t)
	seeded := h.seed(contracts.Task{Title: "elsewhere"})
	task := seeded[0]
	ctx := context.Background()

	own, ok := procid.Current(procid.Default)
	if !ok {
		t.Fatal("procid.Current: could not establish this process's own identity")
	}
	identity := own
	identity.MachineID = "not-this-machine"
	if err := h.data.ClaimWithIdentity(ctx, task.ID, time.Minute, identity); err != nil {
		t.Fatal(err)
	}
	task.Status = contracts.StatusInProgress
	if err := h.data.UpdateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}

	other := contracts.Session{Name: "other launch", Workspace: "test"}
	if err := h.data.CreateSession(ctx, &other); err != nil {
		t.Fatal(err)
	}
	src := "name: solo\nstages:\n  - name: executor\n    agent: executor\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	if _, err := Run(ctx, Config{
		Data: h.data, Provider: fake.New(), SessionID: other.ID,
		Template: tpl, Cwd: h.cwd, StageTimeout: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	if got := h.task(t, task.ID); got.Status != contracts.StatusInProgress {
		t.Errorf("task status after foreign-machine_id launch = %s, want in_progress (untouched)", got.Status)
	}
	leases, err := h.data.Leases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 || leases[0].TaskID != task.ID {
		t.Errorf("leases after foreign-machine_id launch = %+v, want the original lease untouched", leases)
	}
}
