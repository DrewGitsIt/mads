package codexcli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/binlocate"
	"github.com/drewkidwell/mads/providers/provider"
)

// smokeModel is the cheapest model the survey ran: every smoke turn is
// short, at low reasoning effort.
const smokeModel = "gpt-5.6-luna"

func skipUnlessRealCodexSmoke(t *testing.T) {
	t.Helper()
	if os.Getenv("MADS_CODEX_SMOKE") != "1" {
		t.Skip("set MADS_CODEX_SMOKE=1 to run against the real codex binary (spends a few small gpt-5.6-luna turns on the logged-in account)")
	}
	if _, err := binlocate.Find("codex"); err != nil {
		t.Skip("no codex binary on PATH or in a known install location")
	}
}

// TestRealCodexSmoke drives the real `codex app-server` end to end in a
// scratch git repo under /tmp at limited trust (workspace-write sandbox,
// untrusted approvals, every ask allowed): a marker turn, an approved file
// write, an interrupt, a mid-command Close that must reap the command,
// the transcript read back through a fresh app-server, a resume that
// recalls the marker, a fork, and a one-shot. It also proves the user's
// ~/.codex/config.toml is byte-for-byte unchanged afterwards (threadConfig).
//
// MADS_CODEX_SMOKE=1 go test -run Smoke ./providers/codexcli/ -v
func TestRealCodexSmoke(t *testing.T) {
	skipUnlessRealCodexSmoke(t)
	ctx := context.Background()

	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, ".codex", "config.toml")
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		configPath = filepath.Join(codexHome, "config.toml")
	}
	configBefore, configErr := os.ReadFile(configPath)
	t.Cleanup(func() {
		after, err := os.ReadFile(configPath)
		if (err == nil) != (configErr == nil) || !bytes.Equal(after, configBefore) {
			if configErr == nil {
				_ = os.WriteFile(configPath, configBefore, 0o600) // put the user's file back
			}
			t.Errorf("%s changed during the smoke (restored)", configPath)
		}
	})

	dir, err := os.MkdirTemp("/tmp", "mads-codex-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}

	p := New(Config{Effort: "low"})

	models, err := p.Models(ctx)
	if err != nil || len(models) == 0 {
		t.Fatalf("Models = %v, %v", models, err)
	}
	t.Logf("models: %d (first %s)", len(models), models[0].ID)

	var asksMu sync.Mutex
	var asks []provider.PermissionAsk
	allow := func(_ context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
		asksMu.Lock()
		asks = append(asks, ask)
		asksMu.Unlock()
		return provider.PermissionDecision{Allow: true}
	}
	sess, err := p.Start(ctx, provider.StartOptions{Mode: provider.ModeACP, Model: smokeModel, Cwd: dir, Trust: provider.TrustLimited, Permissions: allow})
	if err != nil {
		t.Fatal(err)
	}
	s := sess.(*session)
	ref := s.Ref()
	if ref == "" {
		t.Fatal("no ref at Start-return")
	}
	t.Logf("thread %s", ref)

	// Turn 1: a marker to recall after the resume.
	evs := smokeTurn(t, s, "Reply with exactly this and nothing else: mk-smoke-one")
	end := evs[len(evs)-1]
	if end.Usage == nil || end.Usage.OutputTokens <= 0 || end.Usage.ContextWindow <= 0 {
		t.Errorf("turn 1 usage = %+v", end.Usage)
	}
	t.Logf("turn 1: %q usage %+v", textOf(evs), end.Usage)

	// Turn 2: a write the limited policy asks about; allowed.
	evs = smokeTurn(t, s, "Create a file named smoke.txt containing the word hi, then reply done.")
	asksMu.Lock()
	nAsks := len(asks)
	t.Logf("turn 2: %q, %d asks %+v", textOf(evs), nAsks, asks)
	asksMu.Unlock()
	if nAsks == 0 {
		t.Error("limited trust never asked about the write")
	}
	if _, err := os.Stat(filepath.Join(dir, "smoke.txt")); err != nil {
		t.Logf("smoke.txt not written (model's choice, not a protocol claim): %v", err)
	}

	// Turn 3: interrupted mid-stream.
	if err := s.Send(ctx, "Write a 300-word story about a lighthouse keeper. No tools."); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, provider.EventText)
	if err := s.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	evs = drainTurn(t, s)
	if !evs[len(evs)-1].Interrupted {
		t.Errorf("interrupted turn end = %+v", evs[len(evs)-1])
	}
	t.Logf("turn 3 interrupted mid-stream: first text chunk seen before Interrupt, %d more chars after", len(textOf(evs)))

	// Turn 4: a long command, then Close while it runs — the command must
	// not outlive the session (codex runs it in a group of its own).
	if err := s.Send(ctx, "Run exactly this shell command and nothing else: sleep 41"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, provider.EventToolUse)
	time.Sleep(1500 * time.Millisecond)
	running := countProcs(t, "sleep 41")
	t.Logf("sleep 41 processes running at Close: %d", running)
	if running == 0 {
		t.Log("the model never ran it; reaping not exercised this run")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for countProcs(t, "sleep 41") > 0 {
		if time.Now().After(deadline) {
			t.Fatal("sleep 41 survived Close")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("after Close: %d sleep 41 processes", countProcs(t, "sleep 41"))

	// The transcript, read through a fresh app-server.
	msgs, err := p.Transcript(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	var sawMarker, sawAgent bool
	for _, m := range msgs {
		sawMarker = sawMarker || (m.Role == "user" && strings.Contains(m.Text, "mk-smoke-one"))
		sawAgent = sawAgent || m.Role == "agent"
		if m.Role == "user" && strings.Contains(m.Text, "<environment_context>") {
			t.Errorf("injected context in transcript: %q", m.Text)
		}
	}
	if !sawMarker || !sawAgent {
		t.Errorf("transcript (%d rows) lacks the exchange: %+v", len(msgs), msgs)
	}

	// Resume: same thread, history intact.
	resumed, err := p.Start(ctx, provider.StartOptions{Mode: provider.ModeACP, Model: smokeModel, Cwd: dir, Trust: provider.TrustLimited, Resume: ref})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Ref() != ref {
		t.Errorf("resumed ref %q, want %q", resumed.Ref(), ref)
	}
	evs = smokeTurn(t, resumed, "What exact marker did I ask you to reply with in my first message? Reply with just the marker.")
	if got := textOf(evs); !strings.Contains(got, "mk-smoke-one") {
		t.Errorf("resumed session lost the marker: %q", got)
	}
	t.Logf("resumed recall: %q; transcript rows before resume: %d", textOf(evs), len(msgs))
	_ = resumed.Close()

	// Fork: a new thread, no turn spent.
	fork, err := p.Start(ctx, provider.StartOptions{Mode: provider.ModeACP, Model: smokeModel, Cwd: dir, Trust: provider.TrustLimited, Resume: ref, Fork: true})
	if err != nil {
		t.Fatal(err)
	}
	if fork.Ref() == "" || fork.Ref() == ref {
		t.Errorf("fork ref %q", fork.Ref())
	}
	_ = fork.Close()

	res, err := p.OneShot(ctx, provider.OneShotRequest{Prompt: "Reply with exactly: mk-smoke-oneshot", Model: smokeModel, Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "mk-smoke-oneshot") || res.Ref == "" || res.Usage.OutputTokens <= 0 {
		t.Errorf("one-shot = %+v", res)
	}
	t.Logf("one-shot: %q ref %s", res.Text, res.Ref)
}

func smokeTurn(t *testing.T, s provider.Session, prompt string) []provider.Event {
	t.Helper()
	if err := s.Send(context.Background(), prompt); err != nil {
		t.Fatalf("Send: %v", err)
	}
	evs := drainTurnWithin(t, s, 3*time.Minute)
	for _, ev := range evs {
		if ev.Kind == provider.EventError {
			t.Fatalf("turn error: %s", ev.Text)
		}
	}
	return evs
}

func drainTurnWithin(t *testing.T, sess provider.Session, d time.Duration) []provider.Event {
	t.Helper()
	var evs []provider.Event
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("events closed before turn end; got %v", kinds(evs))
			}
			evs = append(evs, ev)
			if ev.Kind == provider.EventTurnEnd {
				return evs
			}
		case <-deadline:
			t.Fatalf("no turn end within %s; got %v", d, kinds(evs))
		}
	}
}

func waitFor(t *testing.T, s provider.Session, kind provider.EventKind) {
	t.Helper()
	deadline := time.After(2 * time.Minute)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatal("events closed")
			}
			if ev.Kind == kind {
				return
			}
			if ev.Kind == provider.EventTurnEnd {
				t.Fatalf("turn ended before a %s event", kind)
			}
		case <-deadline:
			t.Fatalf("no %s event", kind)
		}
	}
}

func countProcs(t *testing.T, command string) int {
	t.Helper()
	out, _ := exec.Command("ps", "-A", "-o", "command=").Output()
	n := 0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == command {
			n++
		}
	}
	return n
}
