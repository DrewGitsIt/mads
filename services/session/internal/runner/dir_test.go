package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// startInDir boots a runner whose session record asks for dir, with -cwd
// left at its "." default, and returns the options the provider started with
// plus the record as Start left it.
func startInDir(t *testing.T, dir, isolation, worktreeRoot string) (provider.StartOptions, contracts.Session) {
	t.Helper()
	ctx := context.Background()
	base := datatest.Start(t)
	bootstrap := dataclient.New(base, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake", Dir: dir, Isolation: isolation}
	if err := bootstrap.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	data := dataclient.New(base, sess.ID)
	reg := provider.NewRegistry()
	fp := fake.New()
	if err := reg.Register(fp); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r := New(sess.ID, data, reg)
	r.WorktreeRoot = worktreeRoot
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })
	got, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	calls := fp.StartCalls()
	if len(calls) != 1 {
		t.Fatalf("start calls = %d, want 1", len(calls))
	}
	return calls[0], got
}

// The folder a session asked for is where the agent runs — not whatever
// directory the Session Service inherited from the Router.
func TestStartRunsInTheRequestedDir(t *testing.T) {
	dir := t.TempDir()
	opts, sess := startInDir(t, dir, contracts.IsolationCurrentDir, t.TempDir())
	if opts.Cwd != dir {
		t.Errorf("provider cwd = %q, want the requested dir %q", opts.Cwd, dir)
	}
	if sess.Cwd != dir {
		t.Errorf("recorded cwd = %q, want %q", sess.Cwd, dir)
	}
}

// Worktree isolation branches from the requested dir when it is a repo.
func TestStartIsolatesTheRequestedRepo(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	worktreeRoot := t.TempDir()
	opts, sess := startInDir(t, repo, contracts.IsolationWorktree, worktreeRoot)
	if sess.WorktreeDir == "" || opts.Cwd != sess.WorktreeDir {
		t.Fatalf("provider cwd = %q, worktree = %q; want the session worktree", opts.Cwd, sess.WorktreeDir)
	}
	if wt := gitOut(t, repo, "worktree", "list"); !strings.Contains(wt, sess.WorktreeDir) {
		t.Errorf("worktree %q not registered against %q:\n%s", sess.WorktreeDir, repo, wt)
	}
}

// A plain folder under worktree isolation runs in place — the case the
// Launchpad warns about before Start.
func TestStartRunsInAPlainRequestedDirUnderWorktreeIsolation(t *testing.T) {
	dir := t.TempDir()
	opts, sess := startInDir(t, dir, contracts.IsolationWorktree, t.TempDir())
	if opts.Cwd != dir || sess.WorktreeDir != "" {
		t.Errorf("provider cwd = %q, worktree = %q; want %q in place", opts.Cwd, sess.WorktreeDir, dir)
	}
}
