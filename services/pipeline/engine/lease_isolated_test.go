package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
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
	cwd         string // the orphan's worktree; written before started closes
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
	p.cwd = req.Cwd
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

// orphanRun is one isolated run over an "orphan" task (a real subprocess
// agent that only a kill ends) and a "second" task (the fake), under run
// id orphanRunID, so its leases are held by "pipeline:"+orphanRunID.
type orphanRun struct {
	h      *harness
	p      *killableProvider
	seeded []contracts.Task // orphan, second
	done   chan *Summary
	pid    int
}

const orphanRunID = "run_orphan"

// startOrphanRun starts the run with engineData as the engine's client
// and returns once the orphan's agent subprocess is alive.
func startOrphanRun(t *testing.T, h *harness, engineData *dataclient.Client, ttl time.Duration) *orphanRun {
	t.Helper()
	inner := mustScenario(t,
		fake.Rule{Match: "Task: second-task", Reply: "done",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "second.txt", Content: "second\n"}}},
	)
	pidFile := t.TempDir() + "/orphan.pid"
	r := &orphanRun{
		h: h,
		p: &killableProvider{
			Fake: inner, orphanMatch: "Task: orphan-task",
			pidFile: pidFile, started: make(chan struct{}),
		},
		seeded: h.seed(contracts.Task{Title: "orphan-task"}, contracts.Task{Title: "second-task"}),
		done:   make(chan *Summary, 1),
	}
	parsed, verr := template.Check([]byte(isolatedTemplate), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	root := t.TempDir()
	go func() {
		sum, err := Run(context.Background(), Config{
			Data: engineData, Provider: r.p, SessionID: h.sess.ID, RunID: orphanRunID,
			Template: parsed, Cwd: h.cwd, Concurrency: 1,
			StageTimeout: 30 * time.Second, LeaseTTL: ttl,
			Isolated: true, WorktreeRoot: root, DataURL: h.base,
		})
		if err != nil {
			t.Error(err)
		}
		r.done <- sum
	}()

	select {
	case <-r.p.started:
	case <-time.After(10 * time.Second):
		t.Fatal("orphan agent subprocess never started")
	}
	pidBytes, err := waitForFile(t, pidFile)
	if err != nil {
		t.Fatalf("reading orphan pid file: %v", err)
	}
	if _, err := fmt.Sscanf(string(pidBytes), "%d", &r.pid); err != nil {
		t.Fatalf("parsing orphan pid %q: %v", pidBytes, err)
	}
	if !pidAlive(r.pid) {
		t.Fatalf("orphan pid %d not alive right after start", r.pid)
	}
	return r
}

