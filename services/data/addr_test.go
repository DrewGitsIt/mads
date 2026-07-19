package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultAddrIsProductionPort locks in that a bare "./bin/data" launch
// binds :8081, the port the Router's -data-url default and every other
// service in the stack expect — not the ephemeral "localhost:0" the
// runtime-isolated-gates change briefly made the default. Every gate/e2e
// path passes -addr explicitly, so ephemeral-by-default belongs there, not
// in the flag's own default. Asserted via -h, which flag.Parse prints and
// exits(0) on before main ever reaches net.Listen — so this can't itself
// bind the real port and collide with a concurrently running gate.
func TestDefaultAddrIsProductionPort(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "data")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "-h").CombinedOutput()
	if err != nil {
		t.Fatalf("-h: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `listen address (default "localhost:8081")`) {
		t.Fatalf("data's default -addr changed, want localhost:8081:\n%s", out)
	}
}
