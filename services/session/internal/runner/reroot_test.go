package runner

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// defaultWorkspace registers a workspace whose primary repo is dir and makes
// it Config.DefaultWorkspace. An empty dir leaves the workspace without a
// primary repo.
func defaultWorkspace(t *testing.T, base, dir string) contracts.Workspace {
	t.Helper()
	ctx := context.Background()
	admin := dataclient.New(base, "test-bootstrap")
	ws := contracts.Workspace{Name: "home"}
	if dir != "" {
		ws.Repos = []contracts.WorkspaceRepo{{Path: dir, Primary: true}}
	}
	if err := admin.CreateWorkspace(ctx, &ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	cfg, err := admin.Config(ctx)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.DefaultWorkspace = ws.ID
	if err := admin.UpdateConfig(ctx, &cfg); err != nil {
		t.Fatalf("set default workspace: %v", err)
	}
	return ws
}

// goneDir is a folder under a live temp dir that was never made — as far as
// the runner can tell, a deleted one.
func goneDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "old-project")
}

func newRerootRunner(t *testing.T, base string, f *fake.Fake, sess contracts.Session) (*Runner, *dataclient.Client) {
	t.Helper()
	data := dataclient.New(base, sess.ID)
	return New(sess.ID, data, registryOf(t, f)), data
}

