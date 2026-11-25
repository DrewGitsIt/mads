package childsock

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindRefusesAFallbackDirPathWouldRefuse (adversarial, D9; fixed in
// round 1): the
// /tmp fallback dir's name is predictable (/tmp/mads-<uid>-<hash of the
// MADS_HOME path>), and for an install whose primary socket path fits, mads
// never creates it — so another OS user can create it first, as a
// directory they own or a symlink to one. Path() refuses such a dir
// (ensurePrivateDir), but Find(), which `mads api --session ID` uses,
// checks only that <dir>/<id>.sock is a socket: a session that isn't
// running in $MADS_HOME/run (dormant, or a typo'd id) is "found" in the
// planted dir, and the user's request body (a message to an agent, say)
// goes to the other user's listener, which can answer anything.
//
// The other-owner case needs a second uid; the symlink case, which
// ensurePrivateDir refuses for the same reason, shows the gap.
// Find now applies the checks Path does (a real dir, this user's, 0700).
func TestFindRefusesAFallbackDirPathWouldRefuse(t *testing.T) {
	oldTmp := tmpRoot
	t.Cleanup(func() { tmpRoot = oldTmp })
	tmpRoot = shortDir(t)
	home := shortDir(t) // short: the primary path fits, so no fallback is made

	planted := shortDir(t) // "the other user's" directory
	if err := os.Chmod(planted, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(planted, fallbackDir(home)); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(planted, "ses_dormant.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	if _, err := ensurePrivateDirCheck(fallbackDir(home)); err == nil {
		t.Fatal("precondition: Path's own check should refuse the planted dir")
	}
	if p, ok := Find(home, "ses_dormant"); ok {
		t.Errorf("Find returned a socket in a planted fallback dir Path would refuse: %s", p)
	}
}

// ensurePrivateDirCheck runs Path's directory check without creating
// anything new (the dir exists here).
func ensurePrivateDirCheck(dir string) (string, error) {
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// TestAdversarialListenRefusesToReplaceNonSockets: a regular file or a
// symlink at the socket path is never unlinked or followed.
func TestAdversarialListenRefusesToReplaceNonSockets(t *testing.T) {
	dir := shortDir(t)
	file := filepath.Join(dir, "f.sock")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(file); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Errorf("Listen over a regular file: %v", err)
	}
	link := filepath.Join(dir, "l.sock")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(link); err == nil {
		t.Error("Listen over a symlink succeeded")
	}
	if b, _ := os.ReadFile(file); string(b) != "keep" { //nolint:gosec // test file
		t.Error("the file behind the path was touched")
	}
	// A live socket isn't stolen.
	live := filepath.Join(dir, "live.sock")
	ln, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if _, err := Listen(live); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("Listen over a live socket: %v", err)
	}
}
