//go:build linux

package childsock

import (
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// sunPathLen is sizeof(sockaddr_un.sun_path) on Linux.
const sunPathLen = 108

func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if serr == nil {
			uid = int(cred.Uid)
		}
	}); err != nil {
		return 0, err
	}
	return uid, serr
}

func ownerUID(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
