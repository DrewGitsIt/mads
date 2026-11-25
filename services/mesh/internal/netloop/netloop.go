// Package netloop is the Mesh Service's network loop
// (docs/MESH-NETWORK-DESIGN.md U2, F5, "What the Mesh Service reports"):
// every 10 s (and at once when this host's settings or setup window
// change) it detects this machine's networks, listens on exactly the mesh
// networks its user chose that it's on now, moves a listener when its
// network's addresses change, and reports what it found and where it
// listens (mesh.listening, debounced). A network the user didn't choose
// is never listened on; a fresh install (none chosen) listens nowhere.
//
// While the setup window is open it also advertises _mads._tcp on each
// LAN mesh network it listens on (mdns), and stops when the window closes.
package netloop

import (
	"context"
	"log"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/pkg/meshnet/tailscale"
)

// Options configures a Loop.
type Options struct {
	// Adapter detects, listens and advertises (meshnet.Direct).
	Adapter meshnet.Adapter
	// Tailscale reads tailscaled's status (nil: no Tailscale helper).
	Tailscale *tailscale.Helper
	// Settings is this host's mesh settings as last read; false until
	// they have been (nothing is listened on before).
	Settings func() (contracts.MeshSettings, bool)
	// Window is the setup window's end (zero: closed).
	Window func() time.Time
	// FixedAddr, when set (-addr), is the only address listened on,
	// whatever the settings: tests and manual setups.
	FixedAddr string
	// Port, when nonzero (-port given), overrides the settings' port.
	Port int
	// Bind is called with each new listener: the caller serves it until
	// it's closed. n is the network it's on.
	Bind func(n meshnet.Network, l net.Listener)
	// Report receives each changed report (debounced to one per Debounce).
	Report func(contracts.MeshListener)
	// Base is the report's fixed part: fingerprint, platform, hosts.
	Base contracts.MeshListener
	// Advertise runs the mDNS advert for n on port until ctx ends (nil:
	// no adverts).
	Advertise func(ctx context.Context, n meshnet.Network, port int, addrs []netip.Addr) error
	// TailnetCheck is the identity check as applied now.
	TailnetCheck func() bool
	// Every is the scan interval (10 s), Debounce the report's (5 s).
	Every, Debounce time.Duration
	Now             func() time.Time
}

// Loop is the running loop.
type Loop struct {
	opts Options
	kick chan struct{}

	mu        sync.Mutex
	bound     map[string]*bound // by network id
	addrs     []contracts.NetAddress
	ts        tailscale.Status
	tsErr     error
	tsOK      bool
	last      contracts.MeshListener
	reported  bool
	lastAt    time.Time
	pending   *contracts.MeshListener
	changed   map[string][]string // network id -> its addresses before a move
	checkLog  string
	listenErr map[string]string
}

type bound struct {
	n      meshnet.Network
	port   int
	ls     []net.Listener
	err    string
	advert context.CancelFunc
}

// New is a Loop; Run starts it.
func New(opts Options) *Loop {
	if opts.Every <= 0 {
		opts.Every = 10 * time.Second
	}
	if opts.Debounce <= 0 {
		opts.Debounce = 5 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Window == nil {
		opts.Window = func() time.Time { return time.Time{} }
	}
	if opts.TailnetCheck == nil {
		opts.TailnetCheck = func() bool { return false }
	}
	return &Loop{opts: opts, kick: make(chan struct{}, 1), bound: map[string]*bound{}, changed: map[string][]string{}, listenErr: map[string]string{}}
}

// Kick asks for a scan now (settings or the window changed, a listener
// failed).
func (l *Loop) Kick() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// Run scans until ctx ends, then closes every listener.
func (l *Loop) Run(ctx context.Context) {
	t := time.NewTicker(l.opts.Every)
	defer t.Stop()
	flush := time.NewTicker(time.Second)
	defer flush.Stop()
	for {
		l.Scan(ctx)
		select {
		case <-ctx.Done():
			l.closeAll()
			return
		case <-t.C:
		case <-l.kick:
		case <-flush.C:
			l.flushReport()
			continue
		}
	}
}

// Addresses is where this host listens now, in its preferred order: what
// /mesh/hello reports.
func (l *Loop) Addresses() []contracts.NetAddress {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.addrs)
}

