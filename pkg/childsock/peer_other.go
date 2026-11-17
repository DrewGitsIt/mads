//go:build !linux && !darwin

package childsock

import (
	"errors"
	"net"
	"os"
)

const sunPathLen = 104

// peerUID has no implementation here, so every connection is refused: an
// unverified peer is never let in.
func peerUID(c *net.UnixConn) (int, error) {
	return 0, errors.New("peer credentials are not supported on this OS")
}

func ownerUID(fi os.FileInfo) (int, bool) { return 0, false }
