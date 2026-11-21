//go:build !unix

package gitroot

import "os"

// device is unknown off unix: no filesystem-boundary check there.
func device(os.FileInfo) (uint64, bool) { return 0, false }