func createSession(t *testing.T, base string, sess contracts.Session) contracts.Session {
	t.Helper()
	if err := dataclient.New(base, "test-bootstrap").CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

// userTexts is every user turn the provider received on ref, verbatim.
func userTexts(t *testing.T, f *fake.Fake, ref string) []string {
	t.Helper()
	var out []string
	for _, m := range nativeHistory(t, f, ref) {
		if m.Role == "user" {
			out = append(out, m.Text)
		}
	}
	return out
}

func sendAndWait(t *testing.T, r *Runner, text string) {
	t.Helper()
	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()
	if err := r.Send(context.Background(), text); err != nil {
		t.Fatalf("send %q: %v", text, err)
	}
	drainText(t, ch)
}

func TestRerootMovesAMissingFolderToTheDefaultWorkspace(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	repo := t.TempDir()
	ws := defaultWorkspace(t, base, repo)
	gone := goneDir(t)
	f := fake.New()
	sess := createSession(t, base, contracts.Session{Name: "orphan", Provider: "fake", Dir: gone, Isolation: contracts.IsolationCurrentDir})
	r, data := newRerootRunner(t, base, f, sess)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	if calls := f.StartCalls(); len(calls) != 1 || calls[0].Cwd != repo {
		t.Fatalf("provider start = %+v, want one start in %q", calls, repo)
	}
	got, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != repo || got.Cwd != repo || got.RerootedFrom != gone || got.Workspace != ws.ID || !slices.Contains(got.Tags, "rerooted") {
		t.Errorf("record = {dir:%q cwd:%q rerooted_from:%q workspace:%q tags:%v}, want dir/cwd %q, rerooted_from %q, workspace %q, tag rerooted",
			got.Dir, got.Cwd, got.RerootedFrom, got.Workspace, got.Tags, repo, gone, ws.ID)
	}
}

// The note rides the first turn only — not later turns, not a later wake.
func TestRerootNoteIsDeliveredExactlyOnce(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	repo := t.TempDir()
	defaultWorkspace(t, base, repo)
	gone := goneDir(t)
	f := fake.New()
	sess := createSession(t, base, contracts.Session{Name: "orphan", Provider: "fake", Dir: gone, Isolation: contracts.IsolationCurrentDir})
	r, data := newRerootRunner(t, base, f, sess)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	sendAndWait(t, r, "echo one")
	sendAndWait(t, r, "echo two")

	got := waitForRecord(t, data, sess.ID, func(s contracts.Session) bool { return s.ProviderRef != "" && !s.RerootNotePending })
	if err := r.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	texts := userTexts(t, f, got.ProviderRef)
	if len(texts) != 2 {
		t.Fatalf("user turns = %q, want 2", texts)
	}
	if !strings.Contains(texts[0], gone) || !strings.Contains(texts[0], repo) || !strings.HasSuffix(texts[0], "echo one") {
		t.Errorf("first turn = %q, want the note (naming %q and %q) in front of the user's text", texts[0], gone, repo)
	}
	if texts[1] != "echo two" {
		t.Errorf("second turn = %q, want it untouched", texts[1])
	}

	// A later wake: Dir exists now and the note is no longer owed.
	r2, _ := newRerootRunner(t, base, f, sess)
	if err := r2.Start(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}
	t.Cleanup(func() { _ = r2.Shutdown(ctx) })
	sendAndWait(t, r2, "echo three")
	texts = userTexts(t, f, got.ProviderRef)
	if last := texts[len(texts)-1]; last != "echo three" {
		t.Errorf("turn after a later wake = %q, want it untouched", last)
	}
}

// A wake that finds the note still owed (the process died before the clear
// landed) delivers it again: at least once, never zero.
func TestRerootNoteStillOwedAfterAWakeIsDelivered(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	f := fake.New()
	sess := createSession(t, base, contracts.Session{
		Name: "orphan", Provider: "fake", Dir: t.TempDir(), Isolation: contracts.IsolationCurrentDir,
		RerootedFrom: "old-place", RerootNotePending: true, Tags: []string{"rerooted"},
	})
	r, data := newRerootRunner(t, base, f, sess)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	sendAndWait(t, r, "echo hi")
	got := waitForRecord(t, data, sess.ID, func(s contracts.Session) bool { return s.ProviderRef != "" && !s.RerootNotePending })
	texts := userTexts(t, f, got.ProviderRef)
	if len(texts) != 1 || !strings.Contains(texts[0], "old-place") {
		t.Errorf("user turns = %q, want the note naming old-place on the first", texts)
	}
}

// An injection from a linked session is the first turn here, and carries the
// note; the next turn does not.
func TestRerootNoteRidesAnInjectedFirstTurn(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	f := fake.New()
	sess := createSession(t, base, contracts.Session{
		Name: "orphan", Provider: "fake", Dir: t.TempDir(), Isolation: contracts.IsolationCurrentDir,
		RerootedFrom: "old-place", RerootNotePending: true,
	})
	r, data := newRerootRunner(t, base, f, sess)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	ch, unsubscribe := r.Subscribe()
	if err := r.Inject(ctx, "echo from a friend", contracts.Attribution{LinkID: "lnk_1", SourceName: "friend"}); err != nil {
		t.Fatalf("inject: %v", err)
	}
	drainText(t, ch)
	unsubscribe()
	sendAndWait(t, r, "echo mine")
	got := waitForRecord(t, data, sess.ID, func(s contracts.Session) bool { return s.ProviderRef != "" && !s.RerootNotePending })
	texts := userTexts(t, f, got.ProviderRef)
	if len(texts) != 2 || !strings.Contains(texts[0], "old-place") || texts[1] != "echo mine" {
		t.Errorf("user turns = %q, want the note on the injection only", texts)
	}
}

// A queued user message that becomes the first turn carries the note.
func TestRerootNoteRidesAQueuedFirstTurn(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	f := fake.New()
	sess := createSession(t, base, contracts.Session{
		Name: "orphan", Provider: "fake", Dir: t.TempDir(), Isolation: contracts.IsolationCurrentDir,
		RerootedFrom: "old-place", RerootNotePending: true,
	})
	r, data := newRerootRunner(t, base, f, sess)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	// Hold a turn open so the next message queues, then let it deliver.
	r.mu.Lock()
	r.busy = true
	r.mu.Unlock()
	if q, err := r.SendOrQueue(ctx, "echo queued"); err != nil || q == nil {
		t.Fatalf("SendOrQueue = %v, %v, want it queued", q, err)
	}
	ch, unsubscribe := r.Subscribe()
	r.onTurnEnd()
	drainText(t, ch)
	unsubscribe()
	got := waitForRecord(t, data, sess.ID, func(s contracts.Session) bool { return s.ProviderRef != "" && !s.RerootNotePending })
	texts := userTexts(t, f, got.ProviderRef)
	if len(texts) != 1 || !strings.Contains(texts[0], "old-place") || !strings.HasSuffix(texts[0], "echo queued") {
		t.Errorf("user turns = %q, want the note in front of the queued message", texts)
	}
}

func TestRerootLeavesAnExistingFolderAlone(t *testing.T) {
	dir := t.TempDir()
	opts, sess := startInDir(t, dir, contracts.IsolationCurrentDir, t.TempDir())
	if opts.Cwd != dir || sess.Dir != dir || sess.RerootedFrom != "" || sess.RerootNotePending || slices.Contains(sess.Tags, "rerooted") {
		t.Errorf("cwd %q, record %+v; want %q untouched and no re-root", opts.Cwd, sess, dir)
	}
}

func TestRerootFailsWithoutAUsableDefaultWorkspace(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(t *testing.T, base string){
		"no default workspace": func(*testing.T, string) {},
		"no primary repo":      func(t *testing.T, base string) { defaultWorkspace(t, base, "") },
		"primary repo gone":    func(t *testing.T, base string) { defaultWorkspace(t, base, goneDir(t)) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			base := datatest.Start(t)
			setup(t, base)
			f := fake.New()
			gone := goneDir(t)
			sess := createSession(t, base, contracts.Session{Name: "orphan", Provider: "fake", Dir: gone})
			r, data := newRerootRunner(t, base, f, sess)
			err := r.Start(ctx)
			if err == nil || !strings.Contains(err.Error(), gone) {
				t.Fatalf("start error = %v, want one naming the missing folder %q", err, gone)
			}
			if calls := f.StartCalls(); len(calls) != 0 {
				t.Errorf("provider started %d time(s) (cwd %q), want no spawn", len(calls), calls[0].Cwd)
			}
			got, _ := data.Session(ctx, sess.ID)
			if got.Dir != gone || got.RerootedFrom != "" {
				t.Errorf("record dir %q rerooted_from %q, want it untouched", got.Dir, got.RerootedFrom)
			}
		})
	}
}

