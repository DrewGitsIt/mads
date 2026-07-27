package capability

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// TestEnsureStartedStopDoesNotLeakGoroutine guards against a regression
// where cmd.Stdin was backed by a custom io.Reader whose Read never
// returns: exec.Cmd copies such a Stdin to the child on an internal
// goroutine, and that goroutine's blocked Read call kept cmd.Wait() (and
// the reaper goroutine that calls it) from ever returning, even after
// Stop killed the child — a goroutine (and parent pipe FD) leak on every
// EnsureStarted/Stop cycle.
func TestEnsureStartedStopDoesNotLeakGoroutine(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir)
	cap := contracts.Capability{
		Name:   "leaktest",
		Config: contracts.CapabilityConfig{Command: "sleep 30"},
	}

	runtime.GC()
	before := runtime.NumGoroutine()

	if _, err := reg.EnsureStarted(context.Background(), cap, nil); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	if err := reg.Stop(context.Background(), cap.Name); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime.GC()
		if after := runtime.NumGoroutine(); after <= before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak after Stop: before=%d after=%d", before, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
