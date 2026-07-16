package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/update"
)

const defaultRouterURL = "http://localhost:8080"

// cmdUpdate dispatches `mads update check` / `mads update apply`.
func cmdUpdate(args []string, stdin io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("update: expected a subcommand (check, apply)")
	}
	switch args[0] {
	case "check":
		return cmdUpdateCheck(args[1:], out)
	case "apply":
		return cmdUpdateApply(args[1:], stdin, out)
	default:
		return fmt.Errorf("update: unknown subcommand %q (want check or apply)", args[0])
	}
}

type routerVersionResponse struct {
	Version string `json:"version"`
}

func routerGet(ctx context.Context, routerURL, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(routerURL, "/")+path, nil)
	if err != nil {
		return err
	}
	return routerDo(req, v)
}

func routerPost(ctx context.Context, routerURL, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(routerURL, "/")+path, nil)
	if err != nil {
		return err
	}
	return routerDo(req, v)
}

func routerDo(req *http.Request, v any) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("router %s returned %d", req.URL.Path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// cmdUpdateCheck talks to a running Router (spec §14.3's user-initiated
// check — this CLI subcommand is itself a user action, same as the
// Settings button). Checking is never a failure condition: it exits
// non-zero only on a genuine transport error talking to the Router itself,
// distinct from "feed unreachable," which the Router already turns into a
// Detail string, not an HTTP error.
func cmdUpdateCheck(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("update check", flag.ContinueOnError)
	fs.SetOutput(out)
	routerURL := fs.String("router", defaultRouterURL, "Router base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	var v routerVersionResponse
	if err := routerGet(ctx, *routerURL, "/version", &v); err != nil {
		return fmt.Errorf("update check: reach router: %w", err)
	}
	var result update.CheckResult
	if err := routerPost(ctx, *routerURL, "/update/check", &result); err != nil {
		return fmt.Errorf("update check: reach router: %w", err)
	}

	_, _ = fmt.Fprintf(out, "current version: %s\n", v.Version)
	switch {
	case result.Detail != "":
		_, _ = fmt.Fprintln(out, result.Detail)
	case result.UpdateAvailable:
		_, _ = fmt.Fprintf(out, "update available: %s\n", result.LatestVersion)
		if result.Notes != "" {
			_, _ = fmt.Fprintf(out, "\n%s\n", result.Notes)
		}
	default:
		_, _ = fmt.Fprintln(out, "up to date")
	}
	return nil
}

// cmdUpdateApply runs the full update lifecycle (spec §14.4) against the
// running `mads` binary: graceful Router drain (best-effort) -> download ->
// checksum verify -> swap -> restart -> health check -> automatic rollback
// on failure. -feed-url overrides the Router's configured feed URL for a
// one-off apply without needing Settings open (also what CI/the smoke test
// uses).
func cmdUpdateApply(args []string, stdin io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("update apply", flag.ContinueOnError)
	fs.SetOutput(out)
	feedURLFlag := fs.String("feed-url", "", "override the configured update feed URL for this apply")
	routerURL := fs.String("router", defaultRouterURL, "Router base URL")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	feedURL := *feedURLFlag
	if feedURL == "" {
		var cfg contracts.Config
		if err := routerGet(ctx, *routerURL, "/config", &cfg); err != nil {
			return fmt.Errorf("update apply: resolve feed URL: reach router: %w", err)
		}
		feedURL = cfg.UpdateFeedURL
	}
	if feedURL == "" {
		return fmt.Errorf("update apply: no update feed configured — pass -feed-url or set one in Settings")
	}

	manifest, err := (update.HTTPFeed{URL: feedURL}).Fetch(ctx)
	if err != nil {
		return fmt.Errorf("update apply: fetch feed: %w", err)
	}
	art, ok := manifest.Platforms[update.CurrentPlatform()]
	if !ok {
		return fmt.Errorf("update apply: feed has no artifact for platform %s", update.CurrentPlatform())
	}

	_, _ = fmt.Fprintf(out, "update available: %s\n", manifest.Version)
	if manifest.Notes != "" {
		_, _ = fmt.Fprintf(out, "\n%s\n\n", manifest.Notes)
	}
	if !*yes {
		_, _ = fmt.Fprint(out, "Apply this update now? [y/N]: ")
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(line), "y") {
			_, _ = fmt.Fprintln(out, "update cancelled")
			return nil
		}
	}

	bin, err := executablePath()
	if err != nil {
		return fmt.Errorf("update apply: locate running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}

	target := update.Target{BinaryPath: bin, StagingDir: os.TempDir()}
	hooks := routerHooks(defaultMadsHome(), bin, out)

	result, err := update.Apply(ctx, art, target, hooks)
	if err != nil {
		if result.RolledBack {
			_, _ = fmt.Fprintln(out, "update failed and was rolled back to the previous binary")
		}
		return fmt.Errorf("update apply: %w", err)
	}
	_, _ = fmt.Fprintf(out, "updated to %s\n", manifest.Version)
	return nil
}

// defaultMadsHome mirrors services/router/main.go's own resolution (same env
// var family) — intentional small duplication for a handful of lines, same
// precedent as this file's semver duplication in pkg/update.
func defaultMadsHome() string {
	if dir := os.Getenv("MADS_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mads"
	}
	return filepath.Join(home, ".mads")
}

func routerPIDPath(madsHome string) string {
	return filepath.Join(madsHome, "router.pid")
}

// routerHooks builds the real Stop/Start/HealthCheck hooks pkg/update.Apply
// orchestrates. Every step is best-effort: no running Router (the common
// case for a bare `mads` CLI install) or no sibling `router` binary
// degrades to "log guidance, continue" rather than failing the update —
// updating the CLI binary alone is still fully meaningful.
func routerHooks(madsHome, binPath string, out io.Writer) update.Hooks {
	pidPath := routerPIDPath(madsHome)
	var routerWasRunning bool

	stop := func(ctx context.Context) error {
		pid, ok := readAlivePID(pidPath)
		if !ok {
			_, _ = fmt.Fprintln(out, "no running router detected — nothing to drain")
			return nil
		}
		routerWasRunning = true
		proc, err := os.FindProcess(pid)
		if err != nil {
			return nil
		}
		// The exact signal services/router/main.go's own
		// signal.NotifyContext already handles via sup.Shutdown — this
		// triggers the real, existing graceful-drain path.
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			_, _ = fmt.Fprintf(out, "router: signal pid %d: %v\n", pid, err)
			return nil
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) && processAlive(pid) {
			time.Sleep(200 * time.Millisecond)
		}
		return nil
	}

	start := func(ctx context.Context) error {
		if !routerWasRunning {
			return nil
		}
		routerBin := filepath.Join(filepath.Dir(binPath), "router")
		info, statErr := os.Stat(routerBin)
		if statErr != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			_, _ = fmt.Fprintln(out, "router binary not found alongside mads — restart it manually")
			return nil
		}
		cmd := exec.Command(routerBin, "-mads-home", madsHome)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			_, _ = fmt.Fprintf(out, "router: relaunch failed: %v\n", err)
			return nil
		}
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
			_, _ = fmt.Fprintf(out, "router: write pid file: %v\n", err)
		}
		return nil
	}

	healthCheck := func(ctx context.Context) error {
		var lastErr error
		for i := 0; i < 5; i++ {
			if err := exec.CommandContext(ctx, binPath, "--version").Run(); err == nil {
				return nil
			} else {
				lastErr = err
			}
			time.Sleep(500 * time.Millisecond)
		}
		return lastErr
	}

	return update.Hooks{Stop: stop, Start: start, HealthCheck: healthCheck}
}

func readAlivePID(pidPath string) (int, bool) {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	if !processAlive(pid) {
		return 0, false
	}
	return pid, true
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
