package preflight

import (
	"context"
	"os/exec"
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
}

// knownProviderBinaries is deliberately small and honest: one entry per
// provider adapter this repo actually ships (providers/claudecode,
// providers/goosecli). Add kiro-cli's entry here when W7.1 lands that
// adapter — nothing else in this file changes.
var knownProviderBinaries = map[string]providerBinary{
	"claude-code": {
		Bin:          "claude",
		VersionArgs:  []string{"--version"},
		ParseVersion: parseSemverish,
		InstallHint:  "install Claude Code (https://docs.claude.com/en/docs/claude-code) and ensure `claude` is on PATH",
	},
	"goose": {
		Bin:         "goose",
		VersionArgs: []string{"--version"},
		// `goose --version` prints the bare version with a leading space
		// (" 1.46.0" / " 1.48.0" — docs/GOOSE-ADAPTER-DESIGN.md);
		// parseSemverish tolerates that.
		ParseVersion: parseSemverish,
		InstallHint:  "install goose (https://block.github.io/goose/) and ensure `goose` is on PATH",
	},
}

func checkProviderBinary(ctx context.Context, name string, spec providerBinary) Check {
	checkName := "provider:" + name
	path, err := exec.LookPath(spec.Bin)
	if err != nil {
		return Check{Name: checkName, Healthy: false, Detail: spec.Bin + " not found on PATH", FixHint: spec.InstallHint}
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
