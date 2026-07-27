package capability

import (
	"path/filepath"
	"runtime"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// repoRoot resolves the mads checkout root from this source file's own
// on-disk location, independent of whatever directory a caller's process or
// test binary happens to run from (mads always builds and runs from within
// its own checkout — see the Router's ui-dist flag for the same assumption
// stated explicitly — but shipped-capability paths must resolve correctly
// regardless of which package's test runs them, and go test always sets cwd
// to the test's own package directory, not the repo root).
var repoRoot = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}()

// ShippedCapabilities is the fixed list of capabilities mads ships out of
// the box (spec §12), mirroring the six system agents seeded in migration
// 0005_registries: pre-configured, editable, not deletable. Adding an entry
// here and dropping its skill directory under assets/skills/ is the only
// step needed to ship a new built-in skill — see
// services/data/internal/store.SeedShippedCapabilities, which registers
// these in the capability registry at Data Service startup.
var ShippedCapabilities = []contracts.Capability{
	{
		Name:        "playwright-ui-testing",
		Description: "Verify UI changes with Playwright: running ui-smoke, writing a throwaway spec, reading traces on failure.",
		Type:        contracts.CapabilitySkill,
		Config:      contracts.CapabilityConfig{Path: filepath.Join(repoRoot, "assets", "skills", "playwright-ui-testing")},
		System:      true,
	},
}
