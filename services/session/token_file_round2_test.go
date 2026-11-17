package main

import (
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

// runSessionChild builds this package and runs it against a guarded Data
// Service with tokenFile, returning its combined output once it exits (or
// after a few seconds, killed).
func runSessionChild(t *testing.T, g datatest.Guarded, sessionID, tokenFile string) (string, bool) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "session")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home, err := os.MkdirTemp("", "fd2s") // short: the socket path must fit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-session-id", sessionID, "-data-url", g.URL, //nolint:gosec // test-built binary
		"-mads-home", home, "-socket", filepath.Join(home, "s.sock"), "-cwd", home, "-token-file", tokenFile)
	cmd.Env = append(os.Environ(), "MADS_HOME="+home, "HOME="+home, "MADS_AGENT_BIN_DIR="+home)
	out, err := cmd.CombinedOutput()
	return string(out), err != nil && ctx.Err() == nil
}

// TestRound2WrongTokenFileFailsAndNamesIt (adversarial round 2, H4): a
// child handed a -token-file it can't use exits (it can't even load its
// session: every Data call is 401) instead of running half-alive, and its
// output names the file it couldn't read.
func TestRound2WrongTokenFileFailsAndNamesIt(t *testing.T) {
	g := datatest.StartGuarded(t)
	data := dataclient.New(g.URL, "test", dataclient.WithToken(g.Token))
	sess := contracts.Session{Name: "tf", Provider: "fake", Isolation: contracts.IsolationCurrentDir}
	if err := data.CreateSession(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "no-such", "local.token")
	out, exited := runSessionChild(t, g, sess.ID, bad)
	if !exited {
		t.Fatalf("a child with an unusable -token-file kept running:\n%s", out)
	}
	if !strings.Contains(out, bad) || !strings.Contains(out, "session: start") {
		t.Fatalf("the child's output doesn't name the token file %s:\n%s", bad, out)
	}
	// Same for a token file the mode check refuses.
	loose := filepath.Join(t.TempDir(), "local.token")
	if err := os.WriteFile(loose, []byte(g.Token+"\n"), 0o644); err != nil { //nolint:gosec // deliberately loose
		t.Fatal(err)
	}
	out, exited = runSessionChild(t, g, sess.ID, loose)
	if !exited || !strings.Contains(out, "readable or writable by other users") {
		t.Fatalf("a group/world-readable token file (exited=%v):\n%s", exited, out)
	}
}

// TestRound2GoodTokenFileStartsTheChild is the control for the above: the
// same child with the real token file starts and announces its socket.
func TestRound2GoodTokenFileStartsTheChild(t *testing.T) {
	g := datatest.StartGuarded(t)
	data := dataclient.New(g.URL, "test", dataclient.WithToken(g.Token))
	sess := contracts.Session{Name: "tf", Provider: "fake", Isolation: contracts.IsolationCurrentDir}
	if err := data.CreateSession(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	out, exited := runSessionChild(t, g, sess.ID, g.TokenFile)
	if exited || !strings.Contains(out, "listening on") {
		t.Fatalf("child with the right token file (exited=%v):\n%s", exited, out)
	}
}
