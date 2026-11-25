//go:build unix

package devicekey

import (
	"errors"
	"os"
	"path/filepath"
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

// A FIFO at device.key must neither hang startup (the Router, the Mesh
// Service, `mads device`) nor be read: it's refused as not a regular file.
func TestLoadRefusesAFIFOWithoutBlocking(t *testing.T) {
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
}

// A symlink at device.key is never followed, even to a valid key this user
// owns; a dangling one is refused too, not taken for "no key yet" (which
// would create one through the link).
func TestLoadRefusesSymlinks(t *testing.T) {
	real := t.TempDir()
	if _, err := LoadOrCreate(real); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"to a key": Path(real), "dangling": filepath.Join(real, "nope")} {
		home := t.TempDir()
		if err := os.Symlink(target, Path(home)); err != nil {
			t.Fatal(err)
		}
		for fn, load := range map[string]func() error{
			"Load":         func() error { _, err := Load(home); return err },
			"LoadOrCreate": func() error { _, err := LoadOrCreate(home); return err },
		} {
			err := load()
			if err == nil || errors.Is(err, ErrNoKey) || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("%s %s: %v, want a symlink refusal", name, fn, err)
			}
		}
		if _, err := os.Lstat(filepath.Join(real, "nope")); !os.IsNotExist(err) {
			t.Fatalf("%s: a key was created through the link", name)
		}
	}
}
