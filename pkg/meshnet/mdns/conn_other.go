//go:build !linux

package mdns

import "net"

// bindToInterface is Linux's IP_MULTICAST_ALL; elsewhere a socket joined
// on one interface hears only that interface's group.
func bindToInterface(*net.UDPConn) error { return nil }
