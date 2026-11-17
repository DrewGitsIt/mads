package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/providers/provider"
)

// TestNoProviderPutsATokenInArgv (docs/LOCAL-FRONT-DOOR-DESIGN.md H4,
// round 2): every harness that takes MCP servers — kiro has none — is
// started on each transport it offers, separately, with the orchestrate
// MCP server (a stdio server whose Env carries a session-scoped token),
// against a stand-in that answers JSON-RPC far enough for the provider to
// hand its MCP servers over. For every spawn: the MCP server really reached
// the harness (on stdin, in an argv-named file, or in a wrapper), and the
// token is in no argv — argv is readable by every OS user (ps) — and only in
// owner-only files.
func TestNoProviderPutsATokenInArgv(t *testing.T) {
	const scoped = "mads_s_ARGVPROBEARGVPROBEARGVPROBEARGVPROBEARGVPR"
	home := t.TempDir()
	dump := t.TempDir()
	bin := t.TempDir()
	probe := filepath.Join(t.TempDir(), "harnessprobe")
	if out, err := exec.Command("go", "build", "-o", probe, "./testdata/harnessprobe").CombinedOutput(); err != nil {
		t.Fatalf("build the probe: %v\n%s", err, out)
	}
	binOf := map[string]string{"claude-code": "claude", "goose": "goose", "hermes": "hermes-acp", "codex": "codex"}
	for _, n := range binOf {
		if err := os.Symlink(probe, filepath.Join(bin, n)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("MADS_HOME", home)
	t.Setenv("MADS_AGENT_BIN_DIR", bin)
	t.Setenv("PROBE_DIR", dump)

	reg, err := buildProviderRegistry(home, agentenv.ModeInherit, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.MCPServerRef{
		Name:    "mads-links",
		Type:    "stdio",
		Command: filepath.Join(bin, "mads") + " internal orchestrate-mcp",
		Env:     []string{"MADS_ROUTER_URL=http://127.0.0.1:1", "MADS_SESSION_ID=ses_x", localauth.EnvRouterToken + "=" + scoped},
	}
	logs := func(n string) map[string]bool {
		m, _ := filepath.Glob(filepath.Join(dump, n+"-*.log"))
		out := map[string]bool{}
		for _, f := range m {
			out[f] = true
		}
		return out
	}
	covered := 0
	for _, name := range []string{"claude-code", "goose", "hermes", "codex"} {
		p, err := reg.Get(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, mode := range []provider.Mode{provider.ModeACP, provider.ModePTY} {
			label := name + "/" + string(mode)
			before := logs(binOf[name])
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			s, err := p.Start(ctx, provider.StartOptions{Mode: mode, Cwd: home, MCPServers: []provider.MCPServerRef{ref}})
			if errors.Is(err, provider.ErrUnsupported) {
				cancel()
				t.Logf("%s: not a transport this harness offers", label)
				continue
			}
			if err != nil {
				cancel()
				t.Errorf("%s: Start: %v", label, err)
				continue
			}
			// Let the probe log what it was handed.
			var fresh string
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
				for f := range logs(binOf[name]) {
					if !before[f] {
						fresh = f
					}
				}
				if fresh != "" {
					if b, _ := os.ReadFile(fresh); strings.Contains(string(b), "mads-links") || strings.Contains(string(b), "orchestrate-mcp") { //nolint:gosec // test-written
						break
					}
				}
			}
			_ = s.Close()
			cancel()
			if fresh == "" {
				t.Errorf("%s: the harness was never spawned", label)
				continue
			}
			raw, _ := os.ReadFile(fresh) //nolint:gosec // test-written
			got := string(raw)
			if !strings.Contains(got, "mads-links") && !strings.Contains(got, "orchestrate-mcp") {
				t.Errorf("%s: the orchestrate MCP server never reached the harness:\n%s", label, got)
			}
			for _, line := range strings.Split(got, "\n") {
				switch {
				case strings.HasPrefix(line, "ARGV "):
					if strings.Contains(line, scoped) || strings.Contains(line, localauth.EnvRouterToken+"=") {
						t.Errorf("%s: argv holds the scoped token (readable by every OS user via ps): %s", label, line)
					}
				case strings.HasPrefix(line, "FILE "):
					f := strings.SplitN(line, " ", 4)
					if len(f) == 4 && strings.Contains(f[3], scoped) && f[2] != "600" && f[2] != "700" {
						t.Errorf("%s: %s holds the scoped token with mode %s, want owner-only", label, f[1], f[2])
					}
				}
			}
			covered++
		}
	}
	if covered < 5 {
		t.Fatalf("only %d harness/transport combinations reached their MCP handover", covered)
	}
}
