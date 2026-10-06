package goosecli

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// TestRealGoosePermissionAsks drives a real `goose acp` at trust limited
// with a recording asker, proving the ACP permission path end to end:
// goose's own request_permission for a shell call reaches the asker with
// the call's id, a deny keeps the command from running, an allow lets it
// run, and a plain file read never asks (the engine allows the read class
// itself, since goose's approve mode would otherwise ask for everything).
//
// Needs a goose model that actually calls tools. goose reads its provider
// and model from the user's config.yaml; MADS_GOOSE_SMOKE_PROVIDER and
// MADS_GOOSE_SMOKE_MODEL override them (GOOSE_PROVIDER/GOOSE_MODEL) for
// this run only. Turns that call no tool fail with the model's reply in
// the log, since that's a model problem, not a permission one.
//
//	MADS_GOOSE_SMOKE=1 MADS_GOOSE_SMOKE_PROVIDER=... MADS_GOOSE_SMOKE_MODEL=... \
//	  go test ./providers/goosecli -run TestRealGoosePermissionAsks -v
func TestRealGoosePermissionAsks(t *testing.T) {
	skipUnlessRealGooseSmoke(t)

	var env []string
	if v := os.Getenv("MADS_GOOSE_SMOKE_PROVIDER"); v != "" {
		env = append(env, "GOOSE_PROVIDER="+v)
	}
	if v := os.Getenv("MADS_GOOSE_SMOKE_MODEL"); v != "" {
		env = append(env, "GOOSE_MODEL="+v)
	}

	dir := t.TempDir()
	var mu sync.Mutex
	var asks []provider.PermissionAsk
	allow := false
	asker := func(_ context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
		mu.Lock()
		defer mu.Unlock()
		asks = append(asks, ask)
		t.Logf("ask: tool=%q tool_use_id=%q allow=%v", ask.Tool, ask.ToolUseID, allow)
		return provider.PermissionDecision{Allow: allow}
	}
	asksSoFar := func() []provider.PermissionAsk {
		mu.Lock()
		defer mu.Unlock()
		return append([]provider.PermissionAsk(nil), asks...)
	}
	sess := startSession(t, New(Config{Env: env}), provider.StartOptions{Trust: provider.TrustLimited, Permissions: asker, Cwd: dir})

	// turn runs one prompt and reports whether the model called any tool.
	turn := func(prompt string) bool {
		t.Helper()
		if err := sess.Send(context.Background(), prompt); err != nil {
			t.Fatalf("Send: %v", err)
		}
		called := false
		var reply string
		for _, ev := range drainTurnWithin(t, sess, 300*time.Second) {
			switch ev.Kind {
			case provider.EventToolUse:
				called = true
			case provider.EventText:
				reply += ev.Text
			}
		}
		if !called {
			t.Logf("model called no tool; it replied: %.300s", reply)
		}
		return called
	}

	denied := filepath.Join(dir, "denied.txt")
	if !turn("Use your shell tool to run exactly this command and nothing else: touch " + denied) {
		t.Fatal("no tool call on the deny turn — use a goose model that calls tools (MADS_GOOSE_SMOKE_MODEL)")
	}
	got := asksSoFar()
	if len(got) == 0 {
		t.Fatal("goose ran a shell call at trust limited without asking")
	}
	if got[0].ToolUseID == "" {
		t.Errorf("ask carries no tool_use_id: %+v", got[0])
	}
	if _, err := os.Stat(denied); err == nil {
		t.Errorf("%s exists: the denied command ran anyway", denied)
	}

	mu.Lock()
	allow = true
	mu.Unlock()
	allowed := filepath.Join(dir, "allowed.txt")
	if !turn("Use your shell tool to run exactly this command and nothing else: touch " + allowed) {
		t.Fatal("no tool call on the allow turn")
	}
	if _, err := os.Stat(allowed); err != nil {
		t.Errorf("%s missing after the user allowed it: %v", allowed, err)
	}

	note := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(note, []byte("the secret word is pelican\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := len(asksSoFar())
	if !turn("Read the file " + note + " with your file-reading tool and tell me the secret word.") {
		t.Fatal("no tool call on the read turn")
	}
	if extra := asksSoFar()[before:]; len(extra) > 0 {
		t.Errorf("a plain read asked the user: %+v", extra)
	}
}
