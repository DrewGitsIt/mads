//go:build linux

package mdns

import (
	"net"

	"golang.org/x/sys/unix"
)

// bindToInterface keeps a multicast socket to its own interface's group
// on Linux (IP_MULTICAST_ALL off): otherwise it also receives the group's
// traffic joined on other interfaces, and adverts from another network
// would read as this one's (round-1 review S7).
func bindToInterface(uc *net.UDPConn) error {
	rc, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MULTICAST_ALL, 0)
	}); err != nil {
		return err
	}
	return serr
}
