// Package binlocate resolves an agent harness's executable the same way for
// every caller — the provider adapter that spawns it and pkg/preflight that
// reports on it — so the two can never disagree about whether a harness is
// installed. PATH wins; after that, a short list of known install locations
// that deliberately aren't on PATH. The motivating case: goose Desktop on
// macOS ships the full goose CLI inside its app bundle
// (Goose.app/Contents/Resources/bin/goose, per goose's own
// remote-goose-server docs) and never links it onto PATH.
//
// $MADS_AGENT_BIN_DIR (EnvAgentBinDir) confines every agent-binary lookup
// to one directory, so hermetic tests and smoke harnesses can never start a
// real harness: see Find and Resolve.
package binlocate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// knownLocations is the per-binary fallback list, tried in order after PATH.
// A leading "~/" expands to the user's home directory. Only add a location
// a vendor actually documents or ships — a guessed path that happens to
// hold some other "goose" would be exec'd as a harness.
var knownLocations = map[string][]string{
	"goose": {
		"/Applications/Goose.app/Contents/Resources/bin/goose",
		"~/Applications/Goose.app/Contents/Resources/bin/goose",
	},
	// hermes-acp: hermes's installer links its entry points into
	// ~/.local/bin (setup-hermes.sh:216, bin_dir="$HOME/.local/bin") and
	// only offers to add that to the shell's PATH — a desktop-launched mads
	// often doesn't have it.
	"hermes-acp": {
		"~/.local/bin/hermes-acp",
	},
	// codex (providers/codexcli): the standalone installer's link and the
	// release it points at (~/.local/bin/codex →
	// ~/.codex/packages/standalone/current/bin/codex), then the CLI the
	// ChatGPT desktop app ships inside its bundle — all observed on a codex
	// 0.154.0 machine, none on a desktop app's PATH by default.
	"codex": {
		"~/.local/bin/codex",
		"~/.codex/packages/standalone/current/bin/codex",
		"/Applications/ChatGPT.app/Contents/Resources/codex",
		"~/Applications/ChatGPT.app/Contents/Resources/codex",
	},
	// mergiraf (pkg/mergedriver): Homebrew's documented prefixes (Apple
	// silicon, Intel macOS, Linuxbrew) and cargo's install directory. An
	// app launched from the desktop often has none of them on PATH.
	"mergiraf": {
		"/opt/homebrew/bin/mergiraf",
		"/usr/local/bin/mergiraf",
		"/home/linuxbrew/.linuxbrew/bin/mergiraf",
		"~/.cargo/bin/mergiraf",
	},
}

// EnvAgentBinDir names the environment variable that, when non-empty,
// confines agent-binary lookups to that one directory: Find answers only
// $MADS_AGENT_BIN_DIR/<name>, and only if it is an executable file — never a
// PATH hit, never a known install location. Hermetic test and smoke
// harnesses (ui/e2e/lib/topology.ts, scripts/e2esmoke, the router boot test)
// point it at a stand-in directory, empty unless a test drops a script in,
// so a service they boot can't reach a real claude, goose or kiro-cli
// through model discovery, preflight or a session spawn. It is
// pkg/mergedriver's $MADS_MERGIRAF idea applied to every agent harness.
//
// Scope: it governs every name except the ones in notAgent (today just
// mergiraf, which has its own $MADS_MERGIRAF override and isn't a harness).
// Governing by exclusion rather than an allowlist of harness names means a
// newly added adapter is hermetic by default — forgetting to register its
// binary name can't open a leak.
const EnvAgentBinDir = "MADS_AGENT_BIN_DIR"

// notAgent lists the binlocate names that are not agent harnesses and so
// are untouched by $MADS_AGENT_BIN_DIR.
var notAgent = map[string]bool{"mergiraf": true}

// agentBinDir returns $MADS_AGENT_BIN_DIR when it governs name, else "".
func agentBinDir(name string) string {
	if notAgent[name] {
		return ""
	}
	return os.Getenv(EnvAgentBinDir)
}

// Find returns an absolute path to name: exec.LookPath's answer if it has
// one, else the first known location holding an executable regular file.
// When neither resolves it returns LookPath's error, so callers' "not found
// on PATH" reporting is unchanged. With $MADS_AGENT_BIN_DIR set it answers
// only from that directory (see EnvAgentBinDir).
func Find(name string) (string, error) {
	if dir := agentBinDir(name); dir != "" {
		p := filepath.Join(dir, name)
		if isExecutable(p) {
			return p, nil
		}
		return "", &exec.Error{Name: name, Err: fmt.Errorf("not in $%s (%s)", EnvAgentBinDir, dir)}
	}
	path, lookErr := exec.LookPath(name)
	if lookErr == nil {
		return path, nil
	}
	for _, loc := range knownLocations[name] {
		if rest, ok := strings.CutPrefix(loc, "~/"); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				continue
			}
			loc = filepath.Join(home, rest)
		}
		if isExecutable(loc) {
			return loc, nil
		}
	}
	return "", lookErr
}

// Resolve returns the path an adapter should exec for name: Find's answer
// when it has one; else, with $MADS_AGENT_BIN_DIR set, $MADS_AGENT_BIN_DIR/
// <name> anyway (it doesn't exist, so exec fails fast with "no such file"
// and never consults PATH); else the bare name, so exec's PATH lookup and
// its "not found" error read as they always have.
//
// Every provider adapter must default its binary through Resolve rather
// than a bare name or Find-then-bare-name: a bare name is resolved by exec
// against PATH, which would walk straight past $MADS_AGENT_BIN_DIR to a real
// harness in a hermetic test.
func Resolve(name string) string {
	if p, err := Find(name); err == nil {
		return p
	}
	if dir := agentBinDir(name); dir != "" {
		return filepath.Join(dir, name)
	}
	return name
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
