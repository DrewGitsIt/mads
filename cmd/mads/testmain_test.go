package main

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points $MADS_HOME at a scratch dir for the whole package: every
// subcommand reads the install token from there, and a test must never
// read (or create) the real user's ~/.mads/local.token.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mads-cli-test-home")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("MADS_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
