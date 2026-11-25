// Package mdns is the LAN half of discovery (docs/MESH-NETWORK-DESIGN.md
// U4): a DNS-SD advert of _mads._tcp over multicast DNS (RFC 6762/6763),
// run only while this device's setup window is open, and the browse that
// finds other devices' adverts. Only mads devices advertise _mads._tcp,
// so cameras, printers and TVs never appear.
//
// It's a deliberately small implementation (one service type, IPv4
// multicast, no probing or conflict resolution) on the Go team's
// golang.org/x/net/dns/dnsmessage parser: an advert lasts minutes, and
// nothing it learns is trusted. A found device still has to complete the
// pinned TLS handshake, show the same check code on both screens and be
// approved on the host.
//
// On macOS mDNSResponder owns port 5353 too; Go's multicast listener sets
// SO_REUSEADDR and SO_REUSEPORT, so both receive queries and either may
// answer. On Linux the same holds beside avahi-daemon. Where the network
// drops multicast (many campus and hotel networks), nothing is found and
// the pairing code remains.
//
// Packets arrive from anyone on the network: every one is parsed with
// dnsmessage, bounded in size and count, and dropped on any error.
package mdns

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Service is mads's DNS-SD service type.
const Service = "_mads._tcp.local."

// Group is mDNS's IPv4 multicast group and port.
var Group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// maxPacket bounds a packet read or written.
const maxPacket = 9000

// ttl is the advert's records' time to live (short: the window is minutes).
const ttl = 120

// Conn is the multicast socket an advert or a browse uses: real on an
// interface (Listen), an in-memory network in tests.
type Conn interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, addr net.Addr) (int, error)
	Close() error
}

// Advert is what a device announces: its instance name, port, TXT and
// the addresses on this network.
type Advert struct {
	// Instance is the device's name ("pop-os").
	Instance string
	// Host is the A record's name, without ".local." (default: the
	// instance plus "-mads").
	Host  string
	Port  int
	Addrs []netip.Addr
	// TXT is the key=value pairs: v=1, n=<name>, p=<platform>,
	// fp=<short fingerprint>.
	TXT map[string]string
}

func (a Advert) instanceName() string { return escapeLabel(a.Instance) + "." + Service }

func (a Advert) hostName() string {
	h := a.Host
	if h == "" {
		h = a.Instance + "-mads"
	}
	return hostLabel(h) + ".local."
}

// escapeLabel makes an instance name a single DNS label: dots and
// backslashes escaped, at most 63 bytes.
func escapeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if r == '.' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
		if b.Len() >= 60 {
			break
		}
	}
	if b.Len() == 0 {
		return "mads"
	}
	return b.String()
}

