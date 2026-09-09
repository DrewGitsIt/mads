package preflight

import "fmt"

// compatRange is one provider's proven-compatible version window against
// this mads build. Max is exclusive; an empty Min/Max means no known
// floor/ceiling yet (untested, not declared incompatible — don't
// manufacture a warning from absence of data).
type compatRange struct {
	Min, Max string
	Note     string
}

// compatMatrix is seeded honestly: only claude-code has actually been run
// through providers/conformance at a known version, and only the 2.1.x
// line has been proven. Add a row only once a provider/version pair has
// actually been exercised — this table is a record of what's proven, not a
// prediction of what will probably work. When mads itself starts cutting
// versioned releases (W7.5), this becomes keyed by mads version too; until
// then there is exactly one build to be compatible with, so provider name
// is the whole key.
var compatMatrix = map[string]compatRange{
	"claude-code": {Min: "2.1.0", Max: "2.2.0", Note: "proven against providers/conformance"},
	// goose: proven 1.46–1.48 — the early wire probes ran against the
	// 1.46.0 binary, and every committed capture plus the full source
	// re-verification is 1.48.0 (docs/GOOSE-ADAPTER-DESIGN.md,
	// "Re-verified against source 1.48.0"). The version-sensitive
	// surfaces are the _goose/unstable system-prompt method,
	// --output-format stream-json, and the ACP configOptions shape; the
	// 1.46→1.48 deltas (including the ≤1.46 mcpServers suppression) are
	// listed in that re-verification note. Max is exclusive, so 1.48.x
	// passes and 1.49+ warns as unproven.
	"goose": {Min: "1.46.0", Max: "1.49.0", Note: "proven against goose 1.46.0 (binary probes) and 1.48.0 (captures + source re-verification) — docs/GOOSE-ADAPTER-DESIGN.md"},
}

func (r compatRange) evaluate(version string) (ok bool, guidance string) {
	if r.Min != "" && compareSemver(version, r.Min) < 0 {
		return false, fmt.Sprintf("mads requires >= %s, detected %s — upgrade the provider", r.Min, version)
	}
	if r.Max != "" && compareSemver(version, r.Max) >= 0 {
		return false, fmt.Sprintf("mads has only been proven against < %s, detected %s — this version may include unproven breaking changes, check release notes before relying on it", r.Max, version)
	}
	return true, ""
}
