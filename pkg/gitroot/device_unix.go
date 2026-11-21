//go:build unix

package gitroot

import (
	"os"
	"syscall"
)

// device is the filesystem info lives on.
func device(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true //nolint:unconvert // Dev is int32 on darwin, uint64 on linux
}