// Worktree isolation branches from the new folder, not the gone one.
func TestRerootWorktreeIsolationBranchesFromTheNewFolder(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	repo := t.TempDir()
	initRepo(t, repo)
	defaultWorkspace(t, base, repo)
	f := fake.New()
	sess := createSession(t, base, contracts.Session{Name: "orphan", Provider: "fake", Dir: goneDir(t), Isolation: contracts.IsolationWorktree})
	r, data := newRerootRunner(t, base, f, sess)
	r.WorktreeRoot = t.TempDir()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	sess, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	opts := f.StartCalls()[0]
	if sess.WorktreeDir == "" || opts.Cwd != sess.WorktreeDir {
		t.Fatalf("provider cwd %q, worktree %q; want the session worktree", opts.Cwd, sess.WorktreeDir)
	}
	if wt := gitOut(t, repo, "worktree", "list"); !strings.Contains(wt, sess.WorktreeDir) {
		t.Errorf("worktree %q not registered against the new folder %q:\n%s", sess.WorktreeDir, repo, wt)
	}
}

// A session whose worktree is still there runs in it, even though the folder
// it was created from has vanished, and is not re-rooted.
func TestRerootSkippedWhileTheWorktreeExists(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	defaultWorkspace(t, base, t.TempDir())
	wtDir := t.TempDir()
	f := fake.New()
	sess := createSession(t, base, contracts.Session{Name: "wt", Provider: "fake", Dir: goneDir(t), Isolation: contracts.IsolationWorktree})
	data := dataclient.New(base, sess.ID)
	if err := data.SetSessionWorktree(ctx, sess.ID, contracts.SessionWorktreePatch{WorktreeDir: &wtDir}); err != nil {
		t.Fatal(err)
	}
	r, _ := newRerootRunner(t, base, f, sess)
	r.WorktreeRoot = t.TempDir()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	got, _ := data.Session(ctx, sess.ID)
	if calls := f.StartCalls(); len(calls) != 1 || calls[0].Cwd != wtDir {
		t.Errorf("provider start = %+v, want cwd %q", calls, wtDir)
	}
	if got.RerootedFrom != "" || got.WorktreeDir != wtDir {
		t.Errorf("record rerooted_from %q worktree %q, want no re-root and the worktree kept", got.RerootedFrom, got.WorktreeDir)
	}
}
