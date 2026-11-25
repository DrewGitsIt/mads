package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeBinary points executablePath at a throwaway file for the duration
// of fn, restoring it afterward — cmdUninstall must never touch the real
// `go test` binary.
func withFakeBinary(t *testing.T, bin string) {
	t.Helper()
	if err := os.WriteFile(bin, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := executablePath
	executablePath = func() (string, error) { return bin, nil }
	t.Cleanup(func() { executablePath = prev })
}

// isolateShadowRoot points XDG_DATA_HOME into the test's temp dir and
// creates the shadow root there. Every uninstall test must call it: without
// it, a data-removing uninstall would delete the developer's real
// ~/.local/share/mads/shadow.
func isolateShadowRoot(t *testing.T, dir string) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))
	root := defaultShadowRoot()
	if !strings.HasPrefix(root, dir) {
		t.Fatalf("shadow root %q escaped the test dir %q", root, dir)
	}
	if err := os.MkdirAll(filepath.Join(root, "ses_x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestUninstallDeclineKeepsDataDir(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mads")
	dataDir := filepath.Join(dir, "data")
	withFakeBinary(t, bin)
	t.Setenv("MADS_DATA_DIR", dataDir)
	shadowRoot := isolateShadowRoot(t, dir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cmdUninstall(nil, strings.NewReader("n\n"), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Errorf("binary not removed: %v", err)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Errorf("data dir should survive a declined prompt: %v", err)
	}
	if _, err := os.Stat(shadowRoot); err != nil {
		t.Errorf("shadow root should survive a declined prompt: %v", err)
	}
	if !strings.Contains(out.String(), "data dir preserved") {
		t.Errorf("output missing preserved notice: %s", out.String())
	}
}

func TestUninstallConfirmRemovesDataDir(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mads")
	dataDir := filepath.Join(dir, "data")
	withFakeBinary(t, bin)
	t.Setenv("MADS_DATA_DIR", dataDir)
	shadowRoot := isolateShadowRoot(t, dir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cmdUninstall(nil, strings.NewReader("y\n"), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Errorf("binary not removed: %v", err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Errorf("data dir not removed after confirmation: %v", err)
	}
	if _, err := os.Stat(shadowRoot); !os.IsNotExist(err) {
		t.Errorf("shadow root not removed after confirmation: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(shadowRoot)); !os.IsNotExist(err) {
		t.Errorf("empty mads/ parent of the shadow root not removed: %v", err)
	}
}

func TestUninstallKeepDataFlagSkipsPrompt(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mads")
	dataDir := filepath.Join(dir, "data")
	withFakeBinary(t, bin)
	t.Setenv("MADS_DATA_DIR", dataDir)
	shadowRoot := isolateShadowRoot(t, dir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// No stdin available at all — --keep-data must never block on a read.
	var out bytes.Buffer
	if err := cmdUninstall([]string{"--keep-data"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Errorf("binary not removed: %v", err)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Errorf("--keep-data must preserve the data dir: %v", err)
	}
	if _, err := os.Stat(shadowRoot); err != nil {
		t.Errorf("--keep-data must preserve the shadow root: %v", err)
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Errorf("--keep-data must not prompt: %s", out.String())
	}
}

func TestUninstallYesFlagSkipsPrompt(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mads")
	dataDir := filepath.Join(dir, "data")
	withFakeBinary(t, bin)
	t.Setenv("MADS_DATA_DIR", dataDir)
	shadowRoot := isolateShadowRoot(t, dir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cmdUninstall([]string{"--yes"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Errorf("--yes should remove the data dir: %v", err)
	}
	if _, err := os.Stat(shadowRoot); !os.IsNotExist(err) {
		t.Errorf("--yes should remove the shadow root: %v", err)
	}
}

func TestUninstallPrintsExactPathsBeforeDeleting(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "mads")
	dataDir := filepath.Join(dir, "data")
	withFakeBinary(t, bin)
	t.Setenv("MADS_DATA_DIR", dataDir)
	shadowRoot := isolateShadowRoot(t, dir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cmdUninstall([]string{"--keep-data"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{bin, dataDir, shadowRoot} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing exact path %q:\n%s", want, out.String())
		}
	}
}
