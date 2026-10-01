package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// shadowCwd is a session cwd in the canonical form the shadow store records
// (shadow.RealPath). These tests derive a RootKey from the cwd to read the
// store back, so under a symlinked TMPDIR — every macOS temp dir, since /var
// points into /private/var — a raw t.TempDir() keys a root the store never
// wrote.
func shadowCwd(t *testing.T) string {
	t.Helper()
	return shadow.RealPath(t.TempDir())
}

// TestShadowProvisioning: with ShadowRoot set, Start records the session's
// shadow_dir on the session record and baselines the primary root in the
// background; a turn whose scripted tool writes a file yields a turn
// commit in the shadow store carrying the new content.
func TestShadowProvisioning(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	f, err := fake.NewWithScenario(fake.Scenario{
		Name: "writes",
		Rules: []fake.Rule{
			{Match: "write it", Reply: "done", Tools: []fake.ToolStep{
				{Name: "write_file", Path: "foo.txt", Content: "hello"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("scenario: %v", err)
	}
	reg := provider.NewRegistry()
	if err := reg.Register(f); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	r := New(sess.ID, data, reg)
	r.Cwd = shadowCwd(t)
	r.ShadowRoot = t.TempDir()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	t.Cleanup(r.shadowStore.Wait)

	got, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	wantDir := filepath.Join(r.ShadowRoot, sess.ID)
	if got.ShadowDir != wantDir {
		t.Fatalf("shadow_dir = %q, want %q", got.ShadowDir, wantDir)
	}

	// The primary-root baseline runs in a goroutine off Start — poll.
	waitFor(t, "primary root baseline", func() bool {
		_, ok := shadow.BaselineOriginal(ctx, wantDir, r.Cwd, "foo.txt")
		return ok
	})

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()
	if err := r.Send(ctx, "write it"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)

	// The turn commit is async off turn end — poll the index for it, then
	// read the written file back out of the commit itself.
	waitFor(t, "turn commit", func() bool {
		recs, err := shadow.ReadIndex(wantDir)
		if err != nil {
			return false
		}
		for _, rec := range recs {
			if rec.Kind != shadow.RecordTurnCommit {
				continue
			}
			content, err := shadow.FileAtCommit(ctx, wantDir, rec.RootKey, rec.Commit, "foo.txt")
			return err == nil && content == "hello"
		}
		return false
	})
}

// TestShadowCaptureRecordsPreImage drives shadowCapture directly (the fake
// provider's tool events carry no wire tool_use ID), the same
// direct-drive pattern TestFileChangeClassification uses: a mutating
// tool_use against an existing file records its pre-image blob keyed by
// tool_use_id, resolved relative to the session cwd exactly like the file
// ledger.
func TestShadowCaptureRecordsPreImage(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	r := New(sess.ID, data, provider.NewRegistry())
	dir := shadowCwd(t)
	r.Cwd = dir
	r.ShadowRoot = t.TempDir()
	r.shadowStore = shadow.New(r.ShadowRoot, sess.ID)
	t.Cleanup(r.shadowStore.Wait)

	if err := os.WriteFile(filepath.Join(dir, "pre.txt"), []byte("old content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.shadowCapture(&provider.ToolUse{ID: "toolu_1", Path: "pre.txt", Mutates: true})
	r.shadowCapture(&provider.ToolUse{ID: "", Path: "pre.txt", Mutates: true}) // no wire ID: skipped, not fatal

	recs, err := shadow.ReadIndex(r.shadowStore.Dir())
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("index has %d records, want exactly 1 (ID-less capture skipped): %+v", len(recs), recs)
	}
	rec, _, ok := shadow.FindCapture(recs, "toolu_1")
	if !ok {
		t.Fatal("no capture for toolu_1")
	}
	if rec.Path != filepath.Join(dir, "pre.txt") {
		t.Errorf("capture path = %q, want cwd-resolved absolute path", rec.Path)
	}
	content, err := shadow.BlobContent(ctx, r.shadowStore.Dir(), rec.RootKey, rec.Blob)
	if err != nil || content != "old content\n" {
		t.Errorf("BlobContent = (%q, %v), want the pre-image", content, err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
