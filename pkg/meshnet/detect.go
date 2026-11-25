package meshnet

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
)

// Detection (docs/MESH-NETWORK-DESIGN.md, "What ships"): the direct
// adapter reads this machine's interfaces and sorts each address into a
// network. Cheap, local only: it never sends a packet. The Mesh Service
// polls it every 10 s (Q11) to follow DHCP changes and VPNs coming and
// going; the Router polls it for S2's prefixes and to re-race when this
// device's networks change.

// Iface is one interface as the OS reports it.
type Iface struct {
	Name     string
	Up       bool
	Loopback bool
	// PointToPoint interfaces are how most VPNs show up.
	PointToPoint bool
	// Addrs are its addresses, each with its prefix length.
	Addrs []netip.Prefix
}

// Gateway is a default route's next hop on one interface.
type Gateway struct {
	Iface string
	IP    netip.Addr
	// HW is the gateway's hardware address, where the OS shows it (Q3).
	HW string
}

// System is what detection reads from the OS: a seam for tests (fake
// interface tables), real in production (OSSystem).
type System struct {
	Interfaces func() ([]Iface, error)
	// Gateways is best effort: none known reads as none.
	Gateways func() []Gateway
	// Wireless says whether an interface is Wi-Fi, and its network's name
	// (SSID) where the OS gives it freely.
	Wireless func(iface string) (bool, string)
	// Routes is each interface's routed prefixes (the default route aside),
	// best effort.
	Routes func() map[string][]netip.Prefix
}

// vpnFamily is the kind of VPN an interface's name says (zerotier,
// netbird, wireguard, utun, tun, ...), part of its id (R9): two VPNs on
// the same stock subnet (10.8.0.0/24) aren't one network.
func vpnFamily(name string) string {
	n := strings.ToLower(name)
	for _, f := range []struct{ prefix, family string }{
		{"tailscale", "tailscale"}, {"zt", "zerotier"}, {"wt", "netbird"}, {"nb-", "netbird"}, {"wg", "wireguard"},
		{"utun", "utun"}, {"tun", "tun"}, {"tap", "tap"}, {"ppp", "ppp"}, {"ipsec", "ipsec"}, {"nebula", "nebula"},
	} {
		if strings.HasPrefix(n, f.prefix) {
			return f.family
		}
	}
	return "p2p"
}

// vpnPrefix is a VPN's identity: its first IPv4 address's prefix, else
// its first address's.
func vpnPrefix(ps []netip.Prefix) netip.Prefix {
	for _, p := range ps {
		if p.Addr().Is4() {
			return p.Masked()
		}
	}
	return ps[0].Masked()
}

// TailnetRanges are Tailscale's address ranges: where its peers are. The
// IPv4 range is also carrier-grade NAT space and NetBird's, so it alone
// never says "Tailscale"; the IPv6 one is Tailscale's own.
var TailnetRanges = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}

var tailscaleULA = TailnetRanges[1]

// hiddenIfacePrefixes are interfaces that are never a network between the
// user's devices: container and VM bridges, Apple's peer-to-peer links,
// tunnels for IPv6 transition.
var hiddenIfacePrefixes = []string{
	"docker", "br-", "virbr", "veth", "vmnet", "vboxnet", "bridge", "awdl", "llw", "anpi",
	"ap", "gif", "stf", "cni", "flannel", "lxcbr", "lxdbr", "podman", "cali", "kube", "dummy",
	"p2p", "xhc", "vnic", "lo",
}

func hiddenIface(name string) bool {
	n := strings.ToLower(name)
	for _, p := range hiddenIfacePrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// vpnLabel is a VPN interface's label by its name, and whether the name
// says it's a VPN at all.
func vpnLabel(name string) (string, bool) {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "tailscale"):
		return "Tailscale", true
	case strings.HasPrefix(n, "zt"):
		return "VPN (ZeroTier)", true
	case n == "wt0" || strings.HasPrefix(n, "wt") || strings.HasPrefix(n, "nb-"):
		return "VPN (NetBird)", true
	case strings.HasPrefix(n, "wg"):
		return "VPN (" + name + ")", true
	case strings.HasPrefix(n, "utun"), strings.HasPrefix(n, "tun"), strings.HasPrefix(n, "tap"),
		strings.HasPrefix(n, "ppp"), strings.HasPrefix(n, "ipsec"), strings.HasPrefix(n, "nebula"):
		return "VPN (" + name + ")", true
	}
	return "", false
}

// sharedSSIDWords mark a Wi-Fi many strangers share (Q14).
var sharedSSIDWords = []string{"eduroam", "guest", "public", "hotel", "airport", "free", "starbucks", "campus"}

func looksShared(ssid string, p netip.Prefix) bool {
	s := strings.ToLower(ssid)
	for _, w := range sharedSSIDWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return p.Addr().Is4() && p.Bits() < 22
}

