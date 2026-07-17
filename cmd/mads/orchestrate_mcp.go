package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/drewkidwell/mads/pkg/orchestratemcp"
)

// cmdInternal dispatches "mads internal <subcommand>" — hidden, undocumented
// entry points meant only for the platform to spawn as a subprocess
// (Config.Command values), never typed by a user.
func cmdInternal(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("internal: subcommand required")
	}
	switch args[0] {
	case "orchestrate-mcp":
		return cmdOrchestrateMCP(os.Stdin, os.Stdout)
	default:
		return fmt.Errorf("internal: unknown subcommand %q", args[0])
	}
}

// cmdOrchestrateMCP runs the leader capability's MCP-over-stdio server
// (docs/LINKED-SESSIONS-DESIGN.md decisions 1-3). Hidden subcommand: never
// invoked by a user directly, only as the Config.Command a session's own
// synthetic orchestrate capability spawns (services/session/internal/runner/capability.go
// setupOrchestrate), which sets MADS_ROUTER_URL and MADS_SESSION_ID in its
// environment.
func cmdOrchestrateMCP(stdin io.Reader, stdout io.Writer) error {
	routerURL := os.Getenv("MADS_ROUTER_URL")
	sessionID := os.Getenv("MADS_SESSION_ID")
	if routerURL == "" || sessionID == "" {
		return fmt.Errorf("internal orchestrate-mcp: MADS_ROUTER_URL and MADS_SESSION_ID are required")
	}
	srv := orchestratemcp.New(routerURL, sessionID, stdin, stdout)
	return srv.Run(context.Background())
}
