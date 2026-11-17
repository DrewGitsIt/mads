//go:build unix

package localauth

import (
	"os"
	"syscall"
	"testing"
)

// foreignOwned returns fi's Stat_t with the owner changed to someone else.
func foreignOwned(t *testing.T, fi os.FileInfo) any {
	t.Helper()
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no Stat_t on this platform")
	}
	other := *st
	other.Uid = st.Uid + 1
	return &other
}
