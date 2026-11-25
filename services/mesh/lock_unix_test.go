//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLockInstance(t *testing.T) {
	home := t.TempDir()
	if err := lockInstance(home); err != nil {
		t.Fatal(err)
	}
	held := instanceLock
	t.Cleanup(func() { _ = held.Close(); instanceLock = nil })
	b, err := os.ReadFile(filepath.Join(home, "run", "mesh.lock"))
	if err != nil || strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock file %q, %v", b, err)
	}
	// A second Mesh Service for the same home is refused, naming the first.
	err = lockInstance(home)
	if err == nil || !strings.Contains(err.Error(), "pid "+strconv.Itoa(os.Getpid())) {
		t.Fatalf("second: %v", err)
	}
	if instanceLock != held {
		t.Fatal("a refused lock replaced the held one")
	}
	// Another home is its own.
	other := t.TempDir()
	if err := lockInstance(other); err != nil {
		t.Fatalf("other home: %v", err)
	}
	_ = instanceLock.Close()
	instanceLock = held
	// Released (the process gone), the next one takes it.
	_ = held.Close()
	if err := lockInstance(home); err != nil {
		t.Fatalf("after release: %v", err)
	}
	held = instanceLock
}
