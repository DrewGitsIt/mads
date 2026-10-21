package preflight

import (
	"context"
	"os"

	"github.com/drewkidwell/mads/pkg/binlocate"
	"github.com/drewkidwell/mads/pkg/hermesinstall"
)

// providerBinary describes how to detect and version-probe one provider's
// underlying binary. Keyed by provider.Provider.Name() in
// knownProviderBinaries below — only providers actually registered in the
// Router's provider.Registry are ever probed, and only if this package
// knows how.
type providerBinary struct {
	Bin          string
	VersionArgs  []string
	ParseVersion func(output string) (version string, ok bool)
	InstallHint  string
	// InstallURL/InstallCommand: the vendor's install page and its
	// documented macOS/Linux one-liner, verbatim — never a command of our
	// own invention.
	InstallURL     string
	InstallCommand string
	// Ready, when set, is asked before the version probe runs: a non-nil
	// error means running the binary at all would do harm, so the check
	// reports the provider not installed (with the error as detail) and
	// never probes. hermes: running hermes-acp under a home with no
	// dependency install starts a ~600 MB one (pkg/hermesinstall). Even
	// `--version`, which hermes exempts from that today
	// (venv_sync.py:363), isn't relied on.
	Ready func(path string) error
}

// knownProviderBinaries is deliberately small and honest: one entry per
// provider adapter this repo actually ships (providers/claudecode,
// providers/kirocli, providers/goosecli, providers/hermescli,
// providers/codexcli).
var knownProviderBinaries = map[string]providerBinary{
	"claude-code": {
		Bin:            "claude",
		VersionArgs:    []string{"--version"},
		ParseVersion:   parseSemverish,
		InstallHint:    "install Claude Code and ensure `claude` is on PATH",
		InstallURL:     "https://code.claude.com/docs/en/setup",
		InstallCommand: "curl -fsSL https://claude.ai/install.sh | bash",
	},
	"kiro-cli": {
		Bin:            "kiro-cli",
		VersionArgs:    []string{"--version"},
		ParseVersion:   parseSemverish,
		InstallHint:    "install Kiro CLI and ensure `kiro-cli` is on PATH",
		InstallURL:     "https://kiro.dev/docs/cli/",
		InstallCommand: "curl -fsSL https://cli.kiro.dev/install | bash",
	},
	"goose": {
		Bin:         "goose",
		VersionArgs: []string{"--version"},
		// `goose --version` prints the bare version with a leading space
		// (" 1.46.0" / " 1.48.0" / " 1.50.1" — docs/GOOSE-ADAPTER-DESIGN.md);
		// parseSemverish tolerates that.
		ParseVersion:   parseSemverish,
		InstallHint:    "install goose Desktop, or the goose CLI with `goose` on PATH",
		InstallURL:     "https://goose-docs.ai/docs/getting-started/installation",
		InstallCommand: "curl -fsSL https://github.com/aaif-goose/goose/releases/download/stable/download_cli.sh | bash",
	},
	"hermes": {
		// The adapter spawns hermes-acp, not the `hermes` CLI, so that is
		// the binary probed. `hermes-acp --version` prints the bare version
		// ("0.22.2", acp_adapter/entry.py _print_version).
		Bin:            "hermes-acp",
		VersionArgs:    []string{"--version"},
		ParseVersion:   parseSemverish,
		InstallHint:    "install Hermes Agent and ensure `hermes-acp` is on PATH (the installer puts it in ~/.local/bin)",
		InstallURL:     "https://hermes-agent.nousresearch.com/docs/getting-started/installation",
		InstallCommand: "curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash",
		Ready: func(path string) error {
			return hermesinstall.Check(path, os.Environ())
		},
	},
	"codex": {
		Bin:         "codex",
		VersionArgs: []string{"--version"},
		// `codex --version` prints "codex-cli 0.154.0" (the ChatGPT app's
		// bundled copy "codex-cli 0.154.0-alpha.6.2"); parseSemverish takes
		// the first N.N.N.
		ParseVersion:   parseSemverish,
		InstallHint:    "install the Codex CLI and ensure `codex` is on PATH (signing in with ChatGPT happens in codex itself)",
		InstallURL:     "https://github.com/openai/codex",
		InstallCommand: "npm install -g @openai/codex",
	},
}

func checkProviderBinary(ctx context.Context, name string, spec providerBinary) Check {
	checkName := "provider:" + name
	path, err := binlocate.Find(spec.Bin)
	if err != nil {
		return Check{
			Name: checkName, Healthy: false, NotInstalled: true,
			Detail:     spec.Bin + " not found on PATH or in known install locations",
			FixHint:    spec.InstallHint,
			InstallURL: spec.InstallURL, InstallCommand: spec.InstallCommand,
		}
	}
	if spec.Ready != nil {
		if err := spec.Ready(path); err != nil {
			return Check{
				Name: checkName, Healthy: false, NotInstalled: true,
				Detail:  "found at " + path + " but not usable here: " + err.Error(),
				FixHint: spec.InstallHint, InstallURL: spec.InstallURL, InstallCommand: spec.InstallCommand,
			}
		}
	}
	out, err := runVersion(ctx, path, spec.VersionArgs...)
	if err != nil {
		return Check{Name: checkName, Healthy: false, Detail: "found at " + path + " but version check failed: " + err.Error(), FixHint: spec.InstallHint}
	}
	version, ok := spec.ParseVersion(out)
	if !ok {
		return Check{Name: checkName, Healthy: true, Detail: "found at " + path + "; version could not be parsed from output"}
	}
	if compat, known := compatMatrix[name]; known {
		if ok, unproven, guidance := compat.evaluate(version); !ok {
			return Check{Name: checkName, Healthy: false, Warning: unproven, Detail: name + " v" + version + " detected", FixHint: guidance, Version: version}
		}
	}
	return Check{Name: checkName, Healthy: true, Detail: name + " v" + version, Version: version}
}
