package preflight

import (
	"context"

	"github.com/drewkidwell/mads/pkg/binlocate"
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
}

// knownProviderBinaries is deliberately small and honest: one entry per
// provider adapter this repo actually ships (providers/claudecode,
// providers/kirocli, providers/goosecli).
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
		// (" 1.46.0" / " 1.48.0" — docs/GOOSE-ADAPTER-DESIGN.md);
		// parseSemverish tolerates that.
		ParseVersion:   parseSemverish,
		InstallHint:    "install goose Desktop, or the goose CLI with `goose` on PATH",
		InstallURL:     "https://goose-docs.ai/docs/getting-started/installation",
		InstallCommand: "curl -fsSL https://github.com/aaif-goose/goose/releases/download/stable/download_cli.sh | bash",
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
	out, err := runVersion(ctx, path, spec.VersionArgs...)
	if err != nil {
		return Check{Name: checkName, Healthy: false, Detail: "found at " + path + " but version check failed: " + err.Error(), FixHint: spec.InstallHint}
	}
	version, ok := spec.ParseVersion(out)
	if !ok {
		return Check{Name: checkName, Healthy: true, Detail: "found at " + path + "; version could not be parsed from output"}
	}
	if compat, known := compatMatrix[name]; known {
		if ok, guidance := compat.evaluate(version); !ok {
			return Check{Name: checkName, Healthy: false, Detail: name + " v" + version + " detected", FixHint: guidance}
		}
	}
	return Check{Name: checkName, Healthy: true, Detail: name + " v" + version}
}
