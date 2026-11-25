//go:build !perf

package fake_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// TestNoPerfDialectsUntagged: a build without -tags perf (every release)
// has no "permission" or "perf-stream" dialect and ignores
// MADS_FAKE_START_DELAY_MS and MADS_FAKE_PERF_HISTORY_DIR; all live in
// perf.go.
func TestNoPerfDialectsUntagged(t *testing.T) {
	t.Setenv("MADS_FAKE_START_DELAY_MS", "5000")
	asked := make(chan struct{}, 1)
	f := fake.New()
	t0 := time.Now()
	s, err := f.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP,
		Permissions: func(context.Context, provider.PermissionAsk) provider.PermissionDecision {
			asked <- struct{}{}
			return provider.PermissionDecision{Allow: true}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("Start took %s: MADS_FAKE_START_DELAY_MS honoured in an untagged build", d)
	}
	if err := s.Send(context.Background(), "permission Write"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-asked:
			t.Fatal(`the "permission" dialect asked the hook in an untagged build`)
		case ev := <-s.Events():
			if ev.Kind == provider.EventToolUse {
				t.Fatalf("tool call %+v in an untagged build", ev.Tool)
			}
			if ev.Kind == provider.EventTurnEnd {
				return
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}
}

func TestNoPerfStreamOrHistoryUntagged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "perf-chat-1.json"), []byte(`[{"role":"user","text":"hi"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MADS_FAKE_PERF_HISTORY_DIR", dir)
	f := fake.New()
	if _, err := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Resume: "perf-chat-1"}); err == nil {
		t.Fatal("resumed from MADS_FAKE_PERF_HISTORY_DIR in an untagged build")
	}
	s, err := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), "perf-stream 3000 100 30"); err != nil {
		t.Fatal(err)
	}
	texts := 0
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == provider.EventText {
				texts++
			}
			if ev.Kind == provider.EventTurnEnd {
				if texts > 1 {
					t.Fatalf("%d text deltas: the perf-stream dialect ran in an untagged build", texts)
				}
				return
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}
}