// TailscaleSelf is this node as tailscaled last said (scanned, never read
// on a request's path).
func (l *Loop) TailscaleSelf() (tailscale.Self, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.tsOK || l.ts.Self == nil {
		if l.tsErr != nil {
			return tailscale.Self{}, l.tsErr
		}
		return tailscale.Self{}, tailscale.ErrNotFound
	}
	n := l.ts.Self
	s := tailscale.Self{HostName: n.HostName, DNSName: n.DNSName, UserID: n.UserID, IPs: n.TailscaleIPs, KeyExpiry: n.KeyExpiry}
	for _, ip := range n.TailscaleIPs {
		if ip.Is4() {
			s.IPv4 = ip.String()
			break
		}
	}
	return s, nil
}

// KeyExpiry is this node's Tailscale key expiry, when it's on Tailscale
// with expiry on.
func (l *Loop) KeyExpiry() *time.Time {
	self, err := l.TailscaleSelf()
	if err != nil || self.KeyExpiry == nil {
		return nil
	}
	t := *self.KeyExpiry
	return &t
}

// Scan is one pass: detect, reconcile listeners and adverts, report.
func (l *Loop) Scan(ctx context.Context) {
	nets, derr := l.opts.Adapter.Detect(ctx)
	if derr != nil {
		log.Printf("mesh: detect networks: %v", derr)
	}
	l.readTailscale(ctx)
	settings, have := l.opts.Settings()
	want, missing := l.desired(nets, settings, have)
	port := l.opts.Port
	if port == 0 {
		port = settings.MeshPort()
	}
	if l.opts.FixedAddr != "" {
		if _, p, err := net.SplitHostPort(l.opts.FixedAddr); err == nil {
			port, _ = strconv.Atoi(p)
		}
	}
	windowOpen := l.opts.Now().Before(l.opts.Window())

	l.mu.Lock()
	// Close what's no longer wanted, or moved.
	for id, b := range l.bound {
		n, ok := want[id]
		if ok && slices.Equal(n.Addrs, b.n.Addrs) && b.port == port && len(b.ls) > 0 {
			b.n = n // labels may change
			continue
		}
		if ok && !slices.Equal(n.Addrs, b.n.Addrs) {
			l.changed[id] = addrStrings(b.n.Addrs)
			log.Printf("mesh: %s moved from %v to %v: moving its listener", id, b.n.Addrs, n.Addrs)
		}
		b.close()
		delete(l.bound, id)
	}
	// Open what's wanted and not open.
	for id, n := range want {
		if _, ok := l.bound[id]; ok {
			continue
		}
		b := &bound{n: n, port: port}
		ls, err := l.opts.Adapter.Listen(ctx, n, port)
		if err != nil {
			b.err = err.Error()
			if l.listenErr[id] != b.err {
				log.Printf("mesh: listen on %s (%s): %v", n.Label, id, err)
			}
			l.listenErr[id] = b.err
		} else {
			delete(l.listenErr, id)
		}
		b.ls = ls
		for _, ln := range ls {
			log.Printf("mesh: listening on %s (%s) at %s", n.Label, id, ln.Addr())
			if l.opts.Bind != nil {
				l.opts.Bind(n, ln)
			}
		}
		if len(ls) == 0 && b.err == "" {
			b.err = "no address to listen on"
		}
		l.bound[id] = b
	}
	// The setup window's mDNS adverts: only on LAN mesh networks.
	for _, b := range l.bound {
		should := windowOpen && b.n.Kind == meshnet.KindLAN && len(b.ls) > 0 && l.opts.Advertise != nil
		switch {
		case should && b.advert == nil:
			actx, cancel := context.WithCancel(context.Background())
			b.advert = cancel
			n, p := b.n, listenPort(b.ls[0], b.port)
			var v4 []netip.Addr
			for _, a := range n.Addrs {
				if a.Is4() {
					v4 = append(v4, a)
				}
			}
			go func() {
				if err := l.opts.Advertise(actx, n, p, v4); err != nil && actx.Err() == nil {
					log.Printf("mesh: setup window: mDNS advert on %s: %v", n.Label, err)
				}
			}()
		case !should && b.advert != nil:
			b.advert()
			b.advert = nil
		}
	}
	// Where other devices reach this host now, in the settings' order.
	var cands []meshnet.Candidate
	for _, id := range orderOf(want, settings) {
		if b := l.bound[id]; b != nil && len(b.ls) > 0 {
			cands = append(cands, l.opts.Adapter.Advertise(b.n, b.ls)...)
		}
	}
	if len(cands) > meshnet.MaxCandidates {
		cands = cands[:meshnet.MaxCandidates]
	}
	l.addrs = meshnet.ToNets(cands)
	report := l.reportLocked(nets, want, missing, settings, port)
	l.mu.Unlock()
	l.offer(report)
}

