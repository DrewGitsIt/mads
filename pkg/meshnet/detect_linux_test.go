//go:build linux

package meshnet

import "testing"

func TestLinuxParsers(t *testing.T) {
	route := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"eth0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"
	arp := "IP address       HW type     Flags       HW address            Mask     Device\n" +
		"192.168.1.1      0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0\n"
	if r := parseLinuxRouteTable(route); len(r["eth0"]) != 1 || r["eth0"][0].String() != "192.168.1.0/24" {
		t.Fatalf("route table %v", r)
	}
	gws := parseLinuxRoutes(route, arp)
	if len(gws) != 1 || gws[0].Iface != "eth0" || gws[0].IP.String() != "192.168.1.1" || gws[0].HW != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("%+v", gws)
	}
}
