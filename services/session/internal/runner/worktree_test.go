package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// failingStart wraps the fake so its next Start fails — a provider that
// can't launch (bad binary, auth) after isolation already did its work.
type failingStart struct {
	*fake.Fake
	mu   sync.Mutex
	fail bool
}

func (f *failingStart) Start(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	f.mu.Lock()
	fail := f.fail
	f.fail = false
	f.mu.Unlock()
	if fail {
		return nil, errors.New("scripted start failure")
	}
	return f.Fake.Start(ctx, opts)
}

// wtHarness is one session against a real Data Service, restartable: each
// boot is a fresh Runner (a new session child) over the same provider, so a
// resume finds the conversation the last boot left.
type wtHarness struct {
	t      *testing.T
	data   *dataclient.Client
	reg    *provider.Registry
	prov   *failingStart
	id     string
	root   string
	runner *Runner
}

func newWTHarness(t *testing.T, sess contracts.Session, sc fake.Scenario) *wtHarness {
	t.Helper()
	base := datatest.Start(t)
	sess.Name, sess.Provider = "test", "fake"
	if err := dataclient.New(base, "test-bootstrap").CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	f, err := fake.NewWithScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	h := &wtHarness{t: t, data: dataclient.New(base, sess.ID), reg: provider.NewRegistry(),
		prov: &failingStart{Fake: f}, id: sess.ID, root: t.TempDir()}
	if err := h.reg.Register(h.prov); err != nil {
		t.Fatal(err)
	}
	return h
}

// boot starts a new Runner for the session, shutting down the previous one
// first, and returns Start's error.
func (h *wtHarness) boot() error {
	h.t.Helper()
	h.stop()
	r := New(h.id, h.data, h.reg)
	r.WorktreeRoot = h.root
	h.runner = r
	if err := r.Start(context.Background()); err != nil {
		return err
	}
	h.t.Cleanup(func() { _ = r.Shutdown(context.Background()) })
	return nil
}

func (h *wtHarness) stop() {
	if h.runner != nil {
		_ = h.runner.Shutdown(context.Background())
		h.runner = nil
	}
}

func (h *wtHarness) record() contracts.Session {
	h.t.Helper()
	s, err := h.data.Session(context.Background(), h.id)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *wtHarness) lastStart() provider.StartOptions {
	h.t.Helper()
	calls := h.prov.StartCalls()
	if len(calls) == 0 {
		h.t.Fatal("provider never started")
	}
	return calls[len(calls)-1]
}

// worktreeEvents returns the worktree.ensured payloads, oldest first.
func (h *wtHarness) worktreeEvents() []map[string]any {
	h.t.Helper()
	evs, err := h.data.Events(context.Background(), 0, h.id, []string{string(contracts.WorktreeEnsured)})
	if err != nil {
		h.t.Fatal(err)
	}
	out := make([]map[string]any, len(evs))
	for i, ev := range evs {
		if err := json.Unmarshal(ev.Payload, &out[i]); err != nil {
			h.t.Fatal(err)
		}
	}
	return out
}

// turn sends text and waits for its turn end.
func (h *wtHarness) turn(text string) {
	h.t.Helper()
	ch, unsub := h.runner.Subscribe()
	defer unsub()
	if err := h.runner.Send(context.Background(), text); err != nil {
		h.t.Fatalf("send: %v", err)
	}
	drainText(h.t, ch)
}