// desired is the networks to listen on now (by id), and the chosen ones
// this device isn't on (id → why).
func (l *Loop) desired(nets []meshnet.Network, s contracts.MeshSettings, have bool) (map[string]meshnet.Network, map[string]string) {
	want, missing := map[string]meshnet.Network{}, map[string]string{}
	if l.opts.FixedAddr != "" {
		host, _, err := net.SplitHostPort(l.opts.FixedAddr)
		ip, perr := netip.ParseAddr(host)
		if err != nil || perr != nil || ip.IsUnspecified() {
			missing["fixed"] = "-addr " + l.opts.FixedAddr + " isn't a specific IP address and port"
			return want, missing
		}
		n := meshnet.Network{ID: "fixed", Kind: meshnet.InferKind(meshnet.Candidate{Addr: l.opts.FixedAddr}), Label: "-addr " + host, Addrs: []netip.Addr{ip}, Adapter: meshnet.DirectVia}
		for _, d := range nets {
			if slices.Contains(d.Addrs, ip) {
				n.ID, n.Kind, n.Label = d.ID, d.Kind, d.Label
			}
		}
		want[n.ID] = n
		return want, missing
	}
	if !have {
		return want, missing
	}
	byID := map[string]meshnet.Network{}
	for _, n := range nets {
		byID[n.ID] = n
	}
	for _, c := range s.Chosen() {
		n, ok := byID[c.ID]
		switch {
		case !ok:
			missing[c.ID] = "this device isn't on it now"
		case n.Kind == meshnet.KindLAN && c.GatewayHW == "":
			// A LAN chosen without its router's hardware address can't be
			// told from another with the same addresses (round-2 R10).
			missing[c.ID] = "chosen before its router's hardware address was known: choose it again in Set up mesh"
		case c.GatewayHW != "" && n.GatewayHW == "":
			// Chosen by its router's hardware address, which isn't known now
			// (round-1 review M5): not listened on until it's confirmed.
			missing[c.ID] = "can't confirm it's the same network yet (its router's hardware address isn't known)"
		case c.GatewayHW != "" && c.GatewayHW != n.GatewayHW:
			// Same addresses, another router (Q3): not the network chosen.
			missing[c.ID] = "a different network with the same addresses"
		case c.SSID != "" && n.SSID != "" && c.SSID != n.SSID:
			missing[c.ID] = "a different Wi-Fi with the same addresses"
		default:
			want[c.ID] = n
		}
	}
	if s.Public {
		for _, n := range nets {
			if n.Kind == meshnet.KindPublic {
				want[n.ID] = n
			}
		}
	}
	return want, missing
}

// orderOf is want's ids in the settings' order, then the rest by id.
func orderOf(want map[string]meshnet.Network, s contracts.MeshSettings) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range s.Networks {
		if _, ok := want[c.ID]; ok && !seen[c.ID] {
			out = append(out, c.ID)
			seen[c.ID] = true
		}
	}
	var rest []string
	for id := range want {
		if !seen[id] {
			rest = append(rest, id)
		}
	}
	slices.Sort(rest)
	return append(out, rest...)
}

func listenPort(l net.Listener, def int) int {
	if ta, ok := l.Addr().(*net.TCPAddr); ok {
		return ta.Port
	}
	return def
}

func (b *bound) close() {
	if b.advert != nil {
		b.advert()
		b.advert = nil
	}
	for _, l := range b.ls {
		_ = l.Close()
	}
	b.ls = nil
}

func (l *Loop) closeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, b := range l.bound {
		b.close()
		delete(l.bound, id)
	}
	l.addrs = nil
}

// readTailscale refreshes the tailscaled snapshot (a local LocalAPI read).
func (l *Loop) readTailscale(ctx context.Context) {
	if l.opts.Tailscale == nil {
		l.mu.Lock()
		l.tsOK, l.tsErr = false, tailscale.ErrNotFound
		l.mu.Unlock()
		return
	}
	st, err := l.opts.Tailscale.Status(ctx)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.tsOK, l.tsErr = false, err
		return
	}
	l.ts, l.tsOK, l.tsErr = st, true, nil
}

func addrStrings(as []netip.Addr) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.String())
	}
	return out
}