// Detect sorts sys's interfaces into networks. Helpers then describe them
// (Tailscale confirms its interface and names it).
func Detect(ctx context.Context, sys System, helpers ...Helper) ([]Network, error) {
	ifs, err := sys.Interfaces()
	if err != nil {
		return nil, err
	}
	var gws []Gateway
	if sys.Gateways != nil {
		gws = sys.Gateways()
	}
	var out []Network
	byID := map[string]int{}
	add := func(n Network) {
		if i, ok := byID[n.ID]; ok {
			// The same network on two interfaces (wired and Wi-Fi on one
			// router): one network, both addresses.
			m := &out[i]
			for _, a := range n.Addrs {
				if !slices.Contains(m.Addrs, a) {
					m.Addrs = append(m.Addrs, a)
				}
			}
			for _, p := range n.Prefixes {
				if !slices.Contains(m.Prefixes, p) {
					m.Prefixes = append(m.Prefixes, p)
				}
			}
			return
		}
		byID[n.ID] = len(out)
		out = append(out, n)
	}
	for _, ifc := range ifs {
		if !ifc.Up || ifc.Loopback || hiddenIface(ifc.Name) {
			continue
		}
		var usable []netip.Prefix
		for _, p := range ifc.Addrs {
			a := p.Addr().Unmap()
			if !a.IsValid() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() {
				continue
			}
			usable = append(usable, netip.PrefixFrom(a, p.Bits()))
		}
		if len(usable) == 0 {
			continue
		}
		label, isVPN := vpnLabel(ifc.Name)
		tailscaleish := strings.HasPrefix(strings.ToLower(ifc.Name), "tailscale")
		for _, p := range usable {
			if tailscaleULA.Contains(p.Addr()) {
				tailscaleish = true
			}
		}
		if !isVPN && ifc.PointToPoint {
			// An unnamed point-to-point interface with a private address is
			// a VPN of some kind.
			for _, p := range usable {
				if IsPrivate(p.Addr()) {
					isVPN, label = true, "VPN ("+ifc.Name+")"
				}
			}
		}
		if isVPN || tailscaleish {
			// A VPN's id is its address prefix, not its interface's name
			// (macOS renumbers utunN): a different VPN on the same
			// interface name is a different network (round-1 review M6).
			n := Network{ID: "vpn:" + vpnFamily(ifc.Name) + ":" + vpnPrefix(usable).String(), Kind: KindOverlay, Label: label, Iface: ifc.Name, Adapter: DirectVia}
			if tailscaleish {
				n.ID, n.Label = TailscaleID, "Tailscale"
			}
			for _, p := range usable {
				n.Addrs = append(n.Addrs, p.Addr())
				n.Prefixes = append(n.Prefixes, p.Masked())
			}
			// What's routed through it is on it too (a VPN's own address is
			// often a /32 with its range routed through the interface).
			if sys.Routes != nil {
				for _, r := range sys.Routes()[ifc.Name] {
					// Never a full tunnel's routes (0/1 + 128/1, anything
					// wider than /8): they'd put every private range "on" the
					// VPN (R3).
					if r.Bits() >= 8 {
						n.Prefixes = appendMissing(n.Prefixes, r.Masked())
					}
				}
			}
			switch {
			case tailscaleish:
				n.Prefixes = appendMissing(n.Prefixes, TailnetRanges...)
			case label == "VPN (NetBird)":
				// NetBird's range is CGNAT space; only for NetBird, never for
				// any interface that happens to have such an address (S1).
				for _, p := range usable {
					if TailnetRanges[0].Contains(p.Addr()) {
						n.Prefixes = appendMissing(n.Prefixes, TailnetRanges[0])
					}
				}
			}
			add(n)
			continue
		}
		wireless, ssid := false, ""
		if sys.Wireless != nil {
			wireless, ssid = sys.Wireless(ifc.Name)
		}
		var gw Gateway
		for _, g := range gws {
			if g.Iface == ifc.Name && g.IP.Is4() {
				gw = g
				break
			}
		}
		// Private addresses: one LAN network per IPv4 subnet, with the
		// interface's ULAs; public ones: one public network per interface.
		var lans []Network
		var ula []netip.Prefix
		pub := Network{ID: "public:" + ifc.Name, Kind: KindPublic, Iface: ifc.Name, Adapter: DirectVia}
		for _, p := range usable {
			a := p.Addr()
			switch {
			case a.Is4() && IsPrivate(a):
				subnet := p.Masked()
				id := "lan:" + subnet.String()
				if gw.IP.IsValid() && subnet.Contains(gw.IP) {
					id += "/" + gw.IP.String()
				}
				found := false
				for i := range lans {
					if lans[i].ID == id {
						lans[i].Addrs = append(lans[i].Addrs, a)
						found = true
					}
				}
				if !found {
					n := Network{ID: id, Kind: KindLAN, Iface: ifc.Name, Adapter: DirectVia, Addrs: []netip.Addr{a},
						Prefixes: []netip.Prefix{subnet}, Wireless: wireless, SSID: ssid, Shared: looksShared(ssid, subnet)}
					if subnet.Contains(gw.IP) {
						n.Gateway, n.GatewayHW = gw.IP, gw.HW
					}
					n.Label = lanLabel(ifc.Name, wireless, ssid)
					lans = append(lans, n)
				}
			case a.Is6() && IsPrivate(a):
				ula = append(ula, p)
			default:
				pub.Addrs = append(pub.Addrs, a)
				pub.Prefixes = append(pub.Prefixes, p.Masked())
			}
		}
		if len(lans) == 0 && len(ula) > 0 {
			n := Network{ID: "lan:" + ula[0].Masked().String(), Kind: KindLAN, Iface: ifc.Name, Adapter: DirectVia,
				Wireless: wireless, SSID: ssid, Label: lanLabel(ifc.Name, wireless, ssid)}
			lans = append(lans, n)
		}
		if len(lans) > 0 {
			for _, p := range ula {
				lans[0].Addrs = append(lans[0].Addrs, p.Addr())
				lans[0].Prefixes = appendMissing(lans[0].Prefixes, p.Masked())
			}
		}
		for _, n := range lans {
			add(n)
		}
		if len(pub.Addrs) > 0 {
			pub.Label = publicLabel(pub.Addrs)
			add(pub)
		}
	}
	for i := range out {
		for _, h := range helpers {
			h.Describe(ctx, &out[i])
		}
	}
	SortNetworks(out)
	return out, nil
}

