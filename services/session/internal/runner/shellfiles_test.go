package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// shellScriptProvider plays one turn of shell calls: each step announces a
// Bash call, runs its effect on disk, then reports the result — the order a
// real harness streams them in.
type shellScriptProvider struct {
	steps []shellStep
}

type shellStep struct {
	id, command string
	effect      func() // what the command does to the folder
}

func (p *shellScriptProvider) Name() string            { return "shell-script" }
func (p *shellScriptProvider) Matrix() provider.Matrix { return fake.New().Matrix() }
func (p *shellScriptProvider) OneShot(context.Context, provider.OneShotRequest) (*provider.OneShotResult, error) {
	return nil, provider.ErrUnsupported
}
func (p *shellScriptProvider) Transcript(context.Context, string) ([]provider.Message, error) {
	return nil, nil
}
func (p *shellScriptProvider) Start(context.Context, provider.StartOptions) (provider.Session, error) {
	return &shellScriptSession{steps: p.steps, events: make(chan provider.Event, 64)}, nil
}

type shellScriptSession struct {
	steps  []shellStep
	events chan provider.Event
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

func (s *shellScriptSession) Ref() string                   { return "shell-1" }
func (s *shellScriptSession) Mode() provider.Mode           { return provider.ModeACP }
func (s *shellScriptSession) Events() <-chan provider.Event { return s.events }
func (s *shellScriptSession) Interrupt(context.Context) error {
	return nil
}
func (s *shellScriptSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	return nil
}
func (s *shellScriptSession) Send(context.Context, string) error {
	s.once.Do(func() {
		go func() {
			for _, st := range s.steps {
				input, _ := json.Marshal(map[string]string{"command": st.command})
				s.events <- provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{
					ID: st.id, Name: "Bash", Input: input, Kind: provider.ToolKindExecute,
				}}
				if st.effect != nil {
					st.effect()
				}
				s.events <- provider.Event{Kind: provider.EventToolResult, Tool: &provider.ToolUse{ID: st.id}, Text: "ok"}
			}
			s.events <- provider.Event{Kind: provider.EventText, Text: "done"}
			s.events <- provider.Event{Kind: provider.EventTurnEnd}
		}()
	})
	return nil
}

// A session working in place: what its shell commands wrote reaches the
// ledger as its own work and the header counts it; a read-only command and
// a file someone else changed earlier don't.
func TestShellWritesReachTheLedgerInPlace(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	writeFile := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(body), 0o644); err != nil {
			t.Error(err)
		}
	}
	// Someone else's change, well before the session's command.
	writeFile("theirs.txt", "not the session's")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(repo, "theirs.txt"), past, past); err != nil {
		t.Fatal(err)
	}

	prov := &shellScriptProvider{steps: []shellStep{
		{id: "toolu_read", command: "git status --short && rg -n init README.md"},
		{id: "toolu_write", command: "python3 - <<'EOF'\n...\nEOF", effect: func() {
			writeFile("README.md", "rewritten by a script\n")
			writeFile("new.txt", "created by a script\n")
		}},
	}}
	reg := provider.NewRegistry()
	if err := reg.Register(prov); err != nil {
		t.Fatal(err)
	}
	base := datatest.Start(t)
	sess := createSession(t, base, contracts.Session{
		Name: "in place", Provider: prov.Name(), Dir: repo, Isolation: contracts.IsolationCurrentDir,
	})
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, reg)
	r.WorktreeRoot = t.TempDir()
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })
	if r.shell == nil {
		t.Fatal("no shell tracker for a session working in place")
	}

	ch, unsub := r.Subscribe()
	defer unsub()
	if err := r.Send(context.Background(), "fix the readme"); err != nil {
		t.Fatal(err)
	}
	drainText(t, ch)

	want := map[string]string{
		filepath.Join(repo, "README.md"): "modified",
		filepath.Join(repo, "new.txt"):   "added",
	}
	var rows []contracts.FileChange
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if rows, err = data.FileChanges(context.Background(), sess.ID); err != nil {
			t.Fatal(err)
		}
		if len(rows) >= len(want) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(rows) != len(want) {
		t.Fatalf("ledger rows %+v, want %v", rows, want)
	}
	for _, fc := range rows {
		if want[fc.Path] != fc.Change {
			t.Errorf("%s: change %q, want %q", fc.Path, fc.Change, want[fc.Path])
		}
		if fc.Origin != contracts.OriginToolUse || !fc.Attributed {
			t.Errorf("%s: origin %q attributed %v, want the session's own tool use", fc.Path, fc.Origin, fc.Attributed)
		}
	}

	for time.Now().Before(deadline) {
		rec, err := data.Session(context.Background(), sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.UncommittedCount == 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("header never counted the shell's two files")
}

// An isolated session already owns everything in its worktree: no tracker.
func TestNoShellTrackerForAnIsolatedSession(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	h := newWTHarness(t, contracts.Session{Dir: repo, Isolation: contracts.IsolationWorktree}, fake.Scenario{})
	if err := h.boot(); err != nil {
		t.Fatal(err)
	}
	if h.runner.shell != nil {
		t.Error("an isolated session started a shell tracker")
	}
}
