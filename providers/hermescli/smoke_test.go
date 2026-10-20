package hermescli

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/binlocate"
	"github.com/drewkidwell/mads/providers/provider"
)

// TestRealHermesSmoke drives the real hermes-acp (MADS_HERMES_SMOKE=1, plus
// MADS_HERMES_SMOKE_HOME — see below) on GitHub Copilot gpt-4.1 in a
// scratch HERMES_HOME — never ~/.hermes. The
// scratch config names only the provider and model; hermes resolves the
// Copilot credential itself from the gh CLI (`gh auth token`, recorded in
// the scratch auth.json as a "gh_cli" pool entry with a fingerprint, no
// secret copied). Turns are kept short: a one-liner, one `ls`, an
// interrupted count, a recall after resume, one MCP tool call.
func TestRealHermesSmoke(t *testing.T) {
	if os.Getenv("MADS_HERMES_SMOKE") != "1" {
		t.Skip("set MADS_HERMES_SMOKE=1 to run against the real hermes-acp (uses Copilot via gh)")
	}
	if _, err := binlocate.Find("hermes-acp"); err != nil {
		t.Fatalf("hermes-acp not found: %v", err)
	}
	home := t.TempDir()
	// A scratch HERMES_HOME has no hermes dependency install of its own,
	// and mads refuses to start hermes-acp there (pkg/hermesinstall: hermes
	// would sync ~600 MB into it). The smoke borrows the install of a hermes
	// home prepared beforehand, by hermes, outside mads —
	// MADS_HERMES_SMOKE_HOME — through a symlinked installs/ (hermes keys
	// dependency state by <root>/installs/<key>). Never ~/.hermes: a
	// borrowing launch rebuilds the owner's pm-runtime generation in place,
	// which must not happen under the user's running Hermes.
	donor := os.Getenv("MADS_HERMES_SMOKE_HOME")
	if donor == "" {
		t.Fatal("set MADS_HERMES_SMOKE_HOME to a hermes home with its own dependency install (not ~/.hermes); " +
			"make one once with hermes itself, e.g. HERMES_HOME=/tmp/hermes-smoke hermes-acp </dev/null")
	}
	if realHome, err := os.UserHomeDir(); err == nil {
		if a, b := resolvedPath(donor), resolvedPath(filepath.Join(realHome, ".hermes")); a == b {
			t.Fatal("MADS_HERMES_SMOKE_HOME is ~/.hermes: the smoke must not touch the user's own install")
		}
	}
	installs := filepath.Join(donor, "installs")
	if _, err := os.Stat(installs); err != nil {
		t.Fatalf("no hermes install to borrow at %s: %v", installs, err)
	}
	if err := os.Symlink(installs, filepath.Join(home, "installs")); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("model:\n  default: gpt-4.1\n  provider: copilot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgBefore := fileSum(t, cfgPath)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "alpha.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New(Config{Home: home})
	ctx := context.Background()
	const turnTimeout = 3 * time.Minute

	models, err := p.Models(ctx)
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if !hasModel(models, "copilot:gpt-4.1") {
		t.Fatalf("Models (%d) lack copilot:gpt-4.1", len(models))
	}
	t.Logf("models: %d, e.g. %+v", len(models), models[0])

	// The model rides session/set_model, which must not touch config.yaml.
	sess, err := p.Start(ctx, provider.StartOptions{Mode: provider.ModeACP, Cwd: cwd, Model: "copilot:gpt-4.1"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = sess.Close() }()
	ref := sess.Ref()
	t.Logf("ref %s", ref)

	send := func(prompt string) []provider.Event {
		t.Helper()
		if err := sess.Send(ctx, prompt); err != nil {
			t.Fatalf("Send: %v", err)
		}
		return drainTurnWithin(t, sess, turnTimeout)
	}

	t1 := send("Reply with exactly one line: smoke-ok-7341")
	if got := turnText(t1); !strings.Contains(got, "smoke-ok-7341") {
		t.Fatalf("turn 1 text %q", got)
	}
	u1 := turnEnd(t, t1).Usage

	t2 := send("Use your terminal tool to run: ls. Then reply with one short sentence naming the file you saw.")
	var sawUse, sawResult bool
	for _, ev := range t2 {
		sawUse = sawUse || ev.Kind == provider.EventToolUse
		sawResult = sawResult || ev.Kind == provider.EventToolResult
		if ev.Kind == provider.EventToolResult && ev.Tool != nil && strings.HasPrefix(ev.Tool.ID, "edit-approval") {
			t.Errorf("synthetic approval id leaked: %+v", ev)
		}
	}
	live2 := turnText(t2)
	if !sawUse || !sawResult || !strings.Contains(live2, "alpha") {
		t.Fatalf("turn 2: use %v result %v text %q", sawUse, sawResult, live2)
	}
	if strings.HasPrefix(live2, "\n") {
		t.Errorf("turn 2 text still starts with the injected break: %q", live2)
	}
	u2 := turnEnd(t, t2).Usage

	t3 := send("Reply with exactly: done")
	u3 := turnEnd(t, t3).Usage
	t.Logf("usage per turn: %+v / %+v / %+v", u1, u2, u3)
	if u1 == nil || u2 == nil || u3 == nil || u1.OutputTokens <= 0 || u3.InputTokens <= 0 {
		t.Fatalf("usage missing: %+v %+v %+v", u1, u2, u3)
	}
	// Per turn, not cumulative: a cumulative third figure would exceed the
	// first two turns' sum.
	if u3.InputTokens >= u1.InputTokens+u2.InputTokens {
		t.Errorf("turn 3 input %d >= turns 1+2 (%d): usage still cumulative", u3.InputTokens, u1.InputTokens+u2.InputTokens)
	}

	if err := sess.Send(ctx, "Write the numbers from 1 to 300, one per line, no other text."); err != nil {
		t.Fatal(err)
	}
	waitText(t, sess, turnTimeout)
	if err := sess.Interrupt(ctx); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if end := turnEnd(t, drainTurnWithin(t, sess, turnTimeout)); !end.Interrupted {
		t.Errorf("turn end after cancel not interrupted: %+v", end)
	}
	_ = sess.Close()

	if after := fileSum(t, cfgPath); after != cfgBefore {
		t.Errorf("config.yaml changed by set_model: %x -> %x", cfgBefore, after)
	}

	msgs, err := p.Transcript(ctx, ref)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	t.Logf("transcript:\n%s", render(msgs))
	if !containsMsg(msgs, "user", "Reply with exactly one line: smoke-ok-7341") || !containsMsg(msgs, "agent", "smoke-ok-7341") {
		t.Error("transcript lacks turn 1")
	}
	var tool, result bool
	for _, m := range msgs {
		tool = tool || (m.Role == "tool" && m.Tool != nil && m.Tool.Name == "terminal")
		result = result || m.Role == "tool_result"
	}
	if !tool || !result {
		t.Error("transcript lacks the ls call and its result")
	}
	// Live text after the tool and the stored row agree once the break is
	// stripped (rule (b)). The live turn may hold text from before the tool
	// too; the stored final row must be its tail.
	if !anyAgentSuffix(msgs, live2) {
		t.Errorf("no stored agent row is a suffix of the live post-tool text %q", live2)
	}

	resumed, err := p.Start(ctx, provider.StartOptions{Mode: provider.ModeACP, Cwd: cwd, Resume: ref})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer func() { _ = resumed.Close() }()
	if err := resumed.Send(ctx, "In one short sentence: what exact marker did I ask you to reply with in my first message?"); err != nil {
		t.Fatal(err)
	}
	if got := turnText(drainTurnWithin(t, resumed, turnTimeout)); !strings.Contains(got, "smoke-ok-7341") {
		t.Errorf("recall after resume = %q", got)
	}
	_ = resumed.Close()
	if _, err := p.Start(ctx, provider.StartOptions{Mode: provider.ModeACP, Cwd: cwd, Resume: "00000000-0000-4000-8000-000000000000"}); err == nil {
		t.Error("resume of an unknown ref succeeded")
	}

	// MCPServers: a stdio server handed over on session/new must be callable.
	// Its marker exists nowhere but in the tool's result.
	server := filepath.Join(t.TempDir(), "mcpprobe.py")
	if err := os.WriteFile(server, []byte(mcpProbeServer), 0o755); err != nil {
		t.Fatal(err)
	}
	withMCP, err := p.Start(ctx, provider.StartOptions{Mode: provider.ModeACP, Cwd: cwd,
		MCPServers: []provider.MCPServerRef{{Name: "madsprobe", Command: "python3 " + server}}})
	if err != nil {
		t.Fatalf("Start with MCPServers: %v", err)
	}
	defer func() { _ = withMCP.Close() }()
	if err := withMCP.Send(ctx, "Call the MCP tool mads_probe_ping (it may be listed with a server prefix) and reply with exactly the text it returned."); err != nil {
		t.Fatal(err)
	}
	if got := turnText(drainTurnWithin(t, withMCP, turnTimeout)); !strings.Contains(got, "marker-QX7731") {
		t.Errorf("MCP turn text = %q, want the tool's marker", got)
	}
}

// mcpProbeServer is a minimal stdio MCP server with one tool,
// mads_probe_ping, whose result is a marker the model can't guess.
const mcpProbeServer = `#!/usr/bin/env python3
import json, sys
def send(o):
    sys.stdout.write(json.dumps(o) + "\n"); sys.stdout.flush()
for line in sys.stdin:
    try:
        m = json.loads(line)
    except Exception:
        continue
    mid, meth = m.get("id"), m.get("method")
    if meth == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": m["params"].get("protocolVersion", "2024-11-05"),
              "capabilities": {"tools": {}}, "serverInfo": {"name": "madsprobe", "version": "0"}}})
    elif meth == "tools/list":
        send({"jsonrpc": "2.0", "id": mid, "result": {"tools": [{"name": "mads_probe_ping",
              "description": "Returns the probe marker.", "inputSchema": {"type": "object", "properties": {}}}]}})
    elif meth == "tools/call":
        send({"jsonrpc": "2.0", "id": mid, "result": {"content": [{"type": "text", "text": "marker-QX7731"}]}})
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
`

func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func fileSum(t *testing.T, path string) [32]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(raw)
}

func hasModel(models []provider.Model, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}

func containsMsg(msgs []provider.Message, role, text string) bool {
	for _, m := range msgs {
		if m.Role == role && strings.Contains(m.Text, text) {
			return true
		}
	}
	return false
}

func anyAgentSuffix(msgs []provider.Message, live string) bool {
	for _, m := range msgs {
		if m.Role == "agent" && m.Text != "" && strings.HasSuffix(live, m.Text) {
			return true
		}
	}
	return false
}

func waitText(t *testing.T, sess provider.Session, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("events closed before any text")
			}
			if ev.Kind == provider.EventText {
				return
			}
			if ev.Kind == provider.EventTurnEnd {
				t.Fatal("turn ended before it could be interrupted")
			}
		case <-deadline:
			t.Fatal("no text before the deadline")
		}
	}
}
