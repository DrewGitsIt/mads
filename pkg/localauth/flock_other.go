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

// openNoFollow is a plain open off unix, where there's no O_NOFOLLOW.
func openNoFollow(path string) (*os.File, error) { return os.Open(path) } //nolint:gosec // a private file under $MADS_HOME
