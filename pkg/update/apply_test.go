package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const goodBinaryScript = "#!/bin/sh\nexit 0\n"
const brokenBinaryScript = "#!/bin/sh\nexit 1\n"

// writeExecutable writes contents to path with executable permissions.
func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func serveBinary(t *testing.T, contents string) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(contents))
	}))
	t.Cleanup(srv.Close)
	return srv, sha256Hex(t, []byte(contents))
}

func runBinaryHealthCheck(binPath string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		return exec.CommandContext(ctx, binPath).Run()
	}
}

func TestApply_HealthySwap(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "current")
	writeExecutable(t, binPath, "#!/bin/sh\necho placeholder\n")

	srv, sum := serveBinary(t, goodBinaryScript)
	art := PlatformArtifact{URL: srv.URL, SHA256: sum}
	target := Target{BinaryPath: binPath, StagingDir: dir}

	var calls []string
	hooks := Hooks{
		Stop:  func(ctx context.Context) error { calls = append(calls, "stop"); return nil },
		Start: func(ctx context.Context) error { calls = append(calls, "start"); return nil },
		HealthCheck: func(ctx context.Context) error {
			calls = append(calls, "health")
			return runBinaryHealthCheck(binPath)(ctx)
		},
	}

	result, err := Apply(context.Background(), art, target, hooks)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.RolledBack {
		t.Fatal("expected RolledBack false on a healthy swap")
	}

	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != goodBinaryScript {
		t.Fatalf("binary at target path was not replaced with the new binary's bytes")
	}
	if _, err := os.Stat(binPath + ".bak"); !os.IsNotExist(err) {
		t.Fatal("expected no .bak file left behind after a successful swap")
	}

	wantOrder := []string{"stop", "start", "health"}
	if len(calls) != len(wantOrder) {
		t.Fatalf("calls = %v, want %v", calls, wantOrder)
	}
	for i, c := range wantOrder {
		if calls[i] != c {
			t.Fatalf("calls = %v, want order %v", calls, wantOrder)
		}
	}
}

func TestApply_ForcedFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "current")
	original := "#!/bin/sh\necho placeholder\n"
	writeExecutable(t, binPath, original)

	srv, sum := serveBinary(t, brokenBinaryScript)
	art := PlatformArtifact{URL: srv.URL, SHA256: sum}
	target := Target{BinaryPath: binPath, StagingDir: dir}

	stopCalls, startCalls := 0, 0
	hooks := Hooks{
		Stop:        func(ctx context.Context) error { stopCalls++; return nil },
		Start:       func(ctx context.Context) error { startCalls++; return nil },
		HealthCheck: func(ctx context.Context) error { return runBinaryHealthCheck(binPath)(ctx) },
	}

	result, err := Apply(context.Background(), art, target, hooks)
	if err == nil {
		t.Fatal("expected an error from a forced health-check failure")
	}
	if !result.RolledBack {
		t.Fatal("expected RolledBack true after a forced health-check failure")
	}

	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("binary at target path is not byte-identical to the original after rollback:\ngot:  %q\nwant: %q", got, original)
	}
	if _, err := os.Stat(binPath + ".bak"); !os.IsNotExist(err) {
		t.Fatal("expected no .bak file left behind after rollback")
	}
	if stopCalls != 2 {
		t.Fatalf("Stop called %d times, want 2 (once before swap, once inside rollback)", stopCalls)
	}
	if startCalls != 2 {
		t.Fatalf("Start called %d times, want 2 (once after swap, once inside rollback)", startCalls)
	}
}

func TestApply_ChecksumMismatchNeverSwaps(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "current")
	original := "#!/bin/sh\necho placeholder\n"
	writeExecutable(t, binPath, original)

	srv, _ := serveBinary(t, goodBinaryScript)
	art := PlatformArtifact{URL: srv.URL, SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}
	target := Target{BinaryPath: binPath, StagingDir: dir}

	startCalls := 0
	hooks := Hooks{
		Stop:  func(ctx context.Context) error { return nil },
		Start: func(ctx context.Context) error { startCalls++; return nil },
		HealthCheck: func(ctx context.Context) error {
			t.Fatal("HealthCheck should never run on checksum mismatch")
			return nil
		},
	}

	_, err := Apply(context.Background(), art, target, hooks)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}

	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatal("binary at target path should be untouched on checksum mismatch")
	}
	if startCalls != 1 {
		t.Fatalf("Start called %d times, want 1 (best-effort put-back)", startCalls)
	}
}

func TestApply_DataDirUntouched(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "current")
	writeExecutable(t, binPath, "#!/bin/sh\necho placeholder\n")

	dataFile := filepath.Join(dir, "mads.db")
	dataContents := []byte("pretend sqlite bytes, sessions and tasks live here")
	if err := os.WriteFile(dataFile, dataContents, 0o644); err != nil {
		t.Fatal(err)
	}
	before := sha256Hex(t, dataContents)

	srv, sum := serveBinary(t, brokenBinaryScript)
	art := PlatformArtifact{URL: srv.URL, SHA256: sum}
	target := Target{BinaryPath: binPath, StagingDir: dir}

	hooks := Hooks{
		Stop:        func(ctx context.Context) error { return nil },
		Start:       func(ctx context.Context) error { return nil },
		HealthCheck: func(ctx context.Context) error { return runBinaryHealthCheck(binPath)(ctx) },
	}

	if _, err := Apply(context.Background(), art, target, hooks); err == nil {
		t.Fatal("expected forced-failure error")
	}

	after, err := os.ReadFile(dataFile)
	if err != nil {
		t.Fatal(err)
	}
	if sha256Hex(t, after) != before {
		t.Fatal("data file bytes changed across a forced-failure rollback cycle — Apply must never touch anything outside target.BinaryPath/target.StagingDir")
	}
}