// waitAgentKilled fails the test unless the orphan's subprocess dies.
func (r *orphanRun) waitAgentKilled(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for pidAlive(r.pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pidAlive(r.pid) {
		t.Fatalf("orphan subprocess pid %d still alive after lease loss", r.pid)
	}
}

// finish waits for the run and checks what holds either way: the claim loop
// moved on and finished the second task, the engine wrote no status for
// the orphan (in_progress: Interrupted), and the thief's lease is intact.
func (r *orphanRun) finish(t *testing.T, thief *dataclient.Client) {
	t.Helper()
	h, orphanID, secondID := r.h, r.seeded[0].ID, r.seeded[1].ID
	sum := <-r.done
	if len(sum.Done) != 1 || sum.Done[0] != secondID {
		t.Fatalf("summary: %+v; orphan: %+v; second: %+v", sum, h.task(t, orphanID), h.task(t, secondID))
	}
	for _, id := range sum.Blocked {
		if id == orphanID {
			t.Fatalf("engine wrote a status for the orphaned task: %+v", sum)
		}
	}
	if got := h.task(t, orphanID); got.Status != contracts.StatusInProgress {
		t.Errorf("orphaned task status = %s, want in_progress (lease loss changes no status)", got.Status)
	}
	if err := thief.Renew(context.Background(), orphanID); err != nil {
		t.Errorf("thief's lease was disturbed by the engine's abort: %v", err)
	}
	if got := h.task(t, secondID); got.Status != contracts.StatusDone {
		t.Errorf("second task status = %s, want done (claim loop must continue after the abort)", got.Status)
	}
}

// branchExists reports whether refs/heads/<branch> exists in dir's repo.
func branchExists(dir, branch string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = dir
	return cmd.Run() == nil
}

// The full lease-loss scenario end to end, with a real git worktree and a
// real subprocess standing in for the agent, losing the lease the way it
// really happens: the Router's tokenless ReleaseAll on the run's holder
// (supervisor.onPipelineExit), from a client of its own. The engine learns
// of it on its next renewal and, with the lease still free, must kill the
// agent, commit and back up the dirty worktree, write no task status, and
// let the claim loop move on. Only after the snapshot does a thief take
// the task; the engine must leave the thief's lease alone.
func TestLeaseLossKillsAgentBacksUpAndContinuesClaiming(t *testing.T) {
	h := newHarness(t)
	const ttl = 400 * time.Millisecond
	r := startOrphanRun(t, h, h.data, ttl)
	orphanID := r.seeded[0].ID

	h.releaseRun(t, orphanRunID)

	// The engine notices within a heartbeat (ttl/3), kills the agent, and
	// snapshots under a short claim of its own. The backup branch appears
	// only once that snapshot has run, so waiting for it sequences the
	// thief after the engine's decision deterministically.
	r.waitAgentKilled(t)
	deadline := time.Now().Add(10 * time.Second)
	for !branchExists(h.cwd, "backup/"+orphanID) {
		if time.Now().After(deadline) {
			t.Fatalf("backup/%s never appeared after the lease loss", orphanID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The engine releases its snapshot claim right after the backup; until
	// then the thief's claim conflicts.
	thief := dataclient.New(h.base, "thief")
	for {
		err := thief.Claim(context.Background(), orphanID, "", time.Minute)
		if err == nil {
			break
		}
		if !errors.Is(err, dataclient.ErrConflict) || time.Now().After(deadline) {
			t.Fatalf("thief claim after the snapshot: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	r.finish(t, thief)

	branchHead := strings.TrimSpace(gitOut(t, h.cwd, "rev-parse", "mads/"+orphanID))
	backupHead := strings.TrimSpace(gitOut(t, h.cwd, "rev-parse", "backup/"+orphanID))
	if backupHead == "" || backupHead != branchHead {
		t.Errorf("backup/%s = %q, want it to match mads/%s = %q", orphanID, backupHead, orphanID, branchHead)
	}
	msg := gitOut(t, h.cwd, "log", "-1", "--format=%s", "backup/"+orphanID)
	if !strings.Contains(msg, "auto-commit") {
		t.Errorf("backup/%s HEAD isn't the dirty-worktree auto-commit: %q", orphanID, msg)
	}
}

// renewGate fronts the data service for the engine's client and can hold
// its lease renewals, so a test can change the lease between two
// heartbeats with no renewal landing in between.
type renewGate struct {
	mu  sync.RWMutex // renewals hold it shared; close takes it exclusively
	url string
}

func newRenewGate(t *testing.T, base string) *renewGate {
	t.Helper()
	target, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	g := &renewGate{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/lease/renew") {
			g.mu.RLock()
			defer g.mu.RUnlock()
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	g.url = srv.URL
	return g
}

// The woken-laptop case (docs/PIPELINE-LEASES-DESIGN.md A3): by the time
// the engine learns its lease is gone, another run already holds the task.
// That run owns the worktree now, so the engine must still kill its agent
// and stop, but must not snapshot: no stray commit, no backup branch, and
// the dirty file left for the new holder to commit on pickup.
func TestLeaseLossSkipsSnapshotWhenAnotherRunHoldsTask(t *testing.T) {
	h := newHarness(t)
	gate := newRenewGate(t, h.base)
	engineData := dataclient.New(gate.url, "pipeline")
	const ttl = 400 * time.Millisecond
	r := startOrphanRun(t, h, engineData, ttl)
	orphanID := r.seeded[0].ID

	// Hold renewals (waiting out any in flight), lose the lease and let
	// another run take it, then let the engine's next renewal through.
	gate.mu.Lock()
	h.releaseRun(t, orphanRunID)
	thief := dataclient.New(h.base, "pipeline")
	if err := thief.Claim(context.Background(), orphanID, "pipeline:run_thief", time.Minute); err != nil {
		gate.mu.Unlock()
		t.Fatal(err)
	}
	gate.mu.Unlock()

	r.waitAgentKilled(t)
	r.finish(t, thief)

	if branchExists(h.cwd, "backup/"+orphanID) {
		t.Errorf("backup/%s exists: the engine snapshotted a worktree another run holds", orphanID)
	}
	if msg := gitOut(t, h.cwd, "log", "-1", "--format=%s", "mads/"+orphanID); strings.Contains(msg, "auto-commit") {
		t.Errorf("mads/%s HEAD is an auto-commit: the engine wrote a worktree another run holds", orphanID)
	}
	if status := gitOut(t, r.p.cwd, "status", "--porcelain"); !strings.Contains(status, "dirty.txt") {
		t.Errorf("worktree status = %q, want dirty.txt still uncommitted for the new holder", status)
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
