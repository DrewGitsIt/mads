//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// instanceLock is held for the process's life: closing the file (or the
// GC finalizing it) would release the lock.
var instanceLock *os.File

// lockInstance makes this the one Mesh Service for madsHome: an exclusive
// flock(2) on <madsHome>/run/mesh.lock, which then holds this pid (what
// scripts/app.sh reads to reuse or stop it; the Mesh Service has no local
// port to find it by, and may listen nowhere). A second one is refused
// with the holder's pid.
func lockInstance(madsHome string) error {
	dir := filepath.Join(madsHome, "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "mesh.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		b, _ := os.ReadFile(path)
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
				return fmt.Errorf("another Mesh Service is running for %s (pid %d)", madsHome, pid)
			}
			return fmt.Errorf("another Mesh Service is running for %s (%s is locked)", madsHome, path)
		}
		return err
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		_ = f.Close()
		return err
	}
	instanceLock = f
	return nil
}
