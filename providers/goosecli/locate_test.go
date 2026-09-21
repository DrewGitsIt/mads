package goosecli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// listRow mirrors the `session list --format json` row shape the fixtures
// serve (testdata/captures/oneshot-name-list.json).
type listRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	WorkingDir string `json:"working_dir"`
	CreatedAt  string `json:"created_at"`
}

// locateFixtures wires a provider whose fake goose serves the given session
// list rows and per-id export documents.
func locateFixtures(t *testing.T, rows []listRow, exports map[string]string) *Provider {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	listPath := filepath.Join(dir, "list.json")
	if err := os.WriteFile(listPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	exportDir := filepath.Join(dir, "exports")
	if err := os.Mkdir(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, doc := range exports {
		if err := os.WriteFile(filepath.Join(exportDir, id+".json"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return testProvider(t, "MADS_FAKE_SESSION_LIST="+listPath, "MADS_FAKE_EXPORT_DIR="+exportDir)
}

// exportFixture builds an export document whose FIRST conversation entry is
// an injected userVisible:false <turn-context> — the verification must skip
// it and compare the first VISIBLE user message (capture shape,
// oneshot-export.json).
func exportFixture(id, cwd, firstMessage string) string {
	return fmt.Sprintf(`{"id":%q,"working_dir":%q,"conversation":[
	 {"role":"user","created":1788032382,"content":[{"type":"text","text":"<turn-context>injected</turn-context>"}],"metadata":{"userVisible":false,"agentVisible":true,"turnContext":true}},
	 {"role":"user","created":1788032382,"content":[{"type":"text","text":%q}],"metadata":{"userVisible":true,"agentVisible":true}}
	]}`, id, cwd, firstMessage)
}

// declineCtx bounds a LocateSession call that must decline: the locator
// retries until ctx is done (never-guess means it keeps looking, not that
// it errors), so a decline test is a timeout test by construction.
func declineCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func rfc3339(ts time.Time) string { return ts.UTC().Format(time.RFC3339) }

func TestLocateSessionExactMatch(t *testing.T) {
	now := time.Now()
	spawned := now.Add(-time.Minute)
	p := locateFixtures(t, []listRow{
		// Decoys: right cwd but created before spawnedAfter (minus skew);
		// right window but wrong cwd.
		{ID: "20260829_10", WorkingDir: "/work/session", CreatedAt: rfc3339(now.Add(-time.Hour))},
		{ID: "20260829_11", WorkingDir: "/other/place", CreatedAt: rfc3339(now)},
		{ID: "20260829_12", WorkingDir: "/work/session", CreatedAt: rfc3339(now)},
	}, map[string]string{
		"20260829_12": exportFixture("20260829_12", "/work/session", "hello goose"),
	})

	ref, ok := p.LocateSession(context.Background(), "/work/session", spawned, "hello goose")
	if !ok || ref != "20260829_12" {
		t.Fatalf("LocateSession = (%q, %v), want (%q, true)", ref, ok, "20260829_12")
	}
}

func TestLocateSessionAmbiguousDeclines(t *testing.T) {
	now := time.Now()
	p := locateFixtures(t, []listRow{
		{ID: "20260829_12", WorkingDir: "/work/session", CreatedAt: rfc3339(now)},
		{ID: "20260829_13", WorkingDir: "/work/session", CreatedAt: rfc3339(now)},
	}, map[string]string{
		// Even a verifiable first message must not arbitrate: two
		// cwd/time survivors decline before any export runs.
		"20260829_12": exportFixture("20260829_12", "/work/session", "hello goose"),
		"20260829_13": exportFixture("20260829_13", "/work/session", "something else"),
	})

	if ref, ok := p.LocateSession(declineCtx(t), "/work/session", now.Add(-time.Minute), "hello goose"); ok {
		t.Fatalf("LocateSession = (%q, true), want decline on two candidates", ref)
	}
}

func TestLocateSessionWrongFirstMessageDeclines(t *testing.T) {
	now := time.Now()
	p := locateFixtures(t, []listRow{
		{ID: "20260829_12", WorkingDir: "/work/session", CreatedAt: rfc3339(now)},
	}, map[string]string{
		"20260829_12": exportFixture("20260829_12", "/work/session", "someone else's conversation"),
	})

	if ref, ok := p.LocateSession(declineCtx(t), "/work/session", now.Add(-time.Minute), "hello goose"); ok {
		t.Fatalf("LocateSession = (%q, true), want decline when the first visible user message differs", ref)
	}
}

// A ctx that is already done must decline promptly — LocateSession's
// contract on cancellation, and the Runner's shutdown path.
func TestLocateSessionCancelledCtxDeclines(t *testing.T) {
	now := time.Now()
	p := locateFixtures(t, []listRow{
		{ID: "20260829_12", WorkingDir: "/work/session", CreatedAt: rfc3339(now)},
	}, map[string]string{
		"20260829_12": exportFixture("20260829_12", "/work/session", "hello goose"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	var ref string
	var ok bool
	go func() {
		ref, ok = p.LocateSession(ctx, "/work/session", now.Add(-time.Minute), "hello goose")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("LocateSession did not return promptly on a cancelled ctx")
	}
	if ok {
		t.Fatalf("LocateSession = (%q, true), want decline on cancelled ctx", ref)
	}
}

func TestLocateSessionNoCandidateDeclines(t *testing.T) {
	now := time.Now()
	p := locateFixtures(t, []listRow{
		{ID: "20260829_10", WorkingDir: "/work/session", CreatedAt: rfc3339(now.Add(-time.Hour))},
	}, nil)

	if ref, ok := p.LocateSession(declineCtx(t), "/work/session", now.Add(-time.Minute), "hello goose"); ok {
		t.Fatalf("LocateSession = (%q, true), want decline when nothing survives the cwd/time filter", ref)
	}
}
