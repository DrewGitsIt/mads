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
