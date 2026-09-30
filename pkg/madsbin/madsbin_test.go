package madsbin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathPrefersMadsBinEnv(t *testing.T) {
	t.Setenv("MADS_BIN", "/opt/mads/bin/mads")
	if got := Path(); got != "/opt/mads/bin/mads" {
		t.Errorf("Path() = %q, want $MADS_BIN", got)
	}
}

func TestPathSkipsWhitespacePaths(t *testing.T) {
	t.Setenv("MADS_BIN", "/Applications/My Apps/mads")
	t.Setenv("PATH", "")
	// The test binary has no "mads" sibling, so with $MADS_BIN unusable and
	// nothing on PATH the plain name is all that's left.
	if got := Path(); got != "mads" {
		t.Errorf("Path() = %q, want the plain fallback", got)
	}
}

func TestPathFindsMadsOnPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, binName())
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MADS_BIN", "")
	t.Setenv("PATH", dir)
	if got := Path(); got != bin {
		t.Errorf("Path() = %q, want %q", got, bin)
	}
}

// The case that matters for a Router-spawned session child: nothing on
// PATH, but mads built beside the running binary (bin/session, bin/mads).
func TestPathFindsSiblingOfExecutable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path:", err)
	}
	sibling := filepath.Join(filepath.Dir(exe), binName())
	if _, err := os.Stat(sibling); err == nil {
		t.Skip("a mads already sits beside the test binary")
	}
	if err := os.WriteFile(sibling, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Skip("can't write beside the test binary:", err)
	}
	t.Cleanup(func() { _ = os.Remove(sibling) })
	t.Setenv("MADS_BIN", "")
	t.Setenv("PATH", "")
	if got := Path(); got != sibling {
		t.Errorf("Path() = %q, want the sibling %q", got, sibling)
	}
}
