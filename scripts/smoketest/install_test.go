// Package smoketest drives the installer end-to-end (spec §14.7, W7.2):
// scripts/install.sh installs a real mads binary into a temp prefix, a real
// Data Service is started against an isolated $HOME to exercise first-run
// data-dir creation, then `mads uninstall` is driven through its three
// outcomes (declined, confirmed, --keep-data). Nothing here touches the
// machine it runs on outside t.TempDir().
package smoketest

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

var (
	madsBin   string
	dataBin   string
	installSh string
)

// platformMatrix is the installer's target platform list (spec §14.10).
// This machine only has a linux runner; darwin coverage is CI-infrastructure
// this environment doesn't have — see scripts/README.md's pending-CI note.
var platformMatrix = []string{"linux", "darwin"}

func TestMain(m *testing.M) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("smoketest: runtime.Caller failed")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile))) // scripts/smoketest/ -> repo root
	installSh = filepath.Join(root, "scripts", "install.sh")

	// A release-gate run (scripts/release-gate.sh) already built a candidate
	// and wants this suite to test *that* binary, not a fresh self-build.
	// Plain `go test ./scripts/smoketest/...` (unchanged, still part of
	// `make check`/CI) leaves this unset and falls back to building its own.
	if candidateDir := os.Getenv("MADS_RELEASE_CANDIDATE_DIR"); candidateDir != "" {
		madsBin = filepath.Join(candidateDir, "mads")
		dataBin = filepath.Join(candidateDir, "data")
		if _, err := os.Stat(madsBin); err != nil {
			panic(fmt.Sprintf("MADS_RELEASE_CANDIDATE_DIR=%s: missing mads binary: %v", candidateDir, err))
		}
		if _, err := os.Stat(dataBin); err != nil {
			panic(fmt.Sprintf("MADS_RELEASE_CANDIDATE_DIR=%s: missing data binary: %v", candidateDir, err))
		}
		os.Exit(m.Run())
	}

	dir, err := os.MkdirTemp("", "mads-install-smoke")
	if err != nil {
		panic(err)
	}
	madsBin = filepath.Join(dir, "mads")
	dataBin = filepath.Join(dir, "data")

	for _, b := range []struct{ out, pkg string }{
		{madsBin, "github.com/drewkidwell/mads/cmd/mads"},
		{dataBin, "github.com/drewkidwell/mads/services/data"},
	} {
		build := exec.Command("go", "build", "-o", b.out, b.pkg)
		build.Dir = root
		if out, err := build.CombinedOutput(); err != nil {
			panic(fmt.Sprintf("build %s: %v\n%s", b.pkg, err, out))
		}
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// requireLocalRunner skips the test on a platform not in platformMatrix, and
// (today) on every platform except linux — this environment has no darwin
// runner yet.
func requireLocalRunner(t *testing.T) {
	t.Helper()
	inMatrix := false
	for _, p := range platformMatrix {
		if p == runtime.GOOS {
			inMatrix = true
		}
	}
	if !inMatrix {
		t.Skipf("GOOS=%s is not in the installer's platform matrix %v", runtime.GOOS, platformMatrix)
	}
	if runtime.GOOS != "linux" {
		t.Skipf("pending CI: no %s runner available here (see scripts/README.md)", runtime.GOOS)
	}
}

// cleanEnv builds a process environment rooted at an isolated HOME, with no
// leaked MADS_DATA_DIR from the outer test process's own environment.
func cleanEnv(home string) []string {
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "MADS_DATA_DIR=") {
			continue
		}
		env = append(env, e)
	}
	return append(env, "HOME="+home)
}

func runInstall(t *testing.T, sourceDir, prefix, home string) {
	t.Helper()
	cmd := exec.Command("sh", installSh)
	cmd.Env = append(cleanEnv(home), "MADS_INSTALL_SOURCE="+sourceDir, "MADS_INSTALL_PREFIX="+prefix)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
}

func runMads(t *testing.T, madsPath, home string, args ...string) string {
	t.Helper()
	out, err := runMadsStdin(t, madsPath, home, "", args...)
	if err != nil {
		t.Fatalf("mads %v: %v\n%s", args, err, out)
	}
	return out
}

