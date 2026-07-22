// Package agentenv is the single source of truth for constructing the
// environment a spawned agent process (provider CLI, PTY, ACP transport)
// sees. It exists so that "inherit" and "isolated" env_mode semantics are
// defined exactly once and reused at every spawn site, rather than
// reimplemented per call site with the risk of drifting apart.
package agentenv

import "strings"

// Mode selects how much of the parent process's environment is visible to a
// spawned agent. Mirrors provider.Trust: this package defines its own type
// independent of pkg/contracts, so callers cast explicitly at the boundary
// (agentenv.Mode(cfg.EnvMode)) rather than importing contracts here.
type Mode string

const (
	// ModeInherit reproduces today's behavior exactly: the agent sees the
	// full parent environment. Also the zero value, so an unset/empty mode
	// behaves as inherit without any special-casing by callers.
	ModeInherit Mode = "inherit"
	// ModeIsolated restricts the agent to BaseAllowlist plus the profile's
	// explicit passthrough names, plus mads-injected credentials.
	ModeIsolated Mode = "isolated"
)

// BaseAllowlist is the fixed set of parent env var names visible to an
// isolated agent before any per-profile passthrough is added: everything a
// normal OS process needs to run (locate binaries, resolve $HOME, find a
// shell, write temp files) and nothing that could carry a secret. LC_* is
// handled separately by prefix, since locale var names aren't a fixed set.
var BaseAllowlist = []string{
	"PATH",
	"HOME",
	"USER",
	"LOGNAME",
	"SHELL",
	"TERM",
	"LANG",
	"TZ",
	"TMPDIR",
	"XDG_RUNTIME_DIR",
}

// Build constructs the environment for a spawned agent process.
//
// parentEnv is the spawning process's own environment (os.Environ()).
// credentials and extra are KEY=VALUE pairs mads injects itself — resolved
// bound mads credentials, and per-call values such as capability-scoped
// credentials, respectively — and are always included regardless of mode,
// since they never originate from parentEnv and so aren't something
// isolation needs to filter. passthrough is the profile's env_passthrough
// NAMES; only consulted in isolated mode.
//
// In ModeInherit (and any other value, including the zero value), the
// result is parentEnv, then credentials, then extra — byte-identical to the
// pre-env_mode behavior. In ModeIsolated, the result starts from parentEnv
// filtered to BaseAllowlist, LC_* names, and passthrough names, then adds
// credentials and extra the same way.
func Build(mode Mode, parentEnv []string, credentials, passthrough, extra []string) []string {
	base := parentEnv
	if mode == ModeIsolated {
		base = filterEnv(parentEnv, passthrough)
	}
	env := make([]string, 0, len(base)+len(credentials)+len(extra))
	env = append(env, base...)
	env = append(env, credentials...)
	env = append(env, extra...)
	return env
}

// Select extracts the KEY=VALUE pairs from env whose KEY is in names,
// preserving names' order. It lets a service that already has bound
// credential values in its own environment (materialized there by the
// Router at spawn time per spec §13 — names cross the service boundary,
// values don't) build the credentials argument to Build without re-resolving
// against a vault.
func Select(env []string, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	values := make(map[string]string, len(env))
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		values[name] = value
	}
	selected := make([]string, 0, len(names))
	for _, name := range names {
		if value, ok := values[name]; ok {
			selected = append(selected, name+"="+value)
		}
	}
	return selected
}

func filterEnv(parentEnv []string, passthrough []string) []string {
	allowed := make(map[string]bool, len(BaseAllowlist)+len(passthrough))
	for _, name := range BaseAllowlist {
		allowed[name] = true
	}
	for _, name := range passthrough {
		allowed[name] = true
	}
	var filtered []string
	for _, kv := range parentEnv {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if allowed[name] || strings.HasPrefix(name, "LC_") {
			filtered = append(filtered, kv)
		}
	}
	return filtered
}
