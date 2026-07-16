//go:build release_gate

// Package releasegate holds the negative-proof test for
// scripts/release-gate.sh: the gate must fail a candidate whose "mads"
// binary fails its health check, not just silently pass. Tagged
// release_gate (mirrors scripts/e2esmoke's e2e_smoke tag) so a full
// cross-platform build matrix run only happens on demand
// (make release-gate), not in every `go test ./...`/`make check`/CI test job.
package releasegate

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReleaseGateFailsBrokenCandidate feeds scripts/release-gate.sh a
// candidate directory containing only a "mads" binary that exits non-zero
// on --version, via MADS_RELEASE_CANDIDATE_DIR (skipping the multi-minute
// build matrix — the gate's health check must fail long before the matrix
// or the install/update smokes would ever run). Asserts both that the gate
// exits non-zero and that it names the health-check step specifically, so a
// future regression that breaks the gate for an unrelated reason doesn't
// slip through an assertion too loose to tell the difference.
func TestReleaseGateFailsBrokenCandidate(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}

	candidate := t.TempDir()
	brokenMads := filepath.Join(candidate, "mads")
	if err := os.WriteFile(brokenMads, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { //nolint:gosec // test fixture, deliberately a broken executable
		t.Fatal(err)
	}

	cmd := exec.Command("sh", filepath.Join(root, "scripts", "release-gate.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MADS_RELEASE_CANDIDATE_DIR="+candidate)
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("release-gate.sh: want non-zero exit for a broken candidate, got success\n%s", out)
	}
	if !strings.Contains(string(out), "health check failed") {
		t.Fatalf("release-gate.sh: want failure output naming the health-check step, got:\n%s", out)
	}
}

func repoRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", os.ErrNotExist
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile))), nil // scripts/releasegate/ -> repo root
}
