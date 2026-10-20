package hermescli

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

	"github.com/drewkidwell/mads/pkg/hermesinstall"
	"github.com/drewkidwell/mads/providers/provider"
)

// bootstrappingLauncher lays out a stand-in hermes install whose launcher
// behaves like the real one when the home has no dependency install: it
// creates $HOME/.hermes/installs and leaves a detached helper running (the
// `uv sync` tree that outlived hermes-acp). If mads ever ran it, the
// helper's pid lands in pidFile and the directory appears.
func bootstrappingLauncher(t *testing.T) (wrapper, pidFile string) {
	t.Helper()
	dir := t.TempDir()
	checkout := filepath.Join(dir, "hermes-agent")
	for _, d := range []string{".git", ".hermes/bin"} {
		if err := os.MkdirAll(filepath.Join(checkout, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"pyproject.toml":     "[project]\n",
		"install-stamp.json": `{"updateMechanism":"self"}`,
	} {
		if err := os.WriteFile(filepath.Join(checkout, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pidFile = filepath.Join(dir, "bootstrap.pid")
	launcher := filepath.Join(checkout, ".hermes", "bin", "hermes-acp")
	script := "#!/bin/sh\n" +
		"mkdir -p \"$HOME/.hermes/installs\"\n" +
		"nohup sleep 300 >/dev/null 2>&1 &\n" +
		"echo $! > " + pidFile + "\n" +
		"exec sleep 300\n"
	if err := os.WriteFile(launcher, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper = filepath.Join(dir, "local-bin", "hermes-acp")
	if err := os.MkdirAll(filepath.Dir(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+launcher+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return wrapper, pidFile
}

// The regression the integration run found: mads spawning hermes-acp under
// a HOME with no hermes install started a ~600 MB bootstrap that outlived
// mads. Every spawn site refuses first — nothing runs, nothing is left,
// no directory appears.
func TestNoSpawnWithoutAnInstall(t *testing.T) {
	wrapper, pidFile := bootstrappingLauncher(t)
	home := t.TempDir()
	p := New(Config{Bin: wrapper, Env: []string{"HOME=" + home}})
	ctx := context.Background()

	_, err := p.Start(ctx, provider.StartOptions{Mode: provider.ModeACP, Cwd: t.TempDir()})
	if !errors.Is(err, hermesinstall.ErrNotInstalled) {
		t.Errorf("Start = %v, want ErrNotInstalled", err)
	}
	_, err = p.OneShot(ctx, provider.OneShotRequest{Prompt: "hi", Cwd: t.TempDir()})
	if !errors.Is(err, hermesinstall.ErrNotInstalled) {
		t.Errorf("OneShot = %v, want ErrNotInstalled", err)
	}
	_, err = p.Models(ctx)
	if !errors.Is(err, hermesinstall.ErrNotInstalled) || !errors.Is(err, provider.ErrDiscoveryUnavailable) {
		t.Errorf("Models = %v, want ErrNotInstalled and ErrDiscoveryUnavailable", err)
	}

	time.Sleep(200 * time.Millisecond) // a spawned launcher would have written by now
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Errorf("the launcher ran (pid file: %v)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".hermes")); !os.IsNotExist(err) {
		t.Errorf("$HOME/.hermes was created (%v)", err)
	}
}

// helperPids starts the stand-in's "detach" turn shape and returns the two
// helpers it left: one in a session of its own, one in hermes-acp's group.
func helperPids(t *testing.T, pidFile string) []int {
	t.Helper()
	var pids []int
	deadline := time.Now().Add(10 * time.Second)
	for len(pids) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("helpers never started")
		}
		if b, err := os.ReadFile(pidFile); err == nil {
			pids = nil
			for _, f := range strings.Fields(string(b)) {
				if pid, err := strconv.Atoi(f); err == nil {
					pids = append(pids, pid)
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return pids
}

func waitGone(t *testing.T, pids []int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for {
			if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("helper %d survived", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func treeProvider(t *testing.T, pidFile string) *Provider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Bin: exe, Home: t.TempDir(), Env: []string{"MADS_FAKE_HERMES=1", "MADS_FAKE_HERMES_PIDS=" + pidFile}})
}

// Close kills hermes-acp's whole tree: its group, and a helper that left
// the group for a session of its own.
func TestCloseKillsDetachedHelpers(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "helpers")
	sess := startSession(t, treeProvider(t, pidFile), provider.StartOptions{})
	sendTurn(t, sess, "detach")
	pids := helperPids(t, pidFile)
	if g, _ := syscall.Getpgid(pids[0]); g == pids[0] {
		// the detached one leads its own group, as hermes's tools do
	} else {
		t.Fatalf("detached helper %d is in group %d, not its own", pids[0], g)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pids)
}

// A cancelled one-shot (ctx done mid-turn) ends in the same tree kill.
func TestCancelledOneShotKillsDetachedHelpers(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "helpers")
	p := treeProvider(t, pidFile)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.OneShot(ctx, provider.OneShotRequest{Prompt: "detach-slow", Cwd: t.TempDir()})
		done <- err
	}()
	pids := helperPids(t, pidFile)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("OneShot = %v, want context.Canceled", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("OneShot did not return after cancel")
	}
	waitGone(t, pids)
}

// The agent killed outright (its pid only): the helpers it left were
// remembered when the tool started, and Close still reaps them.
func TestCrashedAgentHelpersReapedOnClose(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "helpers")
	sess := startSession(t, treeProvider(t, pidFile), provider.StartOptions{})
	sendTurn(t, sess, "detach")
	pids := helperPids(t, pidFile)
	time.Sleep(600 * time.Millisecond) // the reaper's post-tool snapshot (300 ms)
	agent, err := findAgentPid(pids[1])
	if err != nil {
		t.Fatal(err)
	}
	// Without a group of its own the "agent" found here would be this test
	// binary's own group: never signal that.
	if agent <= 1 || agent == syscall.Getpgrp() {
		t.Fatalf("hermes-acp is not in a process group of its own (pgid %d)", agent)
	}
	_ = syscall.Kill(agent, syscall.SIGKILL)
	for range sess.Events() { // drains until the read loop closes it
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pids)
}

// findAgentPid is the stand-in hermes-acp: the grouped helper's group
// leader (the agent was started with Setpgid).
func findAgentPid(groupedHelper int) (int, error) {
	return syscall.Getpgid(groupedHelper)
}
