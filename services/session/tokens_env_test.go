package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// The stand-in `claude` the session child launches as its agent: it dumps
// its own environment (which is what every tool the agent runs inherits)
// and copies the MCP config it was handed, then exits.
const standInClaude = `#!/bin/sh
env > "$MADS_TEST_DUMP/agent.env"
prev=""
for a in "$@"; do
  if [ "$prev" = "--mcp-config" ]; then cp "$a" "$MADS_TEST_DUMP/mcp.json"; fi
  prev="$a"
done
exit 0
`

// The stand-in for the orchestrate MCP server's local helper (-mads-cmd):
// dumps its environment and exits.
const standInMads = `#!/bin/sh
env > "$MADS_TEST_DUMP/orchestrate-mcp.env"
exit 0
`

func readEnvFile(t *testing.T, path string) map[string][]string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test-written file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	out := map[string][]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			out[k] = append(out[k], v)
		}
	}
	return out
}

func waitFile(t *testing.T, path string, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			time.Sleep(50 * time.Millisecond) // let the writer finish
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

// TestAgentEnvHoldsNeitherToken runs the real session child the way the
// Router spawns it — MADS_LOCAL_TOKEN and MADS_ROUTER_TOKEN in its env — with
// a leader link so the orchestrate MCP server is wired, and proves, for both
// agentenv modes (isolated even with both names on env_passthrough):
//   - the agent's own environment holds neither token;
//   - the orchestrate MCP server gets the scoped token, in the MCP config
//     the provider launches it from and in the local helper's env, and never
//     the install token (so the child really did scrub its own env).
func TestAgentEnvHoldsNeitherToken(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "session")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build session: %v\n%s", err, out)
	}
	for _, mode := range []contracts.EnvMode{contracts.EnvModeInherit, contracts.EnvModeIsolated} {
		t.Run(string(mode), func(t *testing.T) {
			const installTok = "install-token-install-token-install-token-x"
			const scopedTok = "mads_s_scoped-token-scoped-token-scoped-token"
			ctx := context.Background()
			base := datatest.Start(t)
			data := dataclient.New(base, "user")
			cfg, err := data.Config(ctx)
			if err != nil {
				t.Fatal(err)
			}
			cfg.EnvMode = mode
			if mode == contracts.EnvModeIsolated {
				// Adversarial: even named for passthrough, neither gets through.
				cfg.EnvPassthrough = []string{"MADS_TEST_DUMP", localauth.EnvLocalToken, localauth.EnvRouterToken}
			}
			cfg.FeatureFlags.LinkedSessions = true
			if err := data.UpdateConfig(ctx, &cfg); err != nil {
				t.Fatal(err)
			}
			leader := contracts.Session{Name: "leader", Provider: "claude-code", Isolation: contracts.IsolationCurrentDir}
			subject := contracts.Session{Name: "subject", Provider: "fake"}
			for _, s := range []*contracts.Session{&leader, &subject} {
				if err := data.CreateSession(ctx, s); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := data.CreateLink(ctx, contracts.LinkRequest{GrantingSessionID: leader.ID, SubjectSessionID: subject.ID, Grants: []contracts.Grant{contracts.GrantOrchestrate}}); err != nil {
				t.Fatal(err)
			}

			home := t.TempDir()
			dump := t.TempDir()
			agentBin := t.TempDir()
			cwd := t.TempDir()
			if err := os.WriteFile(filepath.Join(agentBin, "claude"), []byte(standInClaude), 0o755); err != nil { //nolint:gosec // executable stand-in
				t.Fatal(err)
			}
			fakeMads := filepath.Join(t.TempDir(), "mads")
			if err := os.WriteFile(fakeMads, []byte(standInMads), 0o755); err != nil { //nolint:gosec // executable stand-in
				t.Fatal(err)
			}
			cmd := exec.Command(bin, //nolint:gosec // test-built binary
				"-addr", "127.0.0.1:0", "-session-id", leader.ID, "-data-url", base,
				"-router-url", "http://127.0.0.1:1", "-cwd", cwd, "-worktree-root", "off",
				"-shadow-root", "", "-mads-home", home, "-mads-cmd", fakeMads)
			cmd.Env = append(os.Environ(),
				"HOME="+home, "MADS_HOME="+home, "MADS_AGENT_BIN_DIR="+agentBin, "MADS_TEST_DUMP="+dump,
				localauth.EnvLocalToken+"="+installTok, localauth.EnvRouterToken+"="+scopedTok)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})

			waitFile(t, filepath.Join(dump, "agent.env"), 20*time.Second)
			agent := readEnvFile(t, filepath.Join(dump, "agent.env"))
			if len(agent["PATH"]) == 0 {
				t.Fatalf("agent env dump looks empty: %v", agent)
			}
			for _, name := range []string{localauth.EnvLocalToken, localauth.EnvRouterToken} {
				if v, ok := agent[name]; ok {
					t.Errorf("the agent's env holds %s=%v", name, v)
				}
			}
			raw, _ := os.ReadFile(filepath.Join(dump, "agent.env")) //nolint:gosec // test-written file
			if strings.Contains(string(raw), installTok) || strings.Contains(string(raw), scopedTok) {
				t.Error("a token value appears somewhere in the agent's env")
			}

			waitFile(t, filepath.Join(dump, "mcp.json"), 5*time.Second)
			mcp, err := os.ReadFile(filepath.Join(dump, "mcp.json")) //nolint:gosec // test-written file
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(mcp), `"`+localauth.EnvRouterToken+`":"`+scopedTok+`"`) {
				t.Errorf("the orchestrate MCP server's config lacks the scoped token: %s", mcp)
			}
			if strings.Contains(string(mcp), installTok) {
				t.Errorf("the MCP config holds the install token: %s", mcp)
			}

			waitFile(t, filepath.Join(dump, "orchestrate-mcp.env"), 5*time.Second)
			helper := readEnvFile(t, filepath.Join(dump, "orchestrate-mcp.env"))
			if got := helper[localauth.EnvRouterToken]; len(got) != 1 || got[0] != scopedTok {
				t.Errorf("orchestrate MCP helper's %s = %v, want exactly the scoped token", localauth.EnvRouterToken, got)
			}
			if v, ok := helper[localauth.EnvLocalToken]; ok {
				t.Errorf("the session child didn't scrub its own env: its MCP helper inherited %s=%v", localauth.EnvLocalToken, v)
			}
		})
	}
}