func unescapeLabel(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\.`, "."), `\\`, `\`)
}

// hostLabel makes s a host name label: letters, digits and '-'.
func hostLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 60 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "mads"
	}
	return out
}

func mustName(s string) dnsmessage.Name {
	n, err := dnsmessage.NewName(s)
	if err != nil {
		return dnsmessage.MustNewName("mads.local.")
	}
	return n
}

// response builds the advert's records (ttl 0: a goodbye).
func (a Advert) response(ttl uint32) ([]byte, error) {
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{Response: true, Authoritative: true})
	b.EnableCompression()
	if err := b.StartAnswers(); err != nil {
		return nil, err
	}
	inst, host, svc := mustName(a.instanceName()), mustName(a.hostName()), mustName(Service)
	shared := dnsmessage.ResourceHeader{Name: svc, Class: dnsmessage.ClassINET, TTL: ttl}
	if err := b.PTRResource(shared, dnsmessage.PTRResource{PTR: inst}); err != nil {
		return nil, err
	}
	// Records only this device owns carry the cache-flush bit.
	unique := dnsmessage.Class(dnsmessage.ClassINET | 1<<15)
	if err := b.SRVResource(dnsmessage.ResourceHeader{Name: inst, Class: unique, TTL: ttl}, dnsmessage.SRVResource{Target: host, Port: uint16(a.Port)}); err != nil {
		return nil, err
	}
	var txt []string
	for _, k := range []string{"v", "n", "p", "fp"} {
		if v, ok := a.TXT[k]; ok {
			txt = append(txt, clipTXT(k+"="+v))
		}
	}
	if len(txt) == 0 {
		txt = []string{""}
	}
	if err := b.TXTResource(dnsmessage.ResourceHeader{Name: inst, Class: unique, TTL: ttl}, dnsmessage.TXTResource{TXT: txt}); err != nil {
		return nil, err
	}
	for _, ip := range a.Addrs {
		if ip.Is4() {
			if err := b.AResource(dnsmessage.ResourceHeader{Name: host, Class: unique, TTL: ttl}, dnsmessage.AResource{A: ip.As4()}); err != nil {
				return nil, err
			}
		}
	}
	return b.Finish()
}

func clipTXT(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// query is a PTR question for the service.
func query() ([]byte, error) {
	b := dnsmessage.NewBuilder(make([]byte, 0, 64), dnsmessage.Header{})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: mustName(Service), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return b.Finish()
}

// asksForUs reports whether a query packet asks for the service, the
// instance, or its host.
func asksForUs(pkt []byte, a Advert) bool {
	var p dnsmessage.Parser
	h, err := p.Start(pkt)
	if err != nil || h.Response {
		return false
	}
	qs, err := p.AllQuestions()
	if err != nil || len(qs) > 32 {
		return false
	}
	inst, host := strings.ToLower(a.instanceName()), strings.ToLower(a.hostName())
	for _, q := range qs {
		name := strings.ToLower(q.Name.String())
		switch {
		case name == Service && (q.Type == dnsmessage.TypePTR || q.Type == dnsmessage.TypeALL):
			return true
		case name == "_services._dns-sd._udp.local." && q.Type == dnsmessage.TypePTR:
			return true
		case name == inst || name == host:
			return true
		}
	}
	return false
}

// Advertise answers queries for a on c, announcing it at start (twice, a
// second apart) and saying goodbye (TTL 0) when ctx ends. Answers to one
// question are at most one a second (RFC 6762 §6). It closes c.
func Advertise(ctx context.Context, c Conn, a Advert) error {
	resp, err := a.response(ttl)
	if err != nil {
		return err
	}
	bye, _ := a.response(0)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		if bye != nil {
			_, _ = c.WriteTo(bye, Group)
		}
		_ = c.Close()
		close(done)
	}()
	_, _ = c.WriteTo(resp, Group)
	go func() {
		select {
		case <-time.After(time.Second):
			_, _ = c.WriteTo(resp, Group)
		case <-done:
		}
	}()
	buf := make([]byte, maxPacket)
	var last time.Time
	for {
		n, _, err := c.ReadFrom(buf)
		if err != nil {
			wg.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !asksForUs(buf[:n], a) || time.Since(last) < time.Second {
			continue
		}
		last = time.Now()
		_, _ = c.WriteTo(resp, Group)
	}
}

// Entry is one device a browse found.
type Entry struct {
	Instance string
	Host     string
	Port     int
	// Addrs are its A records, else the address its answer came from.
	Addrs []netip.Addr
	// From is the address the completing answer came from.
	From netip.Addr
	TXT  map[string]string
}

// SelfAddrs is e's addresses that are the sender's own: an advert may only
// point at the device that sent it, so a stranger's advert can't make a
// browser dial some other machine on the network.
func (e Entry) SelfAddrs() []netip.Addr {
	if !e.From.IsValid() {
		return nil
	}
	for _, a := range e.Addrs {
		if a == e.From {
			return []netip.Addr{a}
		}
	}
	return nil
}

// maxEntries bounds what one browse keeps: a flood of fake adverts can't
// grow it without end.
const maxEntries = 64

// Browse asks for _mads._tcp on c (at once, after a second, then every
// few seconds) and sends each complete entry on the channel it returns,
// once per instance and port, until ctx ends. It closes c.
func Browse(ctx context.Context, c Conn) (<-chan Entry, error) {
	q, err := query()
	if err != nil {
		return nil, err
	}
	out := make(chan Entry, 16)
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()
	go func() {
		for _, d := range []time.Duration{0, time.Second, 3 * time.Second, 5 * time.Second} {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
			_, _ = c.WriteTo(q, Group)
		}
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = c.WriteTo(q, Group)
			}
		}
	}()
	go func() {
		defer close(out)
		st := &browseState{entries: map[string]*Entry{}, hosts: map[string][]netip.Addr{}, sent: map[string]bool{}}
		buf := make([]byte, maxPacket)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			var src netip.Addr
			if ua, ok := from.(*net.UDPAddr); ok {
				src, _ = netip.AddrFromSlice(ua.IP)
				src = src.Unmap()
			}
			for _, e := range st.read(buf[:n], src) {
				select {
				case out <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

type browseState struct {
	entries map[string]*Entry // by instance name, lower case
	hosts   map[string][]netip.Addr
	sent    map[string]bool
}

// read folds one response into the state and returns entries newly
// complete.
func (st *browseState) read(pkt []byte, src netip.Addr) []Entry {
	var p dnsmessage.Parser
	h, err := p.Start(pkt)
	if err != nil || !h.Response {
		return nil
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil
	}
	touched := map[string]bool{}
	for i := 0; i < 64; i++ {
		rh, err := p.AnswerHeader()
		if err != nil {
			break
		}
		name := strings.ToLower(rh.Name.String())
		switch rh.Type {
		case dnsmessage.TypePTR:
			r, err := p.PTRResource()
			if err != nil || name != Service || rh.TTL == 0 {
				continue
			}
			inst := strings.ToLower(r.PTR.String())
			st.entry(inst)
			touched[inst] = true
		case dnsmessage.TypeSRV:
			r, err := p.SRVResource()
			if err != nil || !strings.HasSuffix(name, "."+Service) || rh.TTL == 0 {
				continue
			}
			if e := st.entry(name); e != nil {
				e.Host, e.Port = strings.ToLower(r.Target.String()), int(r.Port)
				touched[name] = true
			}
		case dnsmessage.TypeTXT:
			r, err := p.TXTResource()
			if err != nil || !strings.HasSuffix(name, "."+Service) {
				continue
			}
			if e := st.entry(name); e != nil {
				e.TXT = map[string]string{}
				for _, kv := range r.TXT {
					if k, v, ok := strings.Cut(kv, "="); ok && len(e.TXT) < 16 {
						e.TXT[strings.ToLower(k)] = v
					}
				}
				touched[name] = true
			}
		case dnsmessage.TypeA:
			r, err := p.AResource()
			if err != nil {
				continue
			}
			if len(st.hosts) < maxEntries || st.hosts[name] != nil {
				ip := netip.AddrFrom4(r.A)
				if len(st.hosts[name]) < 4 {
					st.hosts[name] = append(st.hosts[name], ip)
				}
			}
			for k, e := range st.entries {
				if e.Host == name {
					touched[k] = true
				}
			}
		default:
			if err := p.SkipAnswer(); err != nil {
				return nil
			}
		}
	}
	var out []Entry
	for k := range touched {
		e := st.entries[k]
		if e == nil || e.Port == 0 || e.TXT == nil {
			continue
		}
		addrs := st.hosts[e.Host]
		if len(addrs) == 0 && src.IsValid() {
			addrs = []netip.Addr{src}
		}
		if len(addrs) == 0 {
			continue
		}
		key := k + " " + strconv.Itoa(e.Port)
		if st.sent[key] {
			continue
		}
		st.sent[key] = true
		ready := *e
		ready.Addrs = append([]netip.Addr(nil), addrs...)
		ready.From = src
		out = append(out, ready)
	}
	return out
}

func (st *browseState) entry(fullName string) *Entry {
	if e, ok := st.entries[fullName]; ok {
		return e
	}
	if len(st.entries) >= maxEntries {
		return nil
	}
	inst := unescapeLabel(strings.TrimSuffix(fullName, "."+Service))
	e := &Entry{Instance: inst}
	st.entries[fullName] = e
	return e
}

// ErrNoInterface is a network with no interface to multicast on.
var ErrNoInterface = errors.New("mdns: no such interface")
