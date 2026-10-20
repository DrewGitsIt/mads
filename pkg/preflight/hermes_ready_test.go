package preflight

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A hermes-acp whose home has no dependency install is reported not
// installed without being run: running it would start hermes's ~600 MB
// bootstrap. The stand-in checkout's launcher records any run.
func TestCheckProviderBinary_HermesWithoutInstallIsNotProbed(t *testing.T) {
	dir := t.TempDir()
	checkout := filepath.Join(dir, "hermes-agent")
	for _, d := range []string{".git", ".hermes/bin"} {
		if err := os.MkdirAll(filepath.Join(checkout, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(checkout, "pyproject.toml"), []byte("[project]\n"), 0o644)
	_ = os.WriteFile(filepath.Join(checkout, "install-stamp.json"), []byte(`{"updateMechanism":"self"}`), 0o644)
	ran := filepath.Join(dir, "ran")
	launcher := filepath.Join(checkout, ".hermes", "bin", "hermes-acp")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\ntouch "+ran+"\necho 0.22.2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "path")
	_ = os.MkdirAll(bin, 0o755)
	if err := os.WriteFile(filepath.Join(bin, "hermes-acp"), []byte("#!/bin/sh\nexec "+launcher+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERMES_HOME", "")

	check := checkProviderBinary(context.Background(), "hermes", knownProviderBinaries["hermes"])
	if check.Healthy || !check.NotInstalled || !strings.Contains(check.Detail, "no dependency install") {
		t.Errorf("check = %+v, want not installed, naming the missing install", check)
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Error("the launcher was run")
	}
}

// A version probe that hangs is killed with everything it started: the
// probe runs in a group of its own and the timeout kills the group.
func TestRunVersionTimeoutKillsTheGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child")
	script := filepath.Join(dir, "probe")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 300 &\necho $! > "+pidFile+"\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Cancel only once the probe has started its child: a fixed timeout
	// could fire before the shell wrote the pid under a loaded full-suite
	// run, leaving nothing to check.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probeErr := make(chan error, 1)
	go func() {
		_, err := runVersion(ctx, script)
		probeErr <- err
	}()
	var b []byte
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if raw, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(raw)) != "" {
			b = raw
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the probe never started its child")
		}
	}
	cancel()
	if err := <-probeErr; err == nil {
		t.Fatal("hung probe returned no error")
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(3 * time.Second)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("the probe's child %d survived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
