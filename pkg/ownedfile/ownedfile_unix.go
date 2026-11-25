//go:build unix

package ownedfile

import (
	"fmt"
	"os"
	"syscall"
)

// Open opens path read-only if it is a regular file this process's user
// owns, and nothing else: a symlink at path is refused rather than followed
// (O_NOFOLLOW), and the open never blocks (O_NONBLOCK — a FIFO planted there
// would otherwise hang the caller until someone wrote to it; it is then
// refused by the type check). The checks run on the open descriptor, so a
// file swapped in after them changes nothing the caller reads.
func Open(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrRefused, path)
	}
	if !ownedByMe(info) {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s is owned by another user", ErrRefused, path)
	}
	return f, nil
}

// PrivateDir checks that dir is a real directory (not a symlink to one),
// owned by this process's user, that no other user can write to or list:
// the 0700 shape a per-user temp root must have before anything under it is
// trusted. Another local user who made the directory first could otherwise
// plant files the caller would read as its own.
func PrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrRefused, dir)
	}
	if !ownedByMe(info) {
		return fmt.Errorf("%w: %s is owned by another user", ErrRefused, dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: %s is open to other users (%v)", ErrRefused, dir, info.Mode().Perm())
	}
	return nil
}

func ownedByMe(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
