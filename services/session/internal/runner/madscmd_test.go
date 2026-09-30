package runner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveMadsCmdOrder: explicit flag, then the sibling of this binary,
// then $PATH, then the bare name — so a repo-launched Router (bin/mads beside
// bin/session, nothing on PATH) still gets working orchestrate tools.
func TestResolveMadsCmdOrder(t *testing.T) {
	onPath := func(string) (string, error) { return "/usr/local/bin/mads", nil }
	notOnPath := func(string) (string, error) { return "", errors.New("not found") }
	siblingExists := func(p string) bool { return p == "/repo/bin/mads" }
	noSibling := func(string) bool { return false }

	cases := []struct {
		name     string
		explicit string
		isExec   func(string) bool
		lookPath func(string) (string, error)
		want     string
	}{
		{"explicit wins over everything", "/opt/mads/mads", siblingExists, onPath, "/opt/mads/mads"},
		{"sibling beats PATH", "", siblingExists, onPath, "/repo/bin/mads"},
		{"PATH when no sibling", "", noSibling, onPath, "/usr/local/bin/mads"},
		{"bare name as last resort", "", noSibling, notOnPath, "mads"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMadsCmd(tc.explicit, "/repo/bin/session", tc.isExec, tc.lookPath); got != tc.want {
				t.Errorf("resolveMadsCmd = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIsExecutable: the sibling must be a real executable file — a
// directory or a non-executable file named mads doesn't count.
func TestIsExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mads")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "notes")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isExecutable(exe) || isExecutable(plain) || isExecutable(dir) || isExecutable(filepath.Join(dir, "missing")) {
		t.Error("isExecutable misclassified a path")
	}
}
