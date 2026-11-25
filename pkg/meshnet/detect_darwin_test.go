//go:build darwin

package meshnet

import "testing"

func TestDarwinParsers(t *testing.T) {
	routes := `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            192.168.1.1        UGScg                 en0
default            link#22            UCSIg               utun4
127                127.0.0.1          UCS                   lo0
`
	gws := parseDarwinRoutes(routes)
	if len(gws) != 1 || gws[0].Iface != "en0" || gws[0].IP.String() != "192.168.1.1" {
		t.Fatalf("%+v", gws)
	}
	if hw := parseDarwinARP("? (192.168.1.1) at aa:bb:cc:dd:ee:ff on en0 ifscope [ethernet]\n"); hw != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("hw %q", hw)
	}
	if hw := parseDarwinARP("? (192.168.1.1) at (incomplete) on en0"); hw != "" {
		t.Fatalf("hw %q", hw)
	}
	table := parseDarwinRouteTable("Destination Gateway Flags Netif\ndefault 192.168.1.1 UGScg en0\n100.64/10 link#22 UCS utun4\n10.147.17/24 link#23 UC zt0\n192.168.1.1 aa:bb UHLWIir en0\n")
	if len(table["utun4"]) != 1 || table["utun4"][0].String() != "100.64.0.0/10" || table["zt0"][0].String() != "10.147.17.0/24" || len(table["en0"]) != 0 {
		t.Fatalf("routes %v", table)
	}
	ports := parseHardwarePorts("Hardware Port: Wi-Fi\nDevice: en0\nEthernet Address: x\n\nHardware Port: Thunderbolt Ethernet\nDevice: en5\n")
	if !ports["en0"] || ports["en5"] {
		t.Fatalf("%v", ports)
	}
}
