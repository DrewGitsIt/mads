package acprpc

import (
	"os"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// The fake agent IS the spawned child, so a "getenv NAME" prompt reports
// what its real OS environment contains — the same spawn-site proof as
// kirocli's envdump tests, without a second helper binary.

func askEnv(t *testing.T, sess provider.Session, name string) string {
	t.Helper()
	return turnText(sendTurn(t, sess, "getenv "+name))
}

func TestEnvRemoveStripsFromBaseOnly(t *testing.T) {
	t.Setenv("MADS_TEST_ACP_REMOVED", "secret")
	t.Setenv("MADS_TEST_ACP_KEPT", "kept-value")
	t.Setenv("MADS_TEST_ACP_ADDBACK", "from-parent")

	cfg := gooseConfig()
	cfg.EnvRemove = []string{"MADS_TEST_ACP_REMOVED", "MADS_TEST_ACP_ADDBACK"}
	// Explicit additions win over EnvRemove — it strips the base only.
	cfg.Env = []string{"MADS_TEST_ACP_ADDBACK=re-added"}
	e := testEngine(t, cfg, "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})

	if got := askEnv(t, sess, "MADS_TEST_ACP_REMOVED"); got != "env MADS_TEST_ACP_REMOVED unset" {
		t.Fatalf("removed var reached the child: %q", got)
	}
	if got := askEnv(t, sess, "MADS_TEST_ACP_KEPT"); got != "env MADS_TEST_ACP_KEPT=kept-value" {
		t.Fatalf("non-removed var was lost: %q", got)
	}
	if got := askEnv(t, sess, "MADS_TEST_ACP_ADDBACK"); got != "env MADS_TEST_ACP_ADDBACK=re-added" {
		t.Fatalf("explicit addition did not win over EnvRemove: %q", got)
	}
}

func TestBaseEnvSeamReplacesInheritedEnvironment(t *testing.T) {
	t.Setenv("MADS_TEST_ACP_PARENT_ONLY", "should-not-leak")

	cfg := gooseConfig()
	// A non-nil BaseEnv replaces os.Environ() entirely — the binding's
	// agentenv.Build seam. PATH rides along so the re-exec'd binary works.
	cfg.BaseEnv = []string{"PATH=" + pathValue(t), "MADS_TEST_ACP_BASE=base-value"}
	e := testEngine(t, cfg, "goose", t.TempDir())
	sess := startSession(t, e, provider.StartOptions{})

	if got := askEnv(t, sess, "MADS_TEST_ACP_PARENT_ONLY"); got != "env MADS_TEST_ACP_PARENT_ONLY unset" {
		t.Fatalf("parent env leaked past an explicit BaseEnv: %q", got)
	}
	if got := askEnv(t, sess, "MADS_TEST_ACP_BASE"); got != "env MADS_TEST_ACP_BASE=base-value" {
		t.Fatalf("BaseEnv entry missing: %q", got)
	}
}

func pathValue(t *testing.T) string {
	t.Helper()
	v, ok := os.LookupEnv("PATH")
	if !ok {
		t.Skip("no PATH in test environment")
	}
	return v
}
