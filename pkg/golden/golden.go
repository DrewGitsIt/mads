// Package golden pins wire formats and generated output to files under a
// package's testdata/ directory. Run tests with -update to regenerate:
//
//	go test ./pkg/contracts -update
//
// Contract tests use it from both sides of a boundary: the producer asserts
// what it writes matches the golden; the consumer parses the same golden. A
// contract change then fails both suites until the golden is regenerated and
// both sides agree again (spec §15, contract tests).
package golden

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files with current output")

// Assert compares got against testdata/<name>. With -update it rewrites the
// file instead and passes.
func Assert(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("golden: mkdir for %s: %v", path, err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("golden: write %s: %v", path, err)
		}
		return
	}
	want := Read(t, name)
	if !bytes.Equal(got, want) {
		t.Errorf("golden mismatch for %s (run with -update to regenerate)\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// Read returns the contents of testdata/<name>, failing the test if missing.
func Read(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden: read %s (run with -update to create): %v", path, err)
	}
	return b
}
