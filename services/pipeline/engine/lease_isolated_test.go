package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// killableProvider runs one specific task ("orphan") as a real, long-lived
// subprocess instead of the in-process fake, so a test can prove the engine
// actually kills the agent's process tree on lease loss rather than merely
// abandoning a goroutine. Every other prompt is delegated to the embedded
// fake, unchanged.
type killableProvider struct {
	*fake.Fake
	orphanMatch string
	pidFile     string
	started     chan struct{}
}

func (p *killableProvider) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	if !strings.Contains(req.Prompt, p.orphanMatch) {
		return p.Fake.OneShot(ctx, req)
	}
	// Dirty the worktree before backgrounding, exactly like a real agent
	// leaving uncommitted work, then park on a long sleep that only a
	// SIGKILL to the process group ends.
	cmd := exec.Command("sh", "-c", "echo dirty > dirty.txt; echo $$ > "+p.pidFile+"; exec sleep 300")
	cmd.Dir = req.Cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	close(p.started)
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waitErr
		return nil, ctx.Err()
	case err := <-waitErr:
		return nil, fmt.Errorf("orphan subprocess exited before it was killed: %w", err)
	}
}

// pidAlive probes with signal 0 rather than waiting on the process: the
// orphan is owned (and reaped) by the provider's own cmd.Wait goroutine, not
// a child this test could wait on. Signal 0 still succeeds on an unreaped
// zombie, so this only reports dead once that goroutine has reaped it — which
// the provider does before OneShot returns. ESRCH means gone; EPERM means it
// exists but belongs to someone else, which still counts as alive. Signal 0
// works the same on Linux and darwin, unlike stat'ing /proc.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// The full suspend/lease-loss scenario end to end, with a real git worktree
// and a real subprocess standing in for the agent: force-losing the lease
// behind the engine's back must kill the agent process, commit and back up
// the dirty worktree, write no task status, and let the claim loop move on
// to other ready work.
func TestLeaseLossKillsAgentBacksUpAndContinuesClaiming(t *testing.T) {
	h := newHarness(t)
	pidFile := t.TempDir() + "/orphan.pid"
	inner := mustScenario(t,
		fake.Rule{Match: "Task: second-task", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "second.txt", Content: "second\n"}}},
	)
	p := &killableProvider{
		Fake: inner, orphanMatch: "Task: orphan-task",
		pidFile: pidFile, started: make(chan struct{}),
	}

	seeded := h.seed(contracts.Task{Title: "orphan-task"}, contracts.Task{Title: "second-task"})

	parsed, verr := template.Check([]byte(isolatedTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	root := t.TempDir()
	const ttl = 400 * time.Millisecond
	done := make(chan *Summary, 1)
	go func() {
		sum, err := Run(context.Background(), Config{
			Data: h.data, Provider: p, SessionID: h.sess.ID,
			Template: parsed, Cwd: h.cwd, Concurrency: 1,
			StageTimeout: 30 * time.Second, LeaseTTL: ttl,
			Isolated: true, WorktreeRoot: root, DataURL: h.base,
		})
		if err != nil {
			t.Error(err)
		}
		done <- sum
	}()

	select {
	case <-p.started:
	case <-time.After(10 * time.Second):
		t.Fatal("orphan agent subprocess never started")
	}
	pidBytes, err := waitForFile(t, pidFile)
	if err != nil {
		t.Fatalf("reading orphan pid file: %v", err)
	}
	var pid int
	if _, err := fmt.Sscanf(string(pidBytes), "%d", &pid); err != nil {
		t.Fatalf("parsing orphan pid %q: %v", pidBytes, err)
	}
	if !pidAlive(pid) {
		t.Fatalf("orphan pid %d not alive right after start", pid)
	}

	// Force-reassign the lease behind the engine's back, well past the TTL
	// so the very next renewal is rejected outright.
	time.Sleep(2 * ttl)
	thief := dataclient.New(h.base, "thief")
	if err := h.data.Release(context.Background(), seeded[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := thief.Claim(context.Background(), seeded[0].ID, "", time.Minute); err != nil {
		t.Fatal(err)
	}

	sum := <-done
	if len(sum.Done) != 1 || sum.Done[0] != seeded[1].ID {
		t.Fatalf("summary: %+v; orphan: %+v; second: %+v", sum, h.task(t, seeded[0].ID), h.task(t, seeded[1].ID))
	}
	for _, id := range sum.Blocked {
		if id == seeded[0].ID {
			t.Fatalf("engine wrote a status for the orphaned task: %+v", sum)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for pidAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pidAlive(pid) {
		t.Errorf("orphan subprocess pid %d still alive after lease loss", pid)
	}

	branchHead := strings.TrimSpace(gitOut(t, h.cwd, "rev-parse", "mads/"+seeded[0].ID))
	backupHead := strings.TrimSpace(gitOut(t, h.cwd, "rev-parse", "backup/"+seeded[0].ID))
	if backupHead == "" || backupHead != branchHead {
		t.Errorf("backup/%s = %q, want it to match mads/%s = %q", seeded[0].ID, backupHead, seeded[0].ID, branchHead)
	}
	msg := gitOut(t, h.cwd, "log", "-1", "--format=%s", "backup/"+seeded[0].ID)
	if !strings.Contains(msg, "auto-commit") {
		t.Errorf("backup/%s HEAD isn't the dirty-worktree auto-commit: %q", seeded[0].ID, msg)
	}

	orphan := h.task(t, seeded[0].ID)
	if orphan.Status != contracts.StatusInProgress {
		t.Errorf("orphaned task status = %s, want in_progress (untouched — the thief owns it now)", orphan.Status)
	}
	if err := thief.Renew(context.Background(), seeded[0].ID); err != nil {
		t.Errorf("thief's lease was disturbed by the engine's abort: %v", err)
	}

	second := h.task(t, seeded[1].ID)
	if second.Status != contracts.StatusDone {
		t.Errorf("second task status = %s, want done (claim loop must continue after the abort)", second.Status)
	}
}

func waitForFile(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil && len(strings.TrimSpace(string(b))) > 0 {
			return b, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
