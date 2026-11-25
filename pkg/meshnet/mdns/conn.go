package mdns

import (
	"fmt"
	"net"

	"golang.org/x/net/ipv4"
)

// Listen is a real mDNS socket on one interface: joined to 224.0.0.251 on
// it, sending there with TTL 255 (RFC 6762 §11). Only the Mesh Service
// during a setup window, and the Router while Add devices is open, use
// one.
func Listen(iface string) (Conn, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, fmt.Errorf("%w %q: %v", ErrNoInterface, iface, err)
	}
	uc, err := net.ListenMulticastUDP("udp4", ifi, Group)
	if err != nil {
		return nil, err
	}
	if err := bindToInterface(uc); err != nil {
		_ = uc.Close()
		return nil, err
	}
	pc := ipv4.NewPacketConn(uc)
	if err := pc.SetMulticastInterface(ifi); err != nil {
		_ = uc.Close()
		return nil, err
	}
	_ = pc.SetMulticastTTL(255)
	_ = pc.SetMulticastLoopback(true)
	return uc, nil
}
