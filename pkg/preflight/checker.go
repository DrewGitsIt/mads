package preflight

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// DataHealth is satisfied by *dataclient.Client. A separate interface here —
// not an import of pkg/dataclient's concrete type — keeps this package
// testable with a fake and keeps the dependency direction (preflight is a
// leaf package, doesn't need dataclient's full surface).
type DataHealth interface {
	Health(ctx context.Context) error
}

// ProviderLister is satisfied by *provider.Registry.
type ProviderLister interface {
	Names() []string
}

// Deps wires the real Checker to its live counterparts.
type Deps struct {
	Data      DataHealth
	Providers ProviderLister
	// MadsHome is the worktree/credentials root (services/router/main.go's
	// defaultMadsHome()). Disk space is checked against this path, not the
	// binary's own filesystem.
	MadsHome string
}

// New returns the real environment-health Checker (spec §14.8/14.9).
func New(deps Deps) Checker { return &realChecker{deps: deps} }

type realChecker struct{ deps Deps }

// Run implements Checker. Its own error return is reserved for "the whole
// run could not happen" — nothing here ever hits that; every individual
// probe failure becomes an unhealthy Check row instead, since a broken
// environment is data, not a 500.
func (c *realChecker) Run(ctx context.Context) ([]Check, error) {
	checks := []Check{
		checkGit(ctx),
		checkDiskSpace(c.deps.MadsHome),
		checkDataService(ctx, c.deps.Data),
		checkMergiraf(ctx),
	}
	for _, name := range c.deps.Providers.Names() {
		if bin, ok := knownProviderBinaries[name]; ok {
			checks = append(checks, checkProviderBinary(ctx, name, bin))
		}
	}
	return checks, nil
}

func checkGit(ctx context.Context) Check {
	path, err := exec.LookPath("git")
	if err != nil {
		return Check{
			Name: "git", Healthy: false,
			Detail:  "not found on PATH",
			FixHint: "install git (https://git-scm.com/downloads) — required for workspace and worktree operations",
		}
	}
	out, err := runVersion(ctx, path, "--version")
	if err != nil {
		return Check{Name: "git", Healthy: false, Detail: "found at " + path + " but --version failed: " + err.Error(), FixHint: "reinstall git"}
	}
	return Check{Name: "git", Healthy: true, Detail: strings.TrimSpace(out)}
}

// runVersion runs bin with args under a bounded timeout and returns combined
// stdout — every probe in this package is a quick local subprocess call,
// never network-dependent, so 5s is generous, not a real constraint.
func runVersion(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	return string(out), err
}

func checkDiskSpace(madsHome string) Check {
	dir := nearestExistingAncestor(madsHome)
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return Check{Name: "disk_space", Healthy: false, Detail: "could not stat " + dir + ": " + err.Error(), FixHint: "verify " + dir + " is on a mounted, readable filesystem"}
	}
	// stat.Bavail/Bsize are int64 on darwin, uint64 on linux — explicit
	// conversion is required on at least one GOOS, so keep it on both rather
	// than build-tag-fork.
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	const minFree = 1 << 30 // 1 GiB — isolated pipeline runs create full working-tree copies under this root
	detail := fmt.Sprintf("%.1f GiB free at %s", float64(free)/(1<<30), dir)
	if free < minFree {
		return Check{Name: "disk_space", Healthy: false, Detail: detail, FixHint: "free up disk space under " + dir + " — isolated pipeline runs need room for full working-tree copies"}
	}
	return Check{Name: "disk_space", Healthy: true, Detail: detail}
}

// nearestExistingAncestor climbs from path until it finds a directory that
// actually exists — MadsHome itself may not exist yet on a fresh install
// (the wizard runs before anything has written to it).
func nearestExistingAncestor(path string) string {
	for path != "" && path != "/" && path != "." {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		path = filepath.Dir(path)
	}
	return "/"
}

func checkDataService(ctx context.Context, d DataHealth) Check {
	if d == nil {
		return Check{Name: "data_service", Healthy: false, Detail: "no client configured", FixHint: "internal error — report this"}
	}
	if err := d.Health(ctx); err != nil {
		return Check{Name: "data_service", Healthy: false, Detail: err.Error(), FixHint: "start the Data Service (make -C services/data run) or check the Router's -data-url flag"}
	}
	return Check{Name: "data_service", Healthy: true}
}
