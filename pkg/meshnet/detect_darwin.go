//go:build darwin

package meshnet

import (
	"bufio"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// macOS shows its route table and ARP cache through netstat and arp, and
// which interfaces are Wi-Fi through networksetup. Each is a local read;
// the answers are cached so the 10 s scan spawns little. The Wi-Fi name
// (SSID) needs Location permission since macOS 14, so it isn't read (Q15):
// a Wi-Fi network is labelled "Wi-Fi".

var darwinCache struct {
	sync.Mutex
	gwAt   time.Time
	gws    []Gateway
	portAt time.Time
	wifi   map[string]bool
}

func osGateways() []Gateway {
	darwinCache.Lock()
	defer darwinCache.Unlock()
	if time.Since(darwinCache.gwAt) < 30*time.Second {
		return darwinCache.gws
	}
	out, err := exec.Command("/usr/sbin/netstat", "-rn", "-f", "inet").Output()
	var gws []Gateway
	if err == nil {
		gws = parseDarwinRoutes(string(out))
		for i := range gws {
			if arp, err := exec.Command("/usr/sbin/arp", "-n", gws[i].IP.String()).Output(); err == nil {
				gws[i].HW = parseDarwinARP(string(arp))
			}
		}
	}
	darwinCache.gwAt, darwinCache.gws = time.Now(), gws
	return gws
}

// parseDarwinRoutes reads netstat -rn's default routes.
func parseDarwinRoutes(s string) []Gateway {
	var out []Gateway
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[0] != "default" {
			continue
		}
		ip, err := netip.ParseAddr(f[1])
		if err != nil || !ip.Is4() {
			continue
		}
		out = append(out, Gateway{Iface: f[3], IP: ip})
	}
	return out
}

// parseDarwinARP reads "? (192.168.1.1) at aa:bb:cc:dd:ee:ff on en0 ...".
func parseDarwinARP(s string) string {
	f := strings.Fields(s)
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "at" && strings.Count(f[i+1], ":") == 5 {
			return strings.ToLower(f[i+1])
		}
	}
	return ""
}

func osWireless(iface string) (bool, string) {
	darwinCache.Lock()
	defer darwinCache.Unlock()
	if darwinCache.wifi == nil || time.Since(darwinCache.portAt) > 5*time.Minute {
		darwinCache.portAt = time.Now()
		if out, err := exec.Command("/usr/sbin/networksetup", "-listallhardwareports").Output(); err == nil {
			darwinCache.wifi = parseHardwarePorts(string(out))
		} else if darwinCache.wifi == nil {
			darwinCache.wifi = map[string]bool{}
		}
	}
	return darwinCache.wifi[iface], ""
}

// parseHardwarePorts reads networksetup -listallhardwareports: which
// devices are Wi-Fi.
func parseHardwarePorts(s string) map[string]bool {
	out := map[string]bool{}
	port := ""
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "Hardware Port: "); ok {
			port = v
		}
		if v, ok := strings.CutPrefix(line, "Device: "); ok {
			out[v] = port == "Wi-Fi" || port == "AirPort"
		}
	}
	return out
}

var routeCache struct {
	sync.Mutex
	at time.Time
	m  map[string][]netip.Prefix
}

// osRoutes reads netstat -rn's IPv4 network routes, by interface (cached
// 30 s).
func osRoutes() map[string][]netip.Prefix {
	routeCache.Lock()
	defer routeCache.Unlock()
	if time.Since(routeCache.at) < 30*time.Second && routeCache.m != nil {
		return routeCache.m
	}
	out, err := exec.Command("/usr/sbin/netstat", "-rn", "-f", "inet").Output()
	routeCache.at = time.Now()
	if err != nil {
		routeCache.m = map[string][]netip.Prefix{}
		return routeCache.m
	}
	routeCache.m = parseDarwinRouteTable(string(out))
	return routeCache.m
}

// parseDarwinRouteTable reads netstat's network routes ("100.64/10 link#22
// UCS utun4"): the destination's octets padded, with its length.
func parseDarwinRouteTable(s string) map[string][]netip.Prefix {
	out := map[string][]netip.Prefix{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || !strings.Contains(f[0], "/") {
			continue
		}
		dst, bitsS, _ := strings.Cut(f[0], "/")
		parts := strings.Split(dst, ".")
		if len(parts) > 4 {
			continue
		}
		for len(parts) < 4 {
			parts = append(parts, "0")
		}
		ip, err := netip.ParseAddr(strings.Join(parts, "."))
		bits, berr := strconv.Atoi(bitsS)
		if err != nil || berr != nil || bits <= 0 || bits > 32 {
			continue
		}
		if p, err := ip.Prefix(bits); err == nil {
			out[f[3]] = append(out[f[3]], p)
		}
	}
	return out
}
