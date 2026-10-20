package engine

import (
	"context"
	"os"
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
// (spec: reap-on-launch): a dead engine's lease — another run's
// ("pipeline:run_dead", found by kind, not by this run's holder), provable via a
// definitely-gone pid, same machine/boot — is reclaimed at the next engine
// launch, before the first scan. Reclaiming changes no status, so the task
// is Interrupted, and the same run picks it up (docs/PIPELINE-LEASES-DESIGN.md
// B2): with no progress record it restarts at the first stage in the dead
// run's kept worktree, whose uncommitted work survives into trunk.
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
	if err := h.data.ClaimWithIdentity(ctx, task.ID, "pipeline:run_dead", time.Minute, identity); err != nil {
		t.Fatal(err)
	}
	// The dead engine moved the task to in_progress under its lease, as
	// setStatus does: h.data is a "pipeline" client holding that lease's
	// token, so task.status_changed records actor "pipeline" — what makes
	// the task Interrupted once the lease is gone.
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
	if err := os.WriteFile(filepath.Join(dir, "leftover.txt"), []byte("dead run's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tpl, verr := template.Check([]byte(isolatedTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	sum, err := Run(ctx, Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID, RunID: "run_next",
		Template: tpl, Cwd: h.cwd, Concurrency: 2,
		StageTimeout: 30 * time.Second,
		Isolated:     true, WorktreeRoot: root, DataURL: h.base,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reclaim deletes the dead holder's lease and changes no status
	// (docs/PIPELINE-LEASES-DESIGN.md A3); the Interrupted task is then
	// picked up and runs to done.
	if len(sum.Done) != 1 || sum.Done[0] != task.ID {
		t.Fatalf("summary: %+v, want the reclaimed task picked up and done", sum)
	}
	leases, err := h.data.LeasesOfKind(ctx, "pipeline")
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 0 {
		t.Fatalf("leases after the run: %+v, want none", leases)
	}
	if got := h.task(t, task.ID); got.Status != contracts.StatusDone {
		t.Errorf("status = %s, want done", got.Status)
	}
	// No status write on pickup: the test's own ready -> in_progress, then
	// the run's in_progress -> done.
	if evs := h.events(t, "task.status_changed"); len(evs) != 2 {
		t.Errorf("status_changed events = %d, want 2", len(evs))
	}
	picks := h.events(t, string(contracts.StagePickedUp))
	if len(picks) != 1 || !strings.Contains(string(picks[0].Payload), `"mode":"restart"`) {
		t.Errorf("stage.picked_up events = %+v, want one restart (no progress record)", picks)
	}
	if _, err := os.Stat(filepath.Join(h.cwd, "leftover.txt")); err != nil {
		t.Errorf("the dead run's uncommitted work didn't reach trunk: %v", err)
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
	if err := h.data.ClaimWithIdentity(ctx, task.ID, "pipeline:run_live", time.Minute, own); err != nil {
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
		Data: h.data, Provider: fake.New(), SessionID: other.ID, RunID: "run_next",
		Template: tpl, Cwd: h.cwd, StageTimeout: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	if got := h.task(t, task.ID); got.Status != contracts.StatusInProgress {
		t.Errorf("task status after sibling launch = %s, want in_progress (untouched)", got.Status)
	}
	leases, err := h.data.LeasesOfKind(ctx, "pipeline")
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
	if err := h.data.ClaimWithIdentity(ctx, task.ID, "pipeline:run_elsewhere", time.Minute, identity); err != nil {
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
		Data: h.data, Provider: fake.New(), SessionID: other.ID, RunID: "run_next",
		Template: tpl, Cwd: h.cwd, StageTimeout: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	if got := h.task(t, task.ID); got.Status != contracts.StatusInProgress {
		t.Errorf("task status after foreign-machine_id launch = %s, want in_progress (untouched)", got.Status)
	}
	leases, err := h.data.LeasesOfKind(ctx, "pipeline")
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 || leases[0].TaskID != task.ID {
		t.Errorf("leases after foreign-machine_id launch = %+v, want the original lease untouched", leases)
	}
}
