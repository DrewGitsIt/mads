package claudecode

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// echoTurn sends "mcp-echo" and returns the turn's text.
func echoTurn(t *testing.T, sess provider.Session) string {
	t.Helper()
	if err := sess.Send(context.Background(), "mcp-echo"); err != nil {
		t.Fatal(err)
	}
	var text string
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			if ev.Kind == provider.EventText {
				text += ev.Text
			}
			if ev.Kind == provider.EventTurnEnd {
				return text
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}
}

// The structured transport mounts an MCP server on the live process with
// mcp_set_servers: no restart, same session, and the stand-in reports the
// server in its dynamic set with the command resolved like --mcp-config's.
func TestStreamSessionHotSetsMCPServers(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	hs, ok := sess.(provider.MCPHotSetter)
	if !ok {
		t.Fatal("stream session does not implement provider.MCPHotSetter")
	}
	before := echoTurn(t, sess)
	if strings.Contains(before, "dynamic=") {
		t.Fatalf("dynamic servers before the set: %s", before)
	}
	ref := sess.Ref()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hs.SetMCPServers(ctx, []provider.MCPServerRef{orchestrateMCPRef()}); err != nil {
		t.Fatalf("SetMCPServers: %v", err)
	}

	after := echoTurn(t, sess)
	name := orchestrateMCPRef().Name
	if !strings.Contains(after, `"`+name+`"`) || !strings.Contains(after, `"type":"stdio"`) {
		t.Fatalf("dynamic set after mcp_set_servers missing %s: %s", name, after)
	}
	if !strings.Contains(after, `"MADS_SESSION_ID":"sess-123"`) {
		t.Errorf("env didn't reach the dynamic server config: %s", after)
	}
	cmd := after[strings.Index(after, `"command":"`)+len(`"command":"`):]
	cmd = cmd[:strings.Index(cmd, `"`)]
	if !filepath.IsAbs(cmd) && cmd != "mads" {
		t.Errorf("command = %q, want resolved like the --mcp-config path", cmd)
	}
	if sess.Ref() != ref {
		t.Errorf("session ref changed %q → %q; a hot set must not restart", ref, sess.Ref())
	}
}

// A server the process was launched with (--mcp-config) isn't in the
// dynamic set, so a change that drops it can't be applied live.
func TestStreamSessionHotSetRefusesDroppingLaunchServer(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, MCPServers: []provider.MCPServerRef{orchestrateMCPRef()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	hs := sess.(provider.MCPHotSetter)
	err = hs.SetMCPServers(context.Background(), nil)
	if !errors.Is(err, provider.ErrMCPHotSetUnsupported) {
		t.Fatalf("dropping a launch server: err = %v, want ErrMCPHotSetUnsupported", err)
	}
	// Keeping it (and adding nothing) is fine: an empty dynamic set.
	if err := hs.SetMCPServers(context.Background(), []provider.MCPServerRef{orchestrateMCPRef()}); err != nil {
		t.Fatalf("no-op set: %v", err)
	}
}

// A launch-time server can't be reconfigured live either. A leader
// launched with only read links gets the read-only orchestrate server; when
// a link gains orchestrate, the same-named server differs only in env (no
// MADS_ORCHESTRATE_READONLY), and skipping it as "already mounted" left
// send_message missing while the link read active (2026-10-19).
func TestStreamSessionHotSetRefusesChangingLaunchServer(t *testing.T) {
	p := testProvider(t)
	readOnly := orchestrateMCPRef()
	readOnly.Env = append(readOnly.Env, "MADS_ORCHESTRATE_READONLY=1")
	sess, err := p.Start(context.Background(), provider.StartOptions{
		Mode: provider.ModeACP, MCPServers: []provider.MCPServerRef{readOnly},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	hs := sess.(provider.MCPHotSetter)
	err = hs.SetMCPServers(context.Background(), []provider.MCPServerRef{orchestrateMCPRef()})
	if !errors.Is(err, provider.ErrMCPHotSetUnsupported) {
		t.Fatalf("changing a launch server's env: err = %v, want ErrMCPHotSetUnsupported", err)
	}
	if err := hs.SetMCPServers(context.Background(), []provider.MCPServerRef{readOnly}); err != nil {
		t.Fatalf("unchanged launch server: %v", err)
	}
}

// The terminal transport has no control channel.
func TestPTYSessionIsNotMCPHotSetter(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModePTY})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if _, ok := sess.(provider.MCPHotSetter); ok {
		t.Fatal("pty session claims MCPHotSetter; it can't send control requests")
	}
}
