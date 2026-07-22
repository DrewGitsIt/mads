package claudecode

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// DefaultCompleteModel is the haiku-class model Complete falls back to when
// req.Model is empty (Fast Model settings can override per call by setting
// req.Model).
const DefaultCompleteModel = "claude-haiku-4-5-20251001"

// emptyMCPConfig is the inline --mcp-config value asserting no MCP servers
// attach to a Complete call. Verified against `claude --help`: --mcp-config
// loads from JSON files *or strings*, so no temp file is needed for a value
// that's always this same empty object.
const emptyMCPConfig = `{"mcpServers":{}}`

// writeHermeticSettings materializes an empty --settings file for one
// Complete call, mirroring writeMCPConfig's per-spawn temp-file pattern.
// Paired with --setting-sources "" (which already excludes user/project/local
// settings files from loading) this is belt-and-suspenders: an explicit
// --settings pointing at a file with no hooks/preferences can't pick up
// ~/.claude/settings.json even if a future CLI version changes what "no
// sources" means.
func writeHermeticSettings() (path string, err error) {
	f, err := os.CreateTemp("", "mads-complete-settings-*.json")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write([]byte("{}")); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Complete implements provider.Completer: a hermetic, stateless one-shot
// completion. Every flag here was verified against the installed claude CLI
// before use (see testdata/complete_flags.md — two prior tasks in this repo
// died on fabricated flags). Unlike OneShot, Complete:
//
//   - runs in a fresh scratch directory (os.MkdirTemp), never req.Cwd or the
//     workspace — this alone kills CLAUDE.md/AGENTS.md discovery and project
//     settings, and any transcript this leaves behind lands under a
//     throwaway project slug, not the workspace;
//   - passes --safe-mode, which disables CLAUDE.md, hooks, MCP, skills,
//     plugins, and custom agents while leaving auth, model selection,
//     built-in tools, and permissions working normally. --bare was
//     considered and rejected: its help text says it forces
//     ANTHROPIC_API_KEY/apiKeyHelper-only auth, which would silently break
//     Fast Model for the common case of OAuth/keychain-authenticated
//     claude-code users;
//   - passes --setting-sources "" plus an explicit, per-call --settings file
//     with no hooks/preferences, so ~/.claude/settings.json never loads;
//   - passes --strict-mcp-config --mcp-config '{"mcpServers":{}}' so no MCP
//     server (including an ambient project .mcp.json) attaches;
//   - passes --tools "" (every tool denied);
//   - never overrides $HOME: doing so was tried and found to break OAuth
//     credential lookup (~/.claude/.credentials.json lives under the real
//     HOME), so hermeticity here comes from cwd + flags only;
//   - never appends cfg.ExtraArgs: those exist for other registries'
//     permission-mode flags on interactive/OneShot sessions (e.g. the
//     pipeline engine's --dangerously-skip-permissions), which must never
//     leak into a hermetic completion regardless of which Provider instance
//     is used;
//   - never uses --json-schema: JSON validation and the single bounded
//     repair-retry live in the fastmodel layer only, and --json-schema was
//     measured to add an internal tool_use round-trip with much higher, less
//     predictable token overhead — bad for the deterministic overhead budget
//     the hermeticity test pins (see testdata/complete_flags.md).
//
// MaxOutputTokens has no verified CLI equivalent (only --max-budget-usd, a
// dollar limit, exists) and is left unenforced here — best-effort only.
// JSONSchemaHint is intentionally unused by this provider; schema guidance,
// if a caller needs it, belongs in System/Prompt text instead.
func (p *Provider) Complete(ctx context.Context, req provider.CompleteRequest) (provider.CompleteResult, error) {
	model := req.Model
	if model == "" {
		model = DefaultCompleteModel
	}

	scratch, err := os.MkdirTemp("", "mads-complete-*")
	if err != nil {
		return provider.CompleteResult{}, fmt.Errorf("complete: scratch dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	settingsPath, err := writeHermeticSettings()
	if err != nil {
		return provider.CompleteResult{}, fmt.Errorf("complete: hermetic settings: %w", err)
	}
	defer func() { _ = os.Remove(settingsPath) }()

	args := []string{
		"-p", req.Prompt,
		"--output-format", "json",
		"--model", model,
		"--system-prompt", req.System,
		"--safe-mode",
		"--setting-sources", "",
		"--settings", settingsPath,
		"--strict-mcp-config",
		"--mcp-config", emptyMCPConfig,
		"--tools", "",
	}

	cmd := exec.CommandContext(ctx, p.cfg.Bin, args...)
	cmd.Dir = scratch
	cmd.Env = p.env(nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if rle := rateLimit(stderr.String() + stdout.String()); rle != nil {
			return provider.CompleteResult{}, rle
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if len(detail) > 600 {
			detail = detail[:600] + " …"
		}
		return provider.CompleteResult{}, fmt.Errorf("claude -p (complete): %w: %s", err, detail)
	}

	res, err := parseOneShot(stdout.Bytes())
	if err != nil {
		return provider.CompleteResult{}, err
	}
	return provider.CompleteResult{Text: res.Text, Usage: res.Usage}, nil
}
