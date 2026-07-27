package pipelinerun

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// newTestWorkspace sets up a minimal git repo + Makefile — good enough for
// engine.Run to complete against a template with no seeded tasks (mirrors
// services/pipeline/engine's own test harness, which needs the same thing
// for the default-implementation template's command stages).
func newTestWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"),
		[]byte("build:\n\t@true\ntest:\n\t@true\ncheck:\n\t@true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "add", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func loadDefaultTemplate(t *testing.T, data *dataclient.Client) *template.Template {
	t.Helper()
	rec, err := data.Template(context.Background(), "default-implementation")
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	tpl, verr := template.Check([]byte(rec.Source), func(string) bool { return true })
	if verr != nil {
		t.Fatalf("template.Check: %v", verr)
	}
	return tpl
}

// The CLI (`mads run`) case — Options.ManagedExternally left at its zero
// value — must still deactivate its session once the run ends: no
// interactive child exists for it to piggyback on, and that deactivation is
// deliberate (commit 81ba055), not something splitting out
// ManagedExternally for the Router-spawned case should have changed.
func TestRunDeactivatesSessionWhenNotManagedExternally(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "pipeline")

	sess := contracts.Session{Name: "cli run", Workspace: "test"}
	if err := data.CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, err := Run(context.Background(), Options{
		Data: data, Provider: fake.New(), SessionID: sess.ID,
		Template: loadDefaultTemplate(t, data), Cwd: newTestWorkspace(t),
		Concurrency: 2, StageTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := data.Session(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if got.Activity != contracts.SessionInactive {
		t.Fatalf("Activity after non-managed Run with no tasks = %q, want inactive", got.Activity)
	}
}

// Options.ManagedExternally must be honored: Run's own deactivate-on-exit
// defer must not run at all, leaving Activity exactly as the caller left it.
func TestRunManagedExternallySkipsDeactivation(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "pipeline")

	sess := contracts.Session{Name: "router-spawned run"}
	if err := data.CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, err := Run(context.Background(), Options{
		Data: data, Provider: fake.New(), SessionID: sess.ID,
		Template: loadDefaultTemplate(t, data), Cwd: newTestWorkspace(t),
		Concurrency: 2, StageTimeout: 30 * time.Second,
		ManagedExternally: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := data.Session(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if got.Activity != contracts.SessionActive {
		t.Fatalf("Activity after ManagedExternally Run = %q, want active (Run must not deactivate)", got.Activity)
	}
}

// activateSession must not clobber a concurrent writer of the same session
// record (e.g. a live interactive child persisting its own ProviderRef under
// services/session/internal/runner.go's recordMu, runner.go:319-320) with a
// stale copy read before that write landed. Races a concurrent writer
// against activateSession itself (isolated from engine.Run/the deactivation
// defer, neither of which touch ProviderRef, so nothing else could mask a
// failure here) across many trials to make the race land reliably. The
// writer sets Activity=active alongside ProviderRef, mirroring runner.go's
// real write exactly (both fields land in the one call) — this store does
// whole-record overwrites, so if the writer only set ProviderRef, the
// writer's own staleness could spuriously revert Activity depending on
// ordering, which isn't the bug this test is targeting. The old
// single-read implementation had a wide read-to-write window and would
// eventually revert ProviderRef to its pre-race value; the fixed
// re-read-immediately-before-write implementation narrows that window to
// effectively nothing.
func TestActivateSessionDoesNotClobberConcurrentWrite(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "pipeline")
	writer := dataclient.New(base, "session-child")

	sess := contracts.Session{Name: "race test session", Activity: contracts.SessionInactive}
	if err := data.CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	const trials = 30
	for i := range trials {
		ctx := context.Background()

		cur, err := data.Session(ctx, sess.ID)
		if err != nil {
			t.Fatalf("trial %d: Session (reset): %v", i, err)
		}
		cur.Activity = contracts.SessionInactive
		cur.ProviderRef = "baseline"
		if err := data.UpdateSession(ctx, &cur); err != nil {
			t.Fatalf("trial %d: UpdateSession (reset): %v", i, err)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			racer, err := writer.Session(ctx, sess.ID)
			if err != nil {
				return
			}
			racer.Activity = contracts.SessionActive
			racer.ProviderRef = fmt.Sprintf("racer-%d", i)
			_ = writer.UpdateSession(ctx, &racer)
		}(i)

		close(start)
		if err := activateSession(ctx, data, sess.ID); err != nil {
			t.Fatalf("trial %d: activateSession: %v", i, err)
		}
		wg.Wait()

		got, err := data.Session(ctx, sess.ID)
		if err != nil {
			t.Fatalf("trial %d: Session (final): %v", i, err)
		}
		if got.Activity != contracts.SessionActive {
			t.Fatalf("trial %d: Activity = %q, want active", i, got.Activity)
		}
		if got.ProviderRef == "baseline" {
			t.Fatalf("trial %d: ProviderRef = %q, want the concurrent writer's value — activation clobbered it with a stale pre-write copy", i, got.ProviderRef)
		}
	}
}