// sessionBranches lists the repo's session branches, one per line.
func sessionBranches(t *testing.T, repo string) string {
	t.Helper()
	return strings.TrimSpace(gitOut(t, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/mads/session/"))
}

// waitCounts polls the record until the header counts match — they are
// written in the background after the turn ends.
func (h *wtHarness) waitCounts(uncommitted, unlanded int) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := h.record()
		if s.UncommittedCount == uncommitted && s.UnlandedCount == unlanded {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("counts = %d uncommitted, %d unlanded; want %d, %d",
				s.UncommittedCount, s.UnlandedCount, uncommitted, unlanded)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// First start isolates, and the worktree is on the record before the
// provider starts: a provider that fails to launch leaves a record the next
// start picks straight back up, instead of a branch it trips over.
func TestFirstStartPersistsWorktreeBeforeProvider(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	h := newWTHarness(t, contracts.Session{Dir: repo, Isolation: contracts.IsolationWorktree}, fake.Scenario{})

	h.prov.fail = true
	if err := h.boot(); err == nil {
		t.Fatal("start succeeded; want the scripted provider failure")
	}
	rec := h.record()
	wantDir := filepath.Join(h.root, h.id)
	if rec.WorktreeDir != wantDir || rec.WorktreeBranch != "mads/session/"+h.id || rec.WorktreeBase != "main" {
		t.Fatalf("record after failed start: dir=%q branch=%q base=%q", rec.WorktreeDir, rec.WorktreeBranch, rec.WorktreeBase)
	}
	if rec.ProviderRef != "" {
		t.Fatalf("provider ref %q recorded by a start that failed", rec.ProviderRef)
	}

	if err := h.boot(); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if got := h.lastStart().Cwd; got != wantDir {
		t.Errorf("provider cwd = %q, want the worktree %q", got, wantDir)
	}
	rec = h.record()
	if rec.Cwd != wantDir || rec.WorktreeDir != wantDir {
		t.Errorf("record cwd=%q worktree=%q, want %q", rec.Cwd, rec.WorktreeDir, wantDir)
	}
	if b := sessionBranches(t, repo); b != "mads/session/"+h.id {
		t.Errorf("session branches = %q, want exactly one", b)
	}
	evs := h.worktreeEvents()
	if len(evs) != 1 || evs[0]["action"] != "created" || evs[0]["base"] != "main" {
		t.Errorf("worktree events = %v, want one created (the reuse is quiet)", evs)
	}
}

// A session that already ran in place, before isolation existed, stays in
// place: its provider resumes by folder.
func TestLegacyInPlaceSessionStaysInPlace(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	h := newWTHarness(t, contracts.Session{Dir: repo, Isolation: contracts.IsolationWorktree}, fake.Scenario{})

	// Its first life: isolation didn't exist yet.
	h.root = ""
	if err := h.boot(); err != nil {
		t.Fatal(err)
	}
	h.turn("echo hi") // the fake announces its ref with the first turn
	if h.record().ProviderRef == "" {
		t.Fatal("no provider ref after the first life")
	}

	h.root = t.TempDir()
	if err := h.boot(); err != nil {
		t.Fatal(err)
	}
	if got := h.lastStart().Cwd; got != repo {
		t.Errorf("provider cwd = %q, want the original folder %q", got, repo)
	}
	if rec := h.record(); rec.WorktreeDir != "" || rec.WorktreeBranch != "" {
		t.Errorf("legacy session gained a worktree: %+v", rec)
	}
	if b := sessionBranches(t, repo); b != "" {
		t.Errorf("legacy session got a branch: %q", b)
	}
}

// The folder was deleted by hand between runs: resume recreates it at the
// same path from the surviving branch, and says so.
func TestResumeRecreatesDeletedWorktree(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	h := newWTHarness(t, contracts.Session{Dir: repo, Isolation: contracts.IsolationWorktree}, fake.Scenario{})
	if err := h.boot(); err != nil {
		t.Fatal(err)
	}
	dir := h.record().WorktreeDir
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("w\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "work.txt")
	gitRun(t, dir, "commit", "-q", "-m", "session work")

	h.stop()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := h.boot(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := h.lastStart().Cwd; got != dir {
		t.Errorf("provider cwd = %q, want the same path %q", got, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "work.txt")); err != nil {
		t.Errorf("committed work not back: %v", err)
	}
	evs := h.worktreeEvents()
	if last := evs[len(evs)-1]; last["action"] != "reattached" {
		t.Errorf("last worktree event = %v, want reattached", last)
	}
}

// No branch checked out in the primary means nothing to land into: run in
// place, and say why.
func TestDetachedHeadPrimaryIsNotIsolated(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	gitRun(t, repo, "checkout", "-q", "--detach")
	h := newWTHarness(t, contracts.Session{Dir: repo, Isolation: contracts.IsolationWorktree}, fake.Scenario{})
	if err := h.boot(); err != nil {
		t.Fatal(err)
	}
	if got := h.lastStart().Cwd; got != repo {
		t.Errorf("provider cwd = %q, want %q in place", got, repo)
	}
	if rec := h.record(); rec.WorktreeDir != "" {
		t.Errorf("detached primary isolated anyway: %q", rec.WorktreeDir)
	}
	evs := h.worktreeEvents()
	if len(evs) != 1 || evs[0]["action"] != "not_isolated" || evs[0]["reason"] != "detached_head" {
		t.Errorf("worktree events = %v, want one not_isolated/detached_head", evs)
	}
}

// A session that asked for no folder runs wherever the Router was started —
// never isolated off that repo, even though it may be one (this test's own
// cwd is inside the mads checkout).
func TestImplicitCwdIsNotIsolated(t *testing.T) {
	h := newWTHarness(t, contracts.Session{Isolation: contracts.IsolationWorktree}, fake.Scenario{})
	if err := h.boot(); err != nil {
		t.Fatal(err)
	}
	if got := h.lastStart().Cwd; got != "." {
		t.Errorf("provider cwd = %q, want the implicit \".\"", got)
	}
	if rec := h.record(); rec.WorktreeDir != "" {
		t.Errorf("implicit cwd isolated: %q", rec.WorktreeDir)
	}
}

// The header counts follow the session's work: recomputed at turn end, from
// the ledger and git, for an isolated session.
func TestCountsRecomputedAtTurnEnd(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	sc := fake.Scenario{Rules: []fake.Rule{
		{Match: "write", Reply: "wrote", Tools: []fake.ToolStep{
			{Name: "write_file", Path: "new.txt", Content: "new\n"},
			{Name: "write_file", Path: "README.md", Content: "changed\n"},
		}},
	}}
	h := newWTHarness(t, contracts.Session{Dir: repo, Isolation: contracts.IsolationWorktree}, sc)
	if err := h.boot(); err != nil {
		t.Fatal(err)
	}
	h.waitCounts(0, 0)

	h.turn("write")
	h.waitCounts(2, 0) // one created (untracked), one modified

	dir := h.record().WorktreeDir
	gitRun(t, dir, "add", "new.txt")
	gitRun(t, dir, "commit", "-q", "-m", "keep new.txt")
	gitRun(t, dir, "checkout", "--", "README.md") // reverted: no longer counts
	h.turn("echo hi")
	h.waitCounts(0, 1)
}

// Working in place, the counts come from the user's checkout and nothing is
// ever "unlanded".
func TestCountsInPlace(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	sc := fake.Scenario{Rules: []fake.Rule{
		{Match: "write", Reply: "wrote", Tools: []fake.ToolStep{{Name: "write_file", Path: "README.md", Content: "changed\n"}}},
	}}
	h := newWTHarness(t, contracts.Session{Dir: repo, Isolation: contracts.IsolationCurrentDir}, sc)
	if err := h.boot(); err != nil {
		t.Fatal(err)
	}
	// Someone else's edit in the same checkout isn't the session's.
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("o\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.turn("write")
	h.waitCounts(1, 0)
}
