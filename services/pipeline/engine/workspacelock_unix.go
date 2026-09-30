//go:build unix

package engine

import (
	"os"
	"syscall"
)

// acquireWorkspaceFlock takes a blocking exclusive flock(2) on path,
// creating the file if needed. The returned func closes the file, which
// releases the lock (and is safe to call from a different goroutine than
// the one that acquired it, unlike unlocking via the same fd from another
// process — flock's lock lives on the open file description, not the fd
// number).
func acquireWorkspaceFlock(path string) (release func() error, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f.Close, nil
}
