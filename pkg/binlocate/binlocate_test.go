package binlocate

import (
	"os"
	"path/filepath"
	"testing"
)

func writeExe(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func withLocations(t *testing.T, name string, locs ...string) {
	t.Helper()
	saved := knownLocations
	knownLocations = map[string][]string{name: locs}
	t.Cleanup(func() { knownLocations = saved })
}

func TestFindPrefersPATH(t *testing.T) {
	onPath, bundled := t.TempDir(), t.TempDir()
	writeExe(t, filepath.Join(onPath, "harness"))
	writeExe(t, filepath.Join(bundled, "harness"))
	withLocations(t, "harness", filepath.Join(bundled, "harness"))
	t.Setenv("PATH", onPath)

	got, err := Find("harness")
	if err != nil || got != filepath.Join(onPath, "harness") {
		t.Errorf("Find = %q, %v; want the PATH copy", got, err)
	}
}

func TestFindFallsBackToKnownLocationUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	want := filepath.Join(home, "Applications", "Harness.app", "bin", "harness")
	writeExe(t, want)
	withLocations(t, "harness", "/nonexistent/harness", "~/Applications/Harness.app/bin/harness")

	got, err := Find("harness")
	if err != nil || got != want {
		t.Errorf("Find = %q, %v; want %q", got, err, want)
	}
}

func TestFindSkipsNonExecutable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	plain := filepath.Join(dir, "harness")
	if err := os.WriteFile(plain, []byte("not a program"), 0o644); err != nil {
		t.Fatal(err)
	}
	withLocations(t, "harness", plain, dir)

	if got, err := Find("harness"); err == nil {
		t.Errorf("Find = %q; want an error for a non-executable file and a directory", got)
	}
}
