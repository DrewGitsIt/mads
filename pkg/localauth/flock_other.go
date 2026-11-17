//go:build !unix

package localauth

import "os"

// acquireFlock is a no-op off unix (Windows is a non-goal); creation is
// still exclusive through os.Link.
func acquireFlock(path string) (release func() error, err error) {
	return func() error { return nil }, nil
}

// ownerUID has no portable answer off unix.
func ownerUID(fi os.FileInfo) (int, bool) { return 0, false }
