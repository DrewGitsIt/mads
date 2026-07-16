package update

import (
	"strconv"
	"strings"
)

// compareSemver compares dotted version strings by numeric component, left
// to right, missing trailing components treated as 0 ("2.1" == "2.1.0").
// Returns -1/0/1.
//
// Intentional small duplication of pkg/preflight's unexported compareSemver
// (same repo precedent as cmd/mads/uninstall.go's doc comment: kept
// duplicated rather than a shared package for two functions this size — if
// a reviewer wants it deduplicated, the honest fix is promoting it to a
// shared leaf package both preflight and update import).
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
