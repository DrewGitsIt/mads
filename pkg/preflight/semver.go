package preflight

import (
	"regexp"
	"strconv"
	"strings"
)

var versionPattern = regexp.MustCompile(`\d+(?:\.\d+){1,2}`)

// parseSemverish extracts the first "N.N" or "N.N.N" substring from
// free-form --version output (e.g. "2.1.3 (Claude Code)" -> "2.1.3").
// Deliberately not a general semver library — no pre-release/build-metadata
// handling; provider CLI --version output doesn't use those.
func parseSemverish(output string) (string, bool) {
	m := versionPattern.FindString(output)
	return m, m != ""
}

// compareSemver compares dotted version strings by numeric component, left
// to right, missing trailing components treated as 0 ("2.1" == "2.1.0").
// Returns -1/0/1.
func compareSemver(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var an, bn int
		if i < len(as) {
			an, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bn, _ = strconv.Atoi(bs[i])
		}
		if an != bn {
			if an < bn {
				return -1
			}
			return 1
		}
	}
	return 0
}
