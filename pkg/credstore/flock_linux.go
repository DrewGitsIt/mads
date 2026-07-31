//go:build linux

package credstore

import (
	"os"
	"syscall"
)

// acquireFlock takes a blocking exclusive flock(2) on path, creating the
// file if needed. It exists so that router and session — each
// independently calling Open on the same credentials.json (see package
// doc) — serialize the first-open migration instead of racing to mint two
// different data keys or double-encrypt entries. The returned func closes
// the file, which releases the lock (safe to call from a different
// goroutine than the one that acquired it, since flock's lock lives on the
// open file description, not the fd number).
func acquireFlock(path string) (release func() error, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f.Close, nil
}
