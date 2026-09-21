package acprpc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// eventCollector is a race-safe OnEvent sink (OnEvent fires from the
// OneShot goroutine while the test asserts afterwards).
type eventCollector struct {
	mu     sync.Mutex
	events []provider.Event
}

func (c *eventCollector) add(ev provider.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *eventCollector) all() []provider.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Event(nil), c.events...)
}

func TestOneShotStreamsRefTextAndUsage(t *testing.T) {
	dir := t.TempDir()
	e := testEngine(t, gooseConfig(), "goose", dir)
	var col eventCollector
	res, err := e.OneShot(context.Background(), provider.OneShotRequest{
		Prompt:  "echo one-shot",
		System:  "persona",
		OnEvent: col.add,
	})
	if err != nil {
		t.Fatalf("OneShot: %v", err)
	}
	if res.Text != "you said one-shot" {
		t.Fatalf("text = %q", res.Text)
	}
	if res.Ref == "" {
		t.Fatal("Ref empty — sessionId is known synchronously and must be returned")
	}
	if res.Usage.InputTokens != 20 || res.Usage.OutputTokens != 10 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	events := col.all()
	if len(events) == 0 || events[0].Kind != provider.EventSessionRef || events[0].Ref != res.Ref {
		t.Fatalf("first OnEvent must be EventSessionRef with the ref; got %+v", events[:min(len(events), 2)])
	}
	sawText := false
	for _, ev := range events {
		if ev.Kind == provider.EventText && strings.Contains(ev.Text, "you said one-shot") {
			sawText = true
		}
	}
	if !sawText {
		t.Fatalf("OnEvent never saw the streamed text: %+v", events)
	}
	// The emulation closes the session on the way out (goose advertises
	// sessionCapabilities.close).
	if _, err := os.Stat(filepath.Join(dir, "close-called")); err != nil {
		t.Fatalf("one-shot session never closed: %v", err)
	}
}

func TestOneShotNilOnEventIsSafe(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	res, err := e.OneShot(context.Background(), provider.OneShotRequest{Prompt: "echo quiet"})
	if err != nil {
		t.Fatalf("OneShot: %v", err)
	}
	if res.Text != "you said quiet" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestOneShotErroredTurnFails(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	_, err := e.OneShot(context.Background(), provider.OneShotRequest{Prompt: "auth"})
	if err == nil || !strings.Contains(err.Error(), "agent authentication required") {
		t.Fatalf("err = %v", err)
	}
}

func TestOneShotContextCancellation(t *testing.T) {
	e := testEngine(t, gooseConfig(), "goose", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := e.OneShot(ctx, provider.OneShotRequest{Prompt: "slow"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
