//go:build unix

package tailscale

import (
	"errors"
	"os"
	"syscall"
)

// fileOwner is path's owner, not following a symlink.
func fileOwner(path string) (int, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return -1, err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, errors.New("no owner")
	}
	return int(sys.Uid), nil
}
