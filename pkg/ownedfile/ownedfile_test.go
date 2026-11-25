//go:build unix

package ownedfile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpenRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 5)
	if _, err := f.ReadAt(buf, 0); err != nil || string(buf) != "hello" {
		t.Errorf("read %q, %v", buf, err)
	}
}

// A symlink is refused even when it points at a file the user owns: the
// path was the thing being vouched for, not wherever it leads.
func TestOpenRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if f, err := Open(link); err == nil {
		_ = f.Close()
		t.Fatal("Open followed a symlink")
	}
}

// A FIFO must neither hang the open nor be read.
func TestOpenRefusesFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := Open(path)
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRefused) {
			t.Errorf("Open(fifo) = %v, want ErrRefused", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open blocked on a FIFO")
	}
}

func TestOpenMissingIsNotExist(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrRefused) {
		t.Errorf("Open(missing) = %v, want ErrNotExist and not ErrRefused", err)
	}
}

func TestPrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(dir); err != nil {
		t.Errorf("PrivateDir(0700) = %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(dir); !errors.Is(err, ErrRefused) {
		t.Errorf("PrivateDir(0755) = %v, want ErrRefused", err)
	}
	link := dir + "-link"
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(link); !errors.Is(err, ErrRefused) {
		t.Errorf("PrivateDir(symlink) = %v, want ErrRefused", err)
	}
}
