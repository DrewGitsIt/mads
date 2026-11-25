//go:build linux

package meshnet

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// osGateways reads default routes from /proc/net/route, and each
// gateway's hardware address from /proc/net/arp.
func osGateways() []Gateway {
	return parseLinuxRoutes(readFile("/proc/net/route"), readFile("/proc/net/arp"))
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseLinuxRoutes is osGateways on the files' contents.
func parseLinuxRoutes(route, arp string) []Gateway {
	hw := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(arp))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 6 && f[3] != "00:00:00:00:00:00" && strings.Count(f[3], ":") == 5 {
			hw[f[0]+" "+f[5]] = strings.ToLower(f[3])
		}
	}
	var out []Gateway
	sc = bufio.NewScanner(strings.NewReader(route))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		raw, err := hex.DecodeString(f[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], binary.BigEndian.Uint32(raw))
		ip := netip.AddrFrom4(b)
		if ip.IsUnspecified() {
			continue
		}
		out = append(out, Gateway{Iface: f[0], IP: ip, HW: hw[ip.String()+" "+f[0]]})
	}
	return out
}

var ssidCache struct {
	sync.Mutex
	at   map[string]time.Time
	ssid map[string]string
}

// osWireless: an interface with /sys/class/net/<if>/wireless is Wi-Fi; its
// SSID is iwgetid's, when installed (cached a minute).
func osWireless(iface string) (bool, string) {
	if strings.ContainsAny(iface, "/.") {
		return false, ""
	}
	if _, err := os.Stat("/sys/class/net/" + iface + "/wireless"); err != nil {
		return false, ""
	}
	ssidCache.Lock()
	defer ssidCache.Unlock()
	if ssidCache.at == nil {
		ssidCache.at, ssidCache.ssid = map[string]time.Time{}, map[string]string{}
	}
	if at, ok := ssidCache.at[iface]; ok && time.Since(at) < time.Minute {
		return true, ssidCache.ssid[iface]
	}
	ssid := ""
	if path, err := exec.LookPath("iwgetid"); err == nil {
		if out, err := exec.Command(path, "-r", iface).Output(); err == nil {
			ssid = strings.TrimSpace(string(out))
		}
	}
	ssidCache.at[iface], ssidCache.ssid[iface] = time.Now(), ssid
	return true, ssid
}

// osRoutes reads each interface's IPv4 routes from /proc/net/route.
func osRoutes() map[string][]netip.Prefix { return parseLinuxRouteTable(readFile("/proc/net/route")) }

func parseLinuxRouteTable(route string) map[string][]netip.Prefix {
	out := map[string][]netip.Prefix{}
	sc := bufio.NewScanner(strings.NewReader(route))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[1] == "Destination" || f[1] == "00000000" {
			continue
		}
		dst, err1 := hex.DecodeString(f[1])
		mask, err2 := hex.DecodeString(f[7])
		if err1 != nil || err2 != nil || len(dst) != 4 || len(mask) != 4 {
			continue
		}
		var d [4]byte
		binary.LittleEndian.PutUint32(d[:], binary.BigEndian.Uint32(dst))
		m := binary.LittleEndian.Uint32(mask)
		bits := 0
		for m != 0 {
			bits += int(m & 1)
			m >>= 1
		}
		if p, err := netip.AddrFrom4(d).Prefix(bits); err == nil && bits > 0 {
			out[f[0]] = append(out[f[0]], p)
		}
	}
	return out
}
