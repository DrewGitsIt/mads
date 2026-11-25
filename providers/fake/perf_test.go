//go:build perf

package fake_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

func TestPermissionDialectParksOnTheHook(t *testing.T) {
	asked := make(chan provider.PermissionAsk, 1)
	release := make(chan bool)
	f := fake.New()
	s, err := f.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP,
		Permissions: func(ctx context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
			asked <- ask
			return provider.PermissionDecision{Allow: <-release}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), "permission Write"); err != nil {
		t.Fatal(err)
	}
	var ask provider.PermissionAsk
	select {
	case ask = <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the permission hook was never asked")
	}
	if ask.Tool != "Write" || ask.ToolUseID == "" {
		t.Fatalf("ask = %+v", ask)
	}
	// Parked: no turn end until the decision.
	select {
	case ev := <-s.Events():
		if ev.Kind != provider.EventToolUse {
			t.Fatalf("first event %s, want tool_use", ev.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no tool_use")
	}
	select {
	case ev := <-s.Events():
		t.Fatalf("event %s before the decision", ev.Kind)
	case <-time.After(200 * time.Millisecond):
	}
	release <- true
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == provider.EventTurnEnd {
				return
			}
		case <-deadline:
			t.Fatal("no turn end after the decision")
		}
	}
}

func TestStreamDialectPacesDeltas(t *testing.T) {
	f := fake.New()
	s, err := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	const chars, rate, delta = 1200, 100, 30
	t0 := time.Now()
	if err := s.Send(context.Background(), "perf-stream 1200 100 30"); err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	n := 0
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			switch ev.Kind {
			case provider.EventText:
				n++
				got.WriteString(ev.Text)
			case provider.EventTurnEnd:
				if want := fake.StreamText(chars); got.String() != want {
					t.Fatalf("streamed %d chars, want the %d of StreamText", got.Len(), len(want))
				}
				if n != chars/delta {
					t.Fatalf("%d deltas, want %d", n, chars/delta)
				}
				// 40 deltas at 100/s: about 400 ms, never all at once.
				if d := time.Since(t0); d < 300*time.Millisecond || d > 3*time.Second {
					t.Fatalf("took %s, want about 400 ms", d)
				}
				return
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}
}

func TestStreamTextIsStableMarkdown(t *testing.T) {
	a, b := fake.StreamText(15000), fake.StreamText(15000)
	if a != b || len(a) != 15000 {
		t.Fatalf("StreamText not deterministic or wrong length (%d)", len(a))
	}
	for _, want := range []string{"## ", "```", "- ", "**", "`"} {
		if !strings.Contains(a, want) {
			t.Errorf("StreamText has no %q", want)
		}
	}
	if fake.StreamText(100) != a[:100] {
		t.Error("a shorter StreamText is not a prefix of a longer one")
	}
}

func TestResumeFromPerfHistoryDir(t *testing.T) {
	dir := t.TempDir()
	hist := []provider.Message{{Role: "user", Text: "hi", Key: "user:u1"}, {Role: "agent", Text: "hello", Key: "a1"}}
	raw, _ := json.Marshal(hist)
	if err := os.WriteFile(filepath.Join(dir, "perf-chat-1.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	f := fake.New()
	if _, err := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Resume: "perf-chat-1"}); err == nil {
		t.Fatal("resumed an unknown ref with no MADS_FAKE_PERF_HISTORY_DIR")
	}
	t.Setenv("MADS_FAKE_PERF_HISTORY_DIR", dir)
	s, err := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Resume: "perf-chat-1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.KeyedTranscript(context.Background(), s.Ref())
	if err != nil || len(got) != 2 || got[1].Text != "hello" {
		t.Fatalf("history after resume = %+v, %v", got, err)
	}
}
