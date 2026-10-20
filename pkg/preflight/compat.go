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
	// goose: proven 1.46–1.50 — the early wire probes ran against the
	// 1.46.0 binary, the full source re-verification is 1.48.0
	// (docs/GOOSE-ADAPTER-DESIGN.md, "Re-verified against source 1.48.0"),
	// and the committed captures were re-taken against 1.50.1 on
	// 2026-10-20 (the binary Goose Desktop ships and auto-updates, which
	// binlocate resolves). The version-sensitive surfaces are the
	// _goose/unstable system-prompt method, --output-format stream-json,
	// and the ACP configOptions shape; the 1.46→1.48 deltas (including the
	// ≤1.46 mcpServers suppression) and the one 1.48→1.50.1 wire change (a
	// cancelled prompt now resolves stopReason "cancelled", not "end_turn")
	// are listed in the design doc. Max is exclusive, so 1.50.x passes and
	// 1.51+ warns as unproven.
	"goose": {Min: "1.46.0", Max: "1.51.0", Note: "proven against goose 1.46.0 (binary probes), 1.48.0 (source re-verification) and 1.50.1 (captures re-verified 2026-10-20) — docs/GOOSE-ADAPTER-DESIGN.md"},
}

// evaluate reports whether version sits in the proven window. Below Min is
// a known incompatibility; at or above Max is only unproven — likely fine,
// so unproven=true lets the caller warn instead of fail.
func (r compatRange) evaluate(version string) (ok, unproven bool, guidance string) {
	if r.Min != "" && compareSemver(version, r.Min) < 0 {
		return false, false, fmt.Sprintf("mads requires >= %s, detected %s — upgrade the provider", r.Min, version)
	}
	if r.Max != "" && compareSemver(version, r.Max) >= 0 {
		return false, true, fmt.Sprintf("mads has only been proven against < %s, detected %s — this version may include unproven breaking changes, check release notes before relying on it", r.Max, version)
	}
	return true, false, ""
}
