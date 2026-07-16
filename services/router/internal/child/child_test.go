package child

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/drewkidwell/mads/services/router/internal/testleakcheck"
)

var testChildBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mads-router-child-test")
	if err != nil {
		panic(err)
	}

	testChildBin = filepath.Join(dir, "testchild")
	build := exec.Command("go", "build", "-o", testChildBin,
		"github.com/drewkidwell/mads/services/router/internal/testchild")
	if out, err := build.CombinedOutput(); err != nil {
		panic("build testchild: " + err.Error() + "\n" + string(out))
	}

	code := m.Run()

	if err := testleakcheck.Verify(testChildBin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestSpawnDiscoversAddrAndTerminate(t *testing.T) {
	type exitResult struct {
		expected bool
		err      error
	}
	exitCh := make(chan exitResult, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	p, err := Spawn(ctx, "test1", []string{testChildBin}, func(_ *Process, expected bool, err error) {
		exitCh <- exitResult{expected, err}
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = p.Terminate(context.Background(), 2*time.Second) })
	if p.Addr == "" {
		t.Fatal("Spawn: no address discovered")
	}

	conn, err := net.DialTimeout("tcp", p.Addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial discovered addr %s: %v", p.Addr, err)
	}
	_ = conn.Close()

	if err := p.Terminate(context.Background(), 2*time.Second); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	select {
	case res := <-exitCh:
		if !res.expected {
			t.Errorf("onExit: expected=false after Terminate, want true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onExit not called within timeout")
	}

	// Terminate is safe to call again after the process has already exited.
	if err := p.Terminate(context.Background(), time.Second); err != nil {
		t.Errorf("second Terminate: %v", err)
	}
}

func TestExternalKillIsUnexpected(t *testing.T) {
	type exitResult struct {
		expected bool
		err      error
	}
	exitCh := make(chan exitResult, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	p, err := Spawn(ctx, "test2", []string{testChildBin}, func(_ *Process, expected bool, err error) {
		exitCh <- exitResult{expected, err}
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = p.Terminate(context.Background(), 2*time.Second) })

	proc, err := os.FindProcess(p.Cmd.Process.Pid)
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("external kill: %v", err)
	}

	select {
	case res := <-exitCh:
		if res.expected {
			t.Error("onExit: expected=true after an external kill, want false")
		}
		if res.err == nil {
			t.Error("onExit: exitErr = nil after an external kill, want non-nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onExit not called within timeout")
	}
}

func TestSpawnEmptyArgv(t *testing.T) {
	if _, err := Spawn(context.Background(), "empty", nil, nil); err == nil {
		t.Fatal("Spawn with empty argv: want error, got nil")
	}
}

// TestTerminateKillsGrandchildProcessGroup proves the fix for the leak
// go run wrapper. A wrapper script backgrounds testChildBin as a distinct
// grandchild pid (not exec, which would just replace the wrapper's own
// image) and writes that pid to a file; Terminate must reach it via the
// shared process group, not just the wrapper's own pid.
func TestTerminateKillsGrandchildProcessGroup(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "wrapper.sh")
	pidfilePath := filepath.Join(dir, "grandchild.pid")

	script := "#!/bin/sh\n" +
		"pidfile=\"$1\"; shift\n" +
		"\"$@\" &\n" +
		"echo $! > \"$pidfile\"\n" +
		"wait\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatalf("write wrapper script: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	argv := []string{"sh", scriptPath, pidfilePath, testChildBin}
	p, err := Spawn(ctx, "wrapper", argv, nil)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = p.Terminate(context.Background(), 2*time.Second) })
	if p.Addr == "" {
		t.Fatal("Spawn: no address discovered")
	}

	var grandchildPID int
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(pidfilePath)
		if err == nil && len(data) > 0 {
			if _, scanErr := fmt.Sscanf(string(data), "%d", &grandchildPID); scanErr == nil && grandchildPID > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pidfile %s never populated: %v", pidfilePath, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := syscall.Kill(grandchildPID, 0); err != nil {
		t.Fatalf("grandchild pid %d not alive before Terminate: %v", grandchildPID, err)
	}

	if err := p.Terminate(context.Background(), time.Second); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	deadline = time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(grandchildPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid %d still alive after Terminate", grandchildPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
