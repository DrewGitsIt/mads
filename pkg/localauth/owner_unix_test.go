//go:build unix

package localauth

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// foreignOwned returns fi's Stat_t with the owner changed to someone else.
func foreignOwned(t *testing.T, fi os.FileInfo) any {
	t.Helper()
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no Stat_t on this platform")
	}
	other := *st
	other.Uid = st.Uid + 1
	return &other
}

// The token loader shares OpenPrivate with the device key: a FIFO at
// local.token can't hang a service's start, and a symlink isn't followed.
func TestTokenRefusesFIFOAndSymlink(t *testing.T) {
	home := t.TempDir()
	if err := syscall.Mkfifo(Path(home), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := LoadOrCreate(home); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("LoadOrCreate over a FIFO = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LoadOrCreate blocked on a FIFO")
	}

	real := t.TempDir()
	if _, err := LoadOrCreate(real); err != nil {
		t.Fatal(err)
	}
	linked := t.TempDir()
	if err := os.Symlink(Path(real), Path(linked)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadToken(linked); err == nil || errors.Is(err, ErrNoToken) || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ReadToken through a symlink = %v", err)
	}
}
