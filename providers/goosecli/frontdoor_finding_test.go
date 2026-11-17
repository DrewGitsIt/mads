package goosecli

import (
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/providers/provider"
)

// TestPTYArgvCarriesNoToken (adversarial, front door D4; fixed by H4): the orchestrate MCP ref the session Runner builds
// (services/session/internal/runner/capability.go setupOrchestrate) carries
// MADS_ROUTER_TOKEN in its Env, "to this server's own env only". On goose's
// terminal transport, ptyArgs joins that Env into the --with-extension argv
// spec, so the session-scoped token sits in `goose session`'s argv for the
// life of the process — readable by every OS user with `ps -axww` (argv is
// not protected the way /proc/<pid>/environ or KERN_PROCARGS2 env is).
// A scoped token reaches POST /links/{id}/orchestrate/message, i.e. it lets
// another OS user message the subject sessions this session orchestrates —
// exactly the cross-user access the front door exists to close. Now an
// extension with Env is launched through an owner-only wrapper script.
func TestPTYArgvCarriesNoToken(t *testing.T) {
	const scoped = "mads_s_SCOPEDTOKENSCOPEDTOKENSCOPEDTOKENSCOPEDTOK"
	ref := provider.MCPServerRef{
		Name:    "mads-links",
		Command: "/usr/local/bin/mads internal orchestrate-mcp",
		Env: []string{
			"MADS_ROUTER_URL=http://localhost:17373",
			"MADS_SESSION_ID=ses_a",
			localauth.EnvRouterToken + "=" + scoped,
		},
		AlwaysLoad: true,
	}
	args := New(Config{}).ptyArgs(provider.StartOptions{MCPServers: []provider.MCPServerRef{ref}})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, scoped) || strings.Contains(joined, localauth.EnvRouterToken+"=") {
		t.Errorf("goose PTY argv holds the session-scoped Router token (visible to other OS users via ps): %q", args)
	}
}
