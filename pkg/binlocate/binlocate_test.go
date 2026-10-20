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

// agentDirFixture puts an executable "harness" both on PATH and in a known
// location, so a test can prove $MADS_AGENT_BIN_DIR reaches neither.
func agentDirFixture(t *testing.T) (onPath, known string) {
	t.Helper()
	pathDir := t.TempDir()
	onPath = filepath.Join(pathDir, "harness")
	known = filepath.Join(t.TempDir(), "Harness.app", "bin", "harness")
	writeExe(t, onPath)
	writeExe(t, known)
	withLocations(t, "harness", known)
	t.Setenv("PATH", pathDir)
	return onPath, known
}

func TestFindWithAgentBinDirIgnoresPATHAndKnownLocations(t *testing.T) {
	agentDirFixture(t)
	dir := t.TempDir()
	t.Setenv(EnvAgentBinDir, dir)

	if got, err := Find("harness"); err == nil {
		t.Errorf("Find = %q; want not-found: the knob must hide PATH and known locations", got)
	}
	if got := Resolve("harness"); got != filepath.Join(dir, "harness") {
		t.Errorf("Resolve = %q; want the missing %q, never a PATH or bare-name answer", got, filepath.Join(dir, "harness"))
	}
}

func TestFindWithAgentBinDirFindsStandIn(t *testing.T) {
	agentDirFixture(t)
	dir := t.TempDir()
	want := filepath.Join(dir, "harness")
	writeExe(t, want)
	t.Setenv(EnvAgentBinDir, dir)

	if got, err := Find("harness"); err != nil || got != want {
		t.Errorf("Find = %q, %v; want the stand-in %q", got, err, want)
	}
	if got := Resolve("harness"); got != want {
		t.Errorf("Resolve = %q; want %q", got, want)
	}
}

func TestAgentBinDirSkipsNonExecutableStandIn(t *testing.T) {
	agentDirFixture(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "harness"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvAgentBinDir, dir)

	if got, err := Find("harness"); err == nil {
		t.Errorf("Find = %q; want not-found for a non-executable stand-in", got)
	}
}

func TestAgentBinDirLeavesNonAgentNamesAlone(t *testing.T) {
	pathDir := t.TempDir()
	want := filepath.Join(pathDir, "mergiraf")
	writeExe(t, want)
	t.Setenv("PATH", pathDir)
	t.Setenv(EnvAgentBinDir, t.TempDir())

	if got, err := Find("mergiraf"); err != nil || got != want {
		t.Errorf("Find(mergiraf) = %q, %v; want the PATH copy — mergiraf has its own $MADS_MERGIRAF", got, err)
	}
}

func TestAgentBinDirUnsetBehavesAsBefore(t *testing.T) {
	onPath, known := agentDirFixture(t)
	t.Setenv(EnvAgentBinDir, "")

	if got, err := Find("harness"); err != nil || got != onPath {
		t.Errorf("Find = %q, %v; want the PATH copy %q", got, err, onPath)
	}
	t.Setenv("PATH", t.TempDir())
	if got := Resolve("harness"); got != known {
		t.Errorf("Resolve = %q; want the known location %q", got, known)
	}
	withLocations(t, "harness")
	if got := Resolve("harness"); got != "harness" {
		t.Errorf("Resolve = %q; want the bare name when nothing resolves", got)
	}
}
