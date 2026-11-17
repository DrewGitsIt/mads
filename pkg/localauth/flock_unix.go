//go:build unix

package localauth

import (
	"os"
	"syscall"
)

// acquireFlock takes a blocking exclusive flock(2) on path, creating it if
// needed — the same helper pkg/credstore uses for its first-open migration,
// here serializing token creation and rotation across processes.
func acquireFlock(path string) (release func() error, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is $MADS_HOME/local.token.lock
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f.Close, nil
}

// ownerUID is the uid owning fi's file.
func ownerUID(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
