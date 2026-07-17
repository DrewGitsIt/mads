package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

type mcpServerJSON struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type mcpConfigFile struct {
	MCPServers map[string]mcpServerJSON `json:"mcpServers"`
}

// writeMCPConfig materializes servers as a real claude --mcp-config file and
// returns its path. Each command is resolved to an absolute path via
// exec.LookPath before being written: claude spawns the MCP server
// subprocess itself, from the session's own cwd (a worktree, not this
// process's), so a bare command name resolved only against this process's
// PATH would not reliably re-resolve there. A LookPath failure falls back to
// the raw token rather than erroring — claude's own MCP server startup
// surfaces the failure, and one capability's misconfiguration must never
// block the whole session launch.
func writeMCPConfig(servers []provider.MCPServerRef) (path string, err error) {
	cfg := mcpConfigFile{MCPServers: map[string]mcpServerJSON{}}
	for _, s := range servers {
		fields := strings.Fields(s.Command)
		if len(fields) == 0 {
			return "", fmt.Errorf("mcp server %q: empty command", s.Name)
		}
		bin := fields[0]
		if resolved, lookErr := exec.LookPath(bin); lookErr == nil {
			bin = resolved
		}
		entry := mcpServerJSON{Command: bin, Args: fields[1:]}
		for _, kv := range s.Env {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			if entry.Env == nil {
				entry.Env = map[string]string{}
			}
			entry.Env[k] = v
		}
		cfg.MCPServers[s.Name] = entry
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "mads-mcp-*.json")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(raw); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// mcpArgs returns the CLI args wiring servers into claude's real launch, and
// the config file path (for the caller to remove on session Close) — both
// zero/nil when there are none, so sessions without a declared MCP server
// see zero behavior change. --strict-mcp-config keeps an ambient project
// .mcp.json (whose servers sit "pending approval" until a human confirms
// them interactively) from silently attaching and hanging a headless
// session.
func mcpArgs(servers []provider.MCPServerRef) (args []string, configPath string, err error) {
	if len(servers) == 0 {
		return nil, "", nil
	}
	configPath, err = writeMCPConfig(servers)
	if err != nil {
		return nil, "", err
	}
	return []string{"--mcp-config", configPath, "--strict-mcp-config"}, configPath, nil
}
