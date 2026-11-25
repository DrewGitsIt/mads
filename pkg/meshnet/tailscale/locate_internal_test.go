package tailscale

import (
	"errors"
	"testing"
)

// Captured `netstat -anv -p tcp` (macOS 26: a process:pid column). The
// standalone app's network extension listens as root on 127.0.0.1:49231;
// lsof run unprivileged doesn't see it at all.
const netstatNew = `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address                                 Foreign Address                               (state)          rxbytes      txbytes  rhiwat  shiwat          process:pid    state  options           gencnt    flags   flags1 usecnt rtncnt fltrs
tcp4       0      0  192.0.2.106.57797      198.51.100.10.443      ESTABLISHED        12088       180839  131072  148152           claude:39443  00102 00000008 00000000071c3069 00000081 04000900      2      0 000000
tcp4       0      0  127.0.0.1.49231        *.*                    LISTEN                 0            0  131072  131072 io.tailscale.ipn.macsys.network-extension:512  00180 00000006 00000000071c2df2 00000000 00000800      1      0 000000
tcp4       0      0  127.0.0.1.49231        127.0.0.1.60001        ESTABLISHED            0            0  131072  131072 io.tailscale.ipn.macsys.network-extension:512  00180 00000006 00000000071c2df9 00000000 00000800      1      0 000000
tcp46      0      0  *.49636                *.*                    LISTEN                 0            0  131072  131072         rapportd:788    00100 00000006 00000000057d4b33 00000000 00080800      1      0 000000
tcp6       0      0  ::1.25                 *.*                    LISTEN                 0            0  131072  131072           master:95666  00180 00000006 00000000071c2de6 00000000 00000800      1      0 000000
`

// The same from an older macOS: a bare pid column.
const netstatOld = `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)     rhiwat shiwat    pid   epid  state    options
tcp4       0      0  127.0.0.1.49231        *.*                    LISTEN      131072 131072    512      0 0x0080 0x00000006
tcp46      0      0  *.49636                *.*                    LISTEN      131072 131072    788      0 0x0100 0x00000006
`

// Captured `ps -o pid=,uid=,comm= -p ...`.
const psOut = `  512     0 /Library/SystemExtensions/0A1B2C3D-0000-4000-8000-000000000000/io.tailscale.ipn.macsys.network-extension.systemextension/Contents/MacOS/io.tailscale.ipn.macsys.network-extension
  788   501 /usr/libexec/rapportd
  901   501 /usr/bin/nc
`

func fixtures(netstat string) (func() ([]byte, error), func(string) ([]byte, error)) {
	return func() ([]byte, error) { return []byte(netstat), nil },
		func(string) ([]byte, error) { return []byte(psOut), nil }
}

// Final-pass review 1: listeners come from netstat (every pid, root's
// too) and ps, in both netstat formats.
func TestListenersFromNetstat(t *testing.T) {
	for name, out := range map[string]string{"new": netstatNew, "old": netstatOld} {
		ns, ps := fixtures(out)
		ls, err := listenersOn(49231, ns, ps)
		if err != nil || len(ls) != 1 || ls[0].PID != 512 || ls[0].UID != 0 {
			t.Fatalf("%s: %+v %v", name, ls, err)
		}
		if ls, err := listenersOn(17999, ns, ps); err == nil {
			t.Fatalf("%s: nothing listens on 17999, got %+v", name, ls)
		}
	}
	if _, err := listenersOn(1, func() ([]byte, error) { return []byte("garbage\n"), nil }, nil); err == nil {
		t.Fatal("unrecognised netstat output read as nothing listening")
	}
}

// The standalone app's root extension is trusted; a port it shares with
// another user's process isn't; a pid ps can't name isn't.
func TestVerifyWithNetstat(t *testing.T) {
	e := Endpoint{Network: "tcp", Address: "127.0.0.1:49231", Variant: "macos-standalone"}
	loc := func(netstat string, ps func(string) ([]byte, error)) Locator {
		return Locator{GOOS: "darwin", UID: func() int { return 501 }, ListenerOwner: func(port int) ([]Listener, error) {
			return listenersOn(port, func() ([]byte, error) { return []byte(netstat), nil }, ps)
		}}
	}
	_, ps := fixtures("")
	if err := loc(netstatNew, ps).Verify(e); err != nil {
		t.Fatalf("the standalone app's root network extension: %v", err)
	}
	shared := netstatNew + "tcp46      0      0  *.49231                *.*                    LISTEN                 0            0  131072  131072               nc:901    00100 00000006 00000000057d4b39 00000000 00080800      1      0 000000\n"
	if err := loc(shared, ps).Verify(e); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("a port shared with this user's nc: %v", err)
	}
	gone := func(string) ([]byte, error) {
		return []byte("  788   501 /usr/libexec/rapportd\n"), errors.New("exit status 1")
	}
	if err := loc(netstatNew, gone).Verify(e); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("a listener ps can't name: %v", err)
	}
	// The App Store app's extension runs as this user, as IPNExtension.
	store := Endpoint{Network: "tcp", Address: "127.0.0.1:49300", Variant: "macos-app-store"}
	ext := netstatNew + "tcp4       0      0  127.0.0.1.49300        *.*                    LISTEN                 0            0  131072  131072     IPNExtension:933    00180 00000006 00000000071c2e01 00000000 00000800      1      0 000000\n"
	extPS := func(string) ([]byte, error) {
		return []byte("  933   501 /Applications/Tailscale.app/Contents/PlugIns/IPNExtension.appex/Contents/MacOS/IPNExtension\n"), nil
	}
	if err := loc(ext, extPS).Verify(store); err != nil {
		t.Fatalf("the App Store extension: %v", err)
	}
}
