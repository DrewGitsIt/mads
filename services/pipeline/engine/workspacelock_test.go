package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
)

// TestAcquireWorkspaceFlockBlocksUntilReleased exercises the raw lock
// primitive directly: a second acquire on the same path blocks until the
// first releases, proving cross-process exclusion (not just in-process,
// which landQueue already provides).
func TestAcquireWorkspaceFlockBlocksUntilReleased(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("acquireWorkspaceFlock is a no-op off Linux")
	}
	path := filepath.Join(t.TempDir(), "mads-land.lock")

	release1, err := acquireWorkspaceFlock(path)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan struct{})
	go func() {
		release2, err := acquireWorkspaceFlock(path)
		if err != nil {
			t.Error(err)
			return
		}
		close(acquired)
		_ = release2()
	}()

	select {
	case <-acquired:
		t.Fatal("second acquire succeeded while the first still held the lock")
	case <-time.After(200 * time.Millisecond):
	}

	if err := release1(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire never unblocked after release")
	}
}

// TestCrossEngineWorkspaceLockSerializesLandAttempts runs two independent
// engine.Run invocations concurrently — two "engine processes" in every way
// that matters to the lock (separate worktrees instances, separate
// landQueues) — against the same primary repo, each landing one task. The
// spec requirement ("Not Rocket Science Rule") is that land attempts across
// processes never interleave; this test's observable proxy is that the
// trunk ends up clean with both tasks' changes merged, not corrupted by two
// concurrent `git merge --no-ff` runs.
func TestCrossEngineWorkspaceLockSerializesLandAttempts(t *testing.T) {
	h := newHarness(t)

	sessA := contracts.Session{Name: "engine A", Workspace: "test"}
	if err := h.data.CreateSession(context.Background(), &sessA); err != nil {
		t.Fatal(err)
	}
	sessB := contracts.Session{Name: "engine B", Workspace: "test"}
	if err := h.data.CreateSession(context.Background(), &sessB); err != nil {
		t.Fatal(err)
	}

	fA, err := fake.NewWithScenario(fake.Scenario{Name: "A", Rules: []fake.Rule{
		{Match: "Task: alpha", Reply: "done", Tools: []fake.ToolStep{{Name: "write_file", Path: "alpha.txt", Content: "alpha\n"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fB, err := fake.NewWithScenario(fake.Scenario{Name: "B", Rules: []fake.Rule{
		{Match: "Task: beta", Reply: "done", Tools: []fake.ToolStep{{Name: "write_file", Path: "beta.txt", Content: "beta\n"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	tasksA, err := h.data.ImportTasks(context.Background(), []contracts.Task{{SessionID: sessA.ID, Title: "alpha", Source: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	tasksB, err := h.data.ImportTasks(context.Background(), []contracts.Task{{SessionID: sessB.ID, Title: "beta", Source: "user"}})
	if err != nil {
		t.Fatal(err)
	}

	tpl, verr := template.Check([]byte(isolatedTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	run := func(sessionID string, p *fake.Fake) {
		defer wg.Done()
		_, err := Run(context.Background(), Config{
			Data: h.data, Provider: p, SessionID: sessionID,
			Template: tpl, Cwd: h.cwd, Concurrency: 1,
			StageTimeout: 30 * time.Second,
			Isolated:     true, WorktreeRoot: t.TempDir(), DataURL: h.base,
		})
		errs <- err
	}
	wg.Add(2)
	go run(sessA.ID, fA)
	go run(sessB.ID, fB)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{"alpha.txt", "beta.txt"} {
		if _, err := os.Stat(filepath.Join(h.cwd, name)); err != nil {
			t.Errorf("%s missing from merged trunk", name)
		}
	}
	status := gitOut(t, h.cwd, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Errorf("trunk dirty after concurrent merges:\n%s", status)
	}

	doneA := h.task(t, tasksA[0].ID)
	doneB := h.task(t, tasksB[0].ID)
	if doneA.Status != contracts.StatusDone || doneB.Status != contracts.StatusDone {
		t.Fatalf("statuses: A=%s B=%s, want both done", doneA.Status, doneB.Status)
	}
}
