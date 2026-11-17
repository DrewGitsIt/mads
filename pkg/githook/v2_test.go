package githook

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// hookedRepo is a repo with the v2 hook installed and configured to report
// to data as sessionID, reading the token from tokenFile.
func hookedRepo(t *testing.T, dataURL, sessionID, tokenFile string) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main", ".")
	if err := EnsureInstalled(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if err := SetWorktreeConfig(context.Background(), dir, Config{SessionID: sessionID, DataURL: dataURL, TokenFile: tokenFile}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// commit commits in dir and returns git's combined output, failing the test
// if the commit fails: the hook must never break a commit.
func commit(t *testing.T, dir, msg string) string {
	t.Helper()
	cmd := exec.Command("git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", msg)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit failed: %v\n%s", err, out.String())
	}
	return out.String()
}

func guardedSession(t *testing.T) (datatest.Guarded, *dataclient.Client, string) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl on PATH (the hook depends on it)")
	}
	g := datatest.StartGuarded(t)
	data := dataclient.New(g.URL, "user", dataclient.WithToken(g.Token))
	sess := contracts.Session{Name: "hooked", Provider: "fake"}
	if err := data.CreateSession(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	return g, data, sess.ID
}

// TestHookV2ReportsToAGuardedDataService: a commit in a repo with the v2
// hook reaches a Data Service that refuses anything without the install
// token (docs/LOCAL-FRONT-DOOR-DESIGN.md D6).
func TestHookV2ReportsToAGuardedDataService(t *testing.T) {
	g, data, sessionID := guardedSession(t)
	dir := hookedRepo(t, g.URL, sessionID, g.TokenFile)
	if out := commit(t, dir, "reported"); out != "" {
		t.Fatalf("the hook printed during a commit:\n%s", out)
	}
	var commits []contracts.Commit
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		if commits, err = data.Commits(context.Background(), sessionID); err == nil && len(commits) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(commits) != 1 || commits[0].Message != "reported" {
		t.Fatalf("commits = %+v, want the one reported commit", commits)
	}
}

// TestHookV2StaysSilentWithoutAReadableToken: a missing, unreadable or
// unconfigured token file never breaks or clutters a commit, and nothing is
// reported (the guarded service would refuse it anyway).
func TestHookV2StaysSilentWithoutAReadableToken(t *testing.T) {
	g, data, sessionID := guardedSession(t)
	unreadable := filepath.Join(t.TempDir(), "local.token")
	if err := os.WriteFile(unreadable, []byte(g.Token+"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	for name, tokenFile := range map[string]string{
		"unreadable":   unreadable,
		"missing":      filepath.Join(t.TempDir(), "nope"),
		"unconfigured": "",
	} {
		dir := hookedRepo(t, g.URL, sessionID, tokenFile)
		if out := commit(t, dir, name); out != "" {
			t.Errorf("%s: the hook printed during a commit:\n%s", name, out)
		}
	}
	// A v1-era repo pointed at the token itself is no better than a bad path.
	dir := hookedRepo(t, g.URL, sessionID, g.Token)
	commit(t, dir, "token as path")
	time.Sleep(300 * time.Millisecond)
	commits, err := data.Commits(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 0 {
		t.Fatalf("reported %d commit(s) without a readable token", len(commits))
	}
}

// TestHookV2KeepsTheTokenOutOfArgv: the token reaches curl on stdin, never
// in an argv another user could read with ps.
func TestHookV2KeepsTheTokenOutOfArgv(t *testing.T) {
	bin := t.TempDir()
	logDir := t.TempDir()
	fakeCurl := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + logDir + "/argv\ncat > " + logDir + "/stdin\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(fakeCurl), 0o755); err != nil { //nolint:gosec // an executable stand-in
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tokenFile := filepath.Join(t.TempDir(), "local.token")
	const tok = "TOKENVALUE-TOKENVALUE-TOKENVALUE-TOKENVALUE"
	if err := os.WriteFile(tokenFile, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := hookedRepo(t, "http://127.0.0.1:1", "ses_x", tokenFile)
	commit(t, dir, "argv check")
	argv, err := os.ReadFile(filepath.Join(logDir, "argv")) //nolint:gosec // test-written file
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := os.ReadFile(filepath.Join(logDir, "stdin")) //nolint:gosec // test-written file
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argv), tok) {
		t.Fatalf("the token is in curl's argv:\n%s", argv)
	}
	if !strings.Contains(string(argv), "-K\n-\n") {
		t.Fatalf("curl isn't reading its config from stdin:\n%s", argv)
	}
	if string(stdin) != `header = "Authorization: Bearer `+tok+`"`+"\n" {
		t.Fatalf("curl's stdin = %q", stdin)
	}
}

// TestEnsureInstalledUpgradesAV1Hook: the v1 hook mads used to install is
// replaced, never left behind as "foreign".
func TestEnsureInstalledUpgradesAV1Hook(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main", ".")
	hook := filepath.Join(dir, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n# installed by mads — reports commits with task context (spec §5)\ncurl ...\n"), 0o755); err != nil { //nolint:gosec // a hook is executable
		t.Fatal(err)
	}
	if err := EnsureInstalled(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(hook) //nolint:gosec // test path
	if string(raw) != Script {
		t.Fatal("the v1 hook wasn't replaced with v2")
	}
}

// TestHookV2SendsTheTokenOnlyToLoopback (round 2): a mads.dataUrl that
// isn't this machine — or hides another host behind userinfo — gets
// nothing; loopback spellings do.
func TestHookV2SendsTheTokenOnlyToLoopback(t *testing.T) {
	bin := t.TempDir()
	logDir := t.TempDir()
	fakeCurl := "#!/bin/sh\nfor a in \"$@\"; do last=\"$a\"; done\nprintf '%s\\n' \"$last\" >> " + logDir + "/urls\ncat > /dev/null\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(fakeCurl), 0o755); err != nil { //nolint:gosec // an executable stand-in
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tokenFile := filepath.Join(t.TempDir(), "local.token")
	if err := os.WriteFile(tokenFile, []byte("TOKENVALUE-TOKENVALUE-TOKENVALUE-TOKENVALUE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"http://localhost:17374":           true,
		"http://127.0.0.1:17374":           true,
		"http://[::1]:17374":               true,
		"http://localhost":                 true,
		"https://example.com":              false,
		"http://example.com:17374":         false,
		"http://localhost.evil.example":    false,
		"http://127.0.0.1:80@evil.example": false,
		"http://localhost:1@evil.example":  false,
		"http://10.0.0.5:17374":            false,
		"file:///etc/passwd":               false,
	}
	for url, sends := range cases {
		_ = os.Remove(filepath.Join(logDir, "urls"))
		dir := hookedRepo(t, url, "ses_x", tokenFile)
		commit(t, dir, "loopback check")
		raw, _ := os.ReadFile(filepath.Join(logDir, "urls")) //nolint:gosec // test-written
		if got := len(raw) > 0; got != sends {
			t.Errorf("mads.dataUrl %s: curl ran = %v, want %v (%q)", url, got, sends, raw)
		}
	}
}
