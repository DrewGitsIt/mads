package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// maxLandRetries bounds how many times a single land attempt retries after a
// gate failure triaged as a flake (spec: "never retry unboundedly" — flaky
// tests are the canonical merge-queue killer, and bounded retry with
// recorded evidence is the standard countermeasure).
const maxLandRetries = 2

// maxIsolationRuns bounds how many times a package-attributable gate failure
// reruns in isolation before giving up on ever seeing it go green.
const maxIsolationRuns = 3

// triageRun records one isolation rerun's command and outcome, for the
// evidence a block note or land.retried event carries.
type triageRun struct {
	command string
	passed  bool
	output  string
}

// gateTriage is the auto-triage's verdict on one gate failure: whether the
// isolation reruns (same worktree, serial — the land queue already holds
// exclusivity, that's the isolation) ever went green.
type gateTriage struct {
	classification string // "flake" or "deterministic"
	packages       []string
	runs           []triageRun
}

// failPackageRe matches go test's own per-package FAIL summary line (e.g.
// "FAIL\tgithub.com/x/y/pkg/foo\t0.023s"), not the bare "FAIL" a build
// failure or a panic prints alone.
var failPackageRe = regexp.MustCompile(`(?m)^FAIL\s+(\S+)\s`)

// parseFailingGoPackages extracts the distinct packages named in go test's
// FAIL lines within gate output, in first-seen order. Nil when the failure
// isn't package-attributable (a UI suite, lint, or any non-`go test` gate).
func parseFailingGoPackages(output string) []string {
	seen := map[string]bool{}
	var packages []string
	for _, m := range failPackageRe.FindAllStringSubmatch(output, -1) {
		pkg := m[1]
		if pkg == "" || seen[pkg] {
			continue
		}
		seen[pkg] = true
		packages = append(packages, pkg)
	}
	return packages
}

// triageGateFailure reruns the failing portion of a gate command, in the
// same worktree, to distinguish a flake from a real regression before the
// land attempt blocks (spec: "the land queue's failure modes split three
// ways... this task makes the engine distinguish [flake] from [real]
// itself"). When gateOutput names specific Go packages (go test's FAIL
// lines), those packages alone are rerun under -race -count=1, up to
// maxIsolationRuns times, stopping at the first green — a deterministic
// failure never gets more than maxIsolationRuns chances, so it never costs
// more than this one triage round. When the failure isn't
// package-attributable (a UI suite, lint, or any command whose output
// doesn't look like go test), the gate command itself reruns exactly once.
// All reruns are serial and never emit their own stage.* events — only the
// official gate run (landAttempt's caller) does; the isolation reruns are
// evidence-gathering, not stage attempts in their own right.
func (e *engine) triageGateFailure(ctx context.Context, dir, gateCmd, gateOutput string) gateTriage {
	if packages := parseFailingGoPackages(gateOutput); len(packages) > 0 {
		cmd := "go test -race -count=1 " + strings.Join(packages, " ")
		var runs []triageRun
		for i := 0; i < maxIsolationRuns; i++ {
			out, err := e.runCommand(ctx, cmd, dir)
			passed := err == nil
			runs = append(runs, triageRun{command: cmd, passed: passed, output: out})
			if passed {
				return gateTriage{classification: "flake", packages: packages, runs: runs}
			}
		}
		return gateTriage{classification: "deterministic", packages: packages, runs: runs}
	}

	out, err := e.runCommand(ctx, gateCmd, dir)
	classification := "deterministic"
	if err == nil {
		classification = "flake"
	}
	return gateTriage{classification: classification, runs: []triageRun{{command: gateCmd, passed: err == nil, output: out}}}
}

// evidence renders the triage's findings for a block note (spec: "the block
// note MUST carry the evidence: failing packages, isolation rerun results,
// trunk head, and the existing suspect-set wording") — appended after the
// gate failure's own suspect-set message, never replacing it.
func (t gateTriage) evidence(trunkHead string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\ngate auto-triage: classification=%s, trunk_head=%s\n", t.classification, trunkHead)
	if len(t.packages) > 0 {
		fmt.Fprintf(&b, "failing packages: %s\n", strings.Join(t.packages, ", "))
	} else {
		b.WriteString("failing packages: none identified (not attributable to specific go test packages)\n")
	}
	fmt.Fprintf(&b, "isolation reruns (%d):\n", len(t.runs))
	for i, r := range t.runs {
		result := "FAIL"
		if r.passed {
			result = "PASS"
		}
		fmt.Fprintf(&b, "- run %d: %s — %s\n", i+1, result, r.command)
	}
	return b.String()
}
