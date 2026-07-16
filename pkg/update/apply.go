package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Target is the single binary being updated.
type Target struct {
	BinaryPath string // absolute path to the currently-installed executable
	StagingDir string // scratch directory for the download; caller's responsibility to clean up
}

// Hooks are the three system-specific actions Apply orchestrates but does
// not implement itself — this package has no idea what "the system" is (a
// bare CLI, a CLI plus a locally-running Router, a whole desktop app).
// Production callers (cmd/mads) wire real behavior; tests wire fakes/fake
// binaries. All three are required — pass no-op funcs explicitly rather than
// leaving Hooks zero-valued, so a missing wire-up is a visible bug, not a
// silent no-op.
type Hooks struct {
	Stop        func(ctx context.Context) error
	Start       func(ctx context.Context) error
	HealthCheck func(ctx context.Context) error
}

// Result reports what Apply actually did.
type Result struct {
	RolledBack bool
	Version    string
}

// Apply runs the full lifecycle: stop -> download -> checksum -> swap ->
// restart -> health-check -> (rollback on failure). It never leaves the
// system in a half-swapped state: every failure path after the backup is
// taken restores the backup before returning.
func Apply(ctx context.Context, art PlatformArtifact, target Target, hooks Hooks) (Result, error) {
	if err := hooks.Stop(ctx); err != nil {
		return Result{}, fmt.Errorf("update: stop: %w", err)
	}

	staged := filepath.Join(target.StagingDir, "update.download")
	if err := downloadTo(ctx, art.URL, staged); err != nil {
		_ = hooks.Start(ctx) // best-effort: put the still-untouched original back in service
		return Result{}, fmt.Errorf("update: download: %w", err)
	}
	if err := verifyChecksum(staged, art.SHA256); err != nil {
		_ = os.Remove(staged)
		_ = hooks.Start(ctx)
		return Result{}, fmt.Errorf("update: checksum: %w", err)
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		_ = hooks.Start(ctx)
		return Result{}, fmt.Errorf("update: chmod staged binary: %w", err)
	}

	backup := target.BinaryPath + ".bak"
	if err := os.Rename(target.BinaryPath, backup); err != nil {
		_ = hooks.Start(ctx)
		return Result{}, fmt.Errorf("update: backup current binary: %w", err)
	}
	if err := os.Rename(staged, target.BinaryPath); err != nil {
		// Backup is intact, current binary path is empty — restore before giving up.
		_ = os.Rename(backup, target.BinaryPath)
		_ = hooks.Start(ctx)
		return Result{}, fmt.Errorf("update: install new binary: %w", err)
	}

	if err := hooks.Start(ctx); err != nil {
		return rollback(ctx, target, backup, hooks, fmt.Errorf("start: %w", err))
	}
	if err := hooks.HealthCheck(ctx); err != nil {
		return rollback(ctx, target, backup, hooks, fmt.Errorf("health check: %w", err))
	}

	_ = os.Remove(backup) // success: no side-by-side versions (spec requirement 6)
	return Result{RolledBack: false, Version: art.URL}, nil
}

// rollback restores backup over the (broken) new binary and restarts the old
// one. Every step is best-effort past this point — this is the failure
// path's failure path, and Apply's own error (not a secondary error here) is
// what the caller needs to see.
func rollback(ctx context.Context, target Target, backup string, hooks Hooks, cause error) (Result, error) {
	_ = hooks.Stop(ctx)
	_ = os.Rename(backup, target.BinaryPath)
	_ = hooks.Start(ctx)
	return Result{RolledBack: true}, fmt.Errorf("update: %w — rolled back to previous binary", cause)
}

func downloadTo(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s returned %d", url, resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}

func verifyChecksum(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
	}
	return nil
}