func runMadsStdin(t *testing.T, madsPath, home, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(madsPath, args...)
	cmd.Env = cleanEnv(home)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// startDataService runs the built Data Service against home's default data
// dir ($HOME/.mads, since MADS_DATA_DIR is stripped) and returns its base
// URL once it has confirmed the listen address, plus a stop func.
func startDataService(t *testing.T, home string) (string, func()) {
	t.Helper()
	cmd := exec.Command(dataBin, "-addr", "127.0.0.1:0")
	cmd.Env = cleanEnv(home)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	addrCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			const marker = "listening on "
			if idx := strings.Index(line, marker); idx >= 0 {
				rest := strings.Fields(line[idx+len(marker):])
				if len(rest) > 0 {
					addrCh <- rest[0]
				}
			}
		}
	}()

	stop := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	select {
	case addr := <-addrCh:
		return "http://" + addr, stop
	case <-time.After(5 * time.Second):
		stop()
		t.Fatal("data service did not report a listen address in time")
		return "", stop // unreachable, satisfies the compiler
	}
}

// freshInstall runs install.sh into a fresh temp prefix/HOME, checks `mads
// --version` runs, and starts the Data Service once to prove first-run data
// dir + default config creation (spec §14.7 item 2) — the gap check this
// work item's scope calls for is already covered by store.Open (dir
// creation) and migration 0010 (default config seed); this proves that
// holds end-to-end from the installed binary's own $HOME.
func freshInstall(t *testing.T) (madsPath, home string) {
	t.Helper()
	prefix := t.TempDir()
	home = t.TempDir()
	runInstall(t, filepath.Dir(madsBin), prefix, home)

	madsPath = filepath.Join(prefix, "mads")
	info, err := os.Stat(madsPath)
	if err != nil {
		t.Fatalf("installed binary missing: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("installed binary not executable: %v", info.Mode())
	}

	versionOut := runMads(t, madsPath, home, "--version")
	if !strings.Contains(versionOut, "mads ") {
		t.Fatalf("mads --version: %q", versionOut)
	}

	dataDir := filepath.Join(home, ".mads")
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("data dir must not pre-exist before first run: %v", err)
	}
	base, stop := startDataService(t, home)
	defer stop()
	cfg, err := dataclient.New(base, "smoketest").Config(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, contracts.DefaultConfig()) {
		t.Fatalf("first-run config = %+v, want DefaultConfig()", cfg)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "mads.db")); err != nil {
		t.Fatalf("data dir not created on first run: %v", err)
	}
	return madsPath, home
}

func TestInstallFirstRunUninstallDeclined(t *testing.T) {
	requireLocalRunner(t)
	madsPath, home := freshInstall(t)

	out, err := runMadsStdin(t, madsPath, home, "n\n", "uninstall")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[y/N]") {
		t.Errorf("uninstall must ask before deleting data, got:\n%s", out)
	}
	if _, err := os.Stat(madsPath); !os.IsNotExist(err) {
		t.Errorf("binary should be removed regardless of the data-dir answer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".mads")); err != nil {
		t.Errorf("declining confirmation should keep the data dir: %v", err)
	}
}

func TestInstallFirstRunUninstallConfirmed(t *testing.T) {
	requireLocalRunner(t)
	madsPath, home := freshInstall(t)

	out, err := runMadsStdin(t, madsPath, home, "y\n", "uninstall")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if _, err := os.Stat(madsPath); !os.IsNotExist(err) {
		t.Errorf("binary not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".mads")); !os.IsNotExist(err) {
		t.Errorf("confirmed uninstall should remove the data dir: %v", err)
	}
}

func TestInstallFirstRunUninstallKeepData(t *testing.T) {
	requireLocalRunner(t)
	madsPath, home := freshInstall(t)

	out, err := runMadsStdin(t, madsPath, home, "", "uninstall", "--keep-data")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if strings.Contains(out, "[y/N]") {
		t.Errorf("--keep-data must not prompt: %s", out)
	}
	if _, err := os.Stat(madsPath); !os.IsNotExist(err) {
		t.Errorf("binary not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".mads")); err != nil {
		t.Errorf("--keep-data should preserve the data dir: %v", err)
	}
}
