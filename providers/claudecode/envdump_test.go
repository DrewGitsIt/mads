package claudecode

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/drewkidwell/mads/pkg/agentenv"
)

// buildEnvdump compiles the pkg/capability/testdata/envdump helper — the
// same test-only binary services/session/internal/runner/capability_test.go
// uses — once per test run, so these tests can prove what a real spawned
// child's OS environment actually contains, not just what env() returns as
// a []string.
var (
	envdumpBinOnce sync.Once
	envdumpBinPath string
)

func buildEnvdump(t *testing.T) string {
	t.Helper()
	envdumpBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mads-claudecode-envdump")
		if err != nil {
			panic(err)
		}
		envdumpBinPath = filepath.Join(dir, "envdump")
		cmd := exec.Command("go", "build", "-o", envdumpBinPath,
			"github.com/drewkidwell/mads/pkg/capability/testdata/envdump")
		if out, err := cmd.CombinedOutput(); err != nil {
			panic("build envdump: " + err.Error() + "\n" + string(out))
		}
	})
	return envdumpBinPath
}

// dumpEnv runs envdumpBin as the spawn target using p's own env() helper —
// the exact method OneShot/oneShotStream/startStream/startPTY all call — so
// this exercises the real spawn-site wiring, not a reimplementation of it.
func dumpEnv(t *testing.T, p *Provider, extra []string) map[string]string {
	t.Helper()
	dumpPath := filepath.Join(t.TempDir(), "dump.json")
	cmd := exec.Command(p.cfg.Bin, "-out", dumpPath)
	cmd.Env = p.env(extra)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run envdump: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	var dump map[string]string
	if err := json.Unmarshal(raw, &dump); err != nil {
		t.Fatalf("unmarshal dump: %v", err)
	}
	return dump
}

func TestSpawnedChildEnv_IsolatedExcludesSecretButIncludesPassthroughAndCredentials(t *testing.T) {
	envdumpBin := buildEnvdump(t)
	t.Setenv("MADS_TEST_SECRET", "leak-me")
	t.Setenv("MADS_TEST_AWS_PROFILE", "work")

	p := New(Config{
		Bin:            envdumpBin,
		EnvMode:        agentenv.ModeIsolated,
		EnvPassthrough: []string{"MADS_TEST_AWS_PROFILE"},
		Env:            []string{"MADS_TEST_CRED=bound-cred-value"},
	})

	dump := dumpEnv(t, p, []string{"MADS_TEST_EXTRA=per-call-value"})

	if _, leaked := dump["MADS_TEST_SECRET"]; leaked {
		t.Fatalf("isolated mode leaked a planted secret global into the spawned agent: %+v", dump)
	}
	if dump["MADS_TEST_AWS_PROFILE"] != "work" {
		t.Fatalf("isolated mode dropped an explicit passthrough var: %+v", dump)
	}
	if dump["MADS_TEST_CRED"] != "bound-cred-value" {
		t.Fatalf("isolated mode dropped a bound credential: %+v", dump)
	}
	if dump["MADS_TEST_EXTRA"] != "per-call-value" {
		t.Fatalf("isolated mode dropped a per-call extra value: %+v", dump)
	}
}

func TestSpawnedChildEnv_InheritPassesThroughEverything(t *testing.T) {
	envdumpBin := buildEnvdump(t)
	t.Setenv("MADS_TEST_SECRET", "fine-in-inherit-mode")

	p := New(Config{Bin: envdumpBin}) // EnvMode zero value == inherit

	dump := dumpEnv(t, p, nil)

	if dump["MADS_TEST_SECRET"] != "fine-in-inherit-mode" {
		t.Fatalf("inherit mode should pass through every parent env var: %+v", dump)
	}
}
