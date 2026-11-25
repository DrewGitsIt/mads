package mdns

import (
	"net"
	"sync"
)

// MemNetwork is an in-memory multicast network: whatever one member
// writes, every other member reads, from the writer's address. For tests
// (and the test hooks): no real interface is touched.
type MemNetwork struct {
	mu      sync.Mutex
	members []*memMember
}

type memMember struct {
	n    *MemNetwork
	addr *net.UDPAddr
	in   chan memPacket
	once sync.Once
	done chan struct{}
}

type memPacket struct {
	b    []byte
	from net.Addr
}

// Join is a Conn on n at ip.
func (n *MemNetwork) Join(ip string) Conn {
	m := &memMember{n: n, addr: &net.UDPAddr{IP: net.ParseIP(ip), Port: 5353}, in: make(chan memPacket, 256), done: make(chan struct{})}
	n.mu.Lock()
	n.members = append(n.members, m)
	n.mu.Unlock()
	return m
}

func (m *memMember) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case p := <-m.in:
		return copy(b, p.b), p.from, nil
	case <-m.done:
		return 0, nil, net.ErrClosed
	}
}

func (m *memMember) WriteTo(b []byte, _ net.Addr) (int, error) {
	m.n.mu.Lock()
	defer m.n.mu.Unlock()
	for _, o := range m.n.members {
		if o == m {
			continue
		}
		select {
		case o.in <- memPacket{append([]byte(nil), b...), m.addr}:
		default:
		}
	}
	return len(b), nil
}

func (m *memMember) Close() error { m.once.Do(func() { close(m.done) }); return nil }