// SortNetworks orders networks as setup offers them: Tailscale first,
// other VPNs, LANs, then public; by id within each.
func SortNetworks(ns []Network) {
	rank := func(n Network) int {
		switch {
		case n.ID == TailscaleID:
			return 0
		case n.Kind == KindOverlay:
			return 1
		case n.Kind == KindLAN:
			return 2
		}
		return 3
	}
	slices.SortStableFunc(ns, func(a, b Network) int {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra - rb
		}
		return strings.Compare(a.ID, b.ID)
	})
}

func appendMissing(ps []netip.Prefix, more ...netip.Prefix) []netip.Prefix {
	for _, p := range more {
		if !slices.Contains(ps, p) {
			ps = append(ps, p)
		}
	}
	return ps
}

func lanLabel(iface string, wireless bool, ssid string) string {
	switch {
	case wireless && ssid != "":
		return `Wi-Fi "` + cleanLabel(ssid) + `"`
	case wireless:
		return "Wi-Fi"
	}
	n := strings.ToLower(iface)
	if strings.HasPrefix(n, "eth") || strings.HasPrefix(n, "enp") || strings.HasPrefix(n, "eno") || strings.HasPrefix(n, "ens") || strings.HasPrefix(n, "enx") {
		return "Wired network (" + iface + ")"
	}
	return "Local network (" + iface + ")"
}

func publicLabel(addrs []netip.Addr) string {
	v4, v6 := false, false
	for _, a := range addrs {
		if a.Is4() {
			v4 = true
		} else {
			v6 = true
		}
	}
	switch {
	case v4 && v6:
		return "Public IPv4 and IPv6"
	case v6:
		return "Public IPv6"
	}
	return "Public IPv4"
}

// Prefixes is every prefix of ns: S2's "networks this device is on".
func Prefixes(ns []Network) []netip.Prefix {
	var out []netip.Prefix
	for _, n := range ns {
		out = appendMissing(out, n.Prefixes...)
	}
	return out
}

// SameNetworks reports whether a and b are the same networks with the same
// addresses (a change means listeners move and viewers re-race).
func SameNetworks(a, b []Network) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || !slices.Equal(a[i].Addrs, b[i].Addrs) || !slices.Equal(a[i].Prefixes, b[i].Prefixes) {
			return false
		}
	}
	return true
}

// goInterfaces is System.Interfaces from net.Interfaces.
func goInterfaces() ([]Iface, error) {
	list, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(list))
	for _, ifc := range list {
		i := Iface{Name: ifc.Name, Up: ifc.Flags&net.FlagUp != 0, Loopback: ifc.Flags&net.FlagLoopback != 0,
			PointToPoint: ifc.Flags&net.FlagPointToPoint != 0}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ones, _ := ipn.Mask.Size()
			ip = ip.Unmap()
			if ip.Is4() && ones > 32 {
				ones -= 96
			}
			i.Addrs = append(i.Addrs, netip.PrefixFrom(ip, ones))
		}
		out = append(out, i)
	}
	return out, nil
}

// OSSystem reads the real machine: net.Interfaces, and the OS's route
// table and Wi-Fi state where it shows them (detect_<os>.go). Local reads
// only: nothing is sent on any network.
func OSSystem() System {
	return System{Interfaces: goInterfaces, Gateways: osGateways, Wireless: osWireless, Routes: osRoutes}
}