// reportLocked builds mesh.listening's payload. l.mu must be held.
func (l *Loop) reportLocked(nets []meshnet.Network, want map[string]meshnet.Network, missing map[string]string, s contracts.MeshSettings, port int) contracts.MeshListener {
	r := l.opts.Base
	r.Port, r.Addresses = port, slices.Clone(l.addrs)
	r.RequireTailnet = l.opts.TailnetCheck()
	chosen := map[string]bool{}
	for _, c := range s.Chosen() {
		chosen[c.ID] = true
	}
	seen := map[string]bool{}
	for _, n := range nets {
		seen[n.ID] = true
		ln := contracts.ListeningNetwork{ID: n.ID, Kind: string(n.Kind), Label: n.Label, Iface: n.Iface, Addrs: addrStrings(n.Addrs), Chosen: chosen[n.ID]}
		if b := l.bound[n.ID]; b != nil {
			ln.Listening, ln.Error = len(b.ls) > 0, b.err
		} else if why, ok := missing[n.ID]; ok {
			ln.Error = why
		}
		if from, ok := l.changed[n.ID]; ok {
			ln.ChangedFrom = from
			delete(l.changed, n.ID)
		}
		r.Networks = append(r.Networks, ln)
	}
	for id, why := range missing {
		if !seen[id] {
			r.Networks = append(r.Networks, contracts.ListeningNetwork{ID: id, Chosen: chosen[id], Error: why})
		}
	}
	for id, n := range want {
		if !seen[id] {
			b := l.bound[id]
			ln := contracts.ListeningNetwork{ID: id, Kind: string(n.Kind), Label: n.Label, Addrs: addrStrings(n.Addrs), Chosen: true}
			if b != nil {
				ln.Listening, ln.Error = len(b.ls) > 0, b.err
			}
			r.Networks = append(r.Networks, ln)
		}
	}
	if len(r.Addresses) > 0 {
		r.MeshURL = contracts.MeshURLOf(r.Addresses[0].Addr)
	}
	// The Tailscale side: what Settings shows on the Mesh network card.
	ts := &contracts.TailscaleState{Present: l.tsOK}
	if l.tsOK {
		ts.BackendState, ts.NeedsLogin, ts.AuthURL = l.ts.BackendState, l.ts.NeedsLogin(), l.ts.AuthURL
		if self := l.ts.Self; self != nil {
			ts.DNSName, ts.HostName, ts.KeyExpiry = self.DNSName, self.HostName, self.KeyExpiry
			ts.KeyExpiryOff = self.KeyExpiry == nil
			if r.Node == "" {
				r.Node = self.HostName
			}
			r.DNSName = self.DNSName
		}
		if l.ts.CurrentTailnet != nil {
			ts.Tailnet = l.ts.CurrentTailnet.Name
		}
		if l.opts.Tailscale != nil {
			if e, ok := l.opts.Tailscale.Client.Endpoint(); ok {
				ts.Endpoint = e.Variant
			}
		}
	} else if l.tsErr != nil {
		ts.Error = l.tsErr.Error()
	}
	r.Tailscale = ts
	status := "off"
	if l.opts.TailnetCheck() {
		status = "on"
		if !l.tsOK || l.ts.Self == nil || l.ts.Self.UserID == 0 {
			status = "unavailable"
		}
	}
	r.TailnetCheckStatus = status
	if status != l.checkLog {
		switch {
		case status == "unavailable":
			log.Printf("mesh: the Tailscale identity check is on, but tailscaled can't be asked (%v): every connection on the Tailscale network is refused until it can, or the check is turned off", l.tsErr)
		case l.checkLog == "unavailable":
			log.Printf("mesh: tailscaled answers again: the Tailscale identity check applies")
		}
		l.checkLog = status
	}
	return r
}

// offer queues a report if it differs from the last one sent.
func (l *Loop) offer(r contracts.MeshListener) {
	l.mu.Lock()
	cmp := r
	prev := l.last
	cmp.At, prev.At = time.Time{}, time.Time{}
	if l.reported && reflect.DeepEqual(cmp, prev) && l.pending == nil {
		l.mu.Unlock()
		return
	}
	l.pending = &r
	l.mu.Unlock()
	l.flushReport()
}

// flushReport sends the pending report, at most once per Debounce.
func (l *Loop) flushReport() {
	l.mu.Lock()
	if l.pending == nil || (l.reported && l.opts.Now().Sub(l.lastAt) < l.opts.Debounce) {
		l.mu.Unlock()
		return
	}
	r := *l.pending
	r.At = l.opts.Now().UTC()
	l.pending, l.last, l.reported, l.lastAt = nil, r, true, l.opts.Now()
	l.mu.Unlock()
	if l.opts.Report != nil {
		l.opts.Report(r)
	}
}
