package capability

import (
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Diagnose turns a health result into troubleshooting suggestions (spec
// §12.7). Pattern-matches on reason text produced by Health/EnsureStarted
// and by Runner.setupCapabilities' own "missing credential: X" string (all
// of which are stable, package-owned strings) — e.g. "missing credential: X"
// suggests adding X in credential settings, "directory not found" suggests
// checking the capability's configured path.
func Diagnose(cap contracts.Capability, healthy bool, reason string) []string {
	if healthy {
		return nil
	}
	switch {
	case strings.HasPrefix(reason, "missing credential:"):
		name := strings.TrimSpace(strings.TrimPrefix(reason, "missing credential:"))
		return []string{fmt.Sprintf("Add a value for %s in credential settings.", name)}
	case strings.Contains(reason, "directory not found"):
		return []string{fmt.Sprintf("Check that capability %q's configured path (%s) exists and is accessible.", cap.Name, cap.Config.Path)}
	case strings.Contains(reason, "server not running"):
		return []string{fmt.Sprintf("Try starting capability %q's MCP server, or check its command: %s", cap.Name, cap.Config.Command)}
	case strings.Contains(reason, "unreachable"):
		return []string{fmt.Sprintf("Check that %s is reachable from this machine.", cap.Config.URL)}
	case strings.Contains(reason, "empty command"):
		return []string{fmt.Sprintf("Capability %q has no command configured; set Config.Command.", cap.Name)}
	default:
		return []string{"No specific suggestion available; check the capability's configuration."}
	}
}
