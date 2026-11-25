// Package tailscaletest is a fake tailscaled LocalAPI for tests: served on
// a unix socket (Linux, the open-source daemon) or on a localhost TCP port
// behind a token laid out as the macOS app's are (the standalone app's
// /Library/Tailscale/ipnport, the App Store app's group container). It
// never touches a real tailnet.
package tailscaletest

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/meshnet/tailscale"
)

// Fake is a fake LocalAPI. Change its fields under Lock.
type Fake struct {
	sync.Mutex
	// Status is what GET status answers.
	Status tailscale.Status
	// WhoIs answers whois by the address's IP; an IP absent is not a peer.
	WhoIs map[string]WhoIsAnswer
	// Down makes every request fail with 503 (tailscaled wedged).
	Down bool
	// Logins counts POST login-interactive; LoginURL is the AuthURL it
	// then sets.
	Logins   int
	LoginURL string
	// ControlURL is what GET prefs answers as the control server ("": Tailscale's).
	ControlURL string
	// Requests counts every request by path.
	Requests map[string]int

	token string
	srv   *http.Server
}

// WhoIsAnswer is one whois answer.
type WhoIsAnswer struct {
	Node   string
	UserID int64
	Login  string
	Tags   []string
	// Routes are subnet routes it advertises (PrimaryRoutes); ExitNode adds
	// 0.0.0.0/0 to its AllowedIPs.
	Routes   []string
	ExitNode bool
}

func newFake(token string) *Fake {
	f := &Fake{token: token, WhoIs: map[string]WhoIsAnswer{}, Requests: map[string]int{}}
	f.srv = &http.Server{Handler: http.HandlerFunc(f.serve), ReadHeaderTimeout: 5 * time.Second}
	return f
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if r.Host != tailscale.Host {
		http.Error(w, "bad host "+r.Host, http.StatusForbidden)
		return
	}
	if f.token != "" {
		if _, pass, ok := r.BasicAuth(); !ok || pass != f.token {
			http.Error(w, "need a token", http.StatusUnauthorized)
			return
		}
	}
	path := strings.TrimPrefix(r.URL.Path, "/localapi/v0/")
	f.Lock()
	defer f.Unlock()
	f.Requests[path]++
	if f.Down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	switch {
	case path == "status" && r.Method == http.MethodGet:
		st := f.Status
		if r.URL.Query().Get("peers") == "false" {
			st.Peer = nil
		}
		_ = json.NewEncoder(w).Encode(st)
	case path == "whois" && r.Method == http.MethodGet:
		ap, err := netip.ParseAddrPort(r.URL.Query().Get("addr"))
		if err != nil {
			http.Error(w, "bad addr", http.StatusBadRequest)
			return
		}
		a, ok := f.WhoIs[ap.Addr().Unmap().String()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		allowed := []string{ap.Addr().Unmap().String() + "/32"}
		if a.ExitNode {
			allowed = append(allowed, "0.0.0.0/0", "::/0")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Node":        map[string]any{"Name": a.Node + ".", "User": a.UserID, "Tags": a.Tags, "PrimaryRoutes": a.Routes, "AllowedIPs": allowed},
			"UserProfile": map[string]any{"ID": a.UserID, "LoginName": a.Login},
		})
	case path == "prefs" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"ControlURL": f.ControlURL})
	case path == "login-interactive" && r.Method == http.MethodPost:
		f.Logins++
		if f.LoginURL != "" {
			f.Status.AuthURL = f.LoginURL
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (f *Fake) start(t testing.TB, ln net.Listener) {
	go func() {
		if err := f.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Logf("tailscaletest: %v", err)
		}
	}()
	t.Cleanup(func() { _ = f.srv.Close() })
}

// Set changes the fake under its lock.
func (f *Fake) Set(fn func(*Fake)) {
	f.Lock()
	defer f.Unlock()
	fn(f)
}

// Count is how many requests path has had.
func (f *Fake) Count(path string) int {
	f.Lock()
	defer f.Unlock()
	return f.Requests[path]
}

func shortDir(t testing.TB) string {
	dir, err := os.MkdirTemp("", "ts") // short: macOS caps socket paths at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// StartSocket serves the fake on a unix socket, as Linux's tailscaled
// does; the path is its Locator.Socket.
func StartSocket(t testing.TB) (*Fake, string) {
	t.Helper()
	f := newFake("")
	sock := filepath.Join(shortDir(t), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f.start(t, ln)
	return f, sock
}

// StartLinuxRoot serves the fake at <root>/var/run/tailscale/tailscaled.sock:
// the Locator (GOOS linux, Root) finds it by the default path.
func StartLinuxRoot(t testing.TB) (*Fake, tailscale.Locator) {
	t.Helper()
	f := newFake("")
	root := shortDir(t)
	dir := filepath.Join(root, "var", "run", "tailscale")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "tailscaled.sock"))
	if err != nil {
		t.Skipf("socket path too long here: %v", err)
	}
	f.start(t, ln)
	return f, tailscale.Locator{GOOS: "linux", Root: root, FileOwner: rootOwned}
}

// rootOwned stands in for files and sockets root owns, as tailscaled's do.
func rootOwned(string) (int, error) { return 0, nil }

// listenerAs answers lsof's question about the fake's port as uid, cmd.
func listenerAs(uid int, cmd string) func(int) ([]tailscale.Listener, error) {
	return func(int) ([]tailscale.Listener, error) { return []tailscale.Listener{{UID: uid, Command: cmd}}, nil }
}

func tcpFake(t testing.TB, token string) (*Fake, int) {
	f := newFake(token)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.start(t, ln)
	return f, ln.Addr().(*net.TCPAddr).Port
}

// StartMacStandalone serves the fake on a localhost port with a token,
// laid out as the standalone macOS app's: <root>/Library/Tailscale/ipnport
// a symlink to the port number, sameuserproof-<port> holding the token.
func StartMacStandalone(t testing.TB, token string) (*Fake, tailscale.Locator) {
	t.Helper()
	f, port := tcpFake(t, token)
	root := shortDir(t)
	dir := filepath.Join(root, "Library", "Tailscale")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(strconv.Itoa(port), filepath.Join(dir, "ipnport")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sameuserproof-"+strconv.Itoa(port)), []byte(token+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	return f, tailscale.Locator{GOOS: "darwin", Root: root, Home: "/Users/test", Getenv: func(string) string { return "" },
		Lsof: func() ([]byte, error) { return nil, errors.New("no lsof in tests") }, FileOwner: rootOwned,
		ListenerOwner: listenerAs(0, "io.tailscale.ipn.macsys.network-extension")}
}

// StartMacAppStore serves the fake on a localhost port with a token, laid
// out as the App Store app's: a file sameuserproof-<port>-<token> in
// <root>/Users/test/Library/Group Containers/W5364U7YZB.io.tailscale.ipn.macos.
// stale adds an older file for a port nothing listens on.
func StartMacAppStore(t testing.TB, token string, stale bool) (*Fake, tailscale.Locator) {
	t.Helper()
	f, port := tcpFake(t, token)
	root := shortDir(t)
	dir := filepath.Join(root, "Users", "test", "Library", "Group Containers", "W5364U7YZB.io.tailscale.ipn.macos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if stale {
		p := filepath.Join(dir, "sameuserproof-1-oldtoken")
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		_ = os.Chtimes(p, old, old)
	}
	if err := os.WriteFile(filepath.Join(dir, "sameuserproof-"+strconv.Itoa(port)+"-"+token), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return f, tailscale.Locator{GOOS: "darwin", Root: root, Home: "/Users/test", Getenv: func(string) string { return "" },
		Lsof: func() ([]byte, error) { return nil, errors.New("no lsof in tests") }, FileOwner: rootOwned,
		ListenerOwner: listenerAs(os.Getuid(), "IPNExtension"), UID: os.Getuid}
}

// Node is a status node, for building Status.
func Node(host string, user int64, ips ...string) *tailscale.Node {
	n := &tailscale.Node{HostName: host, DNSName: host + ".tail4c2e.ts.net", UserID: user, Online: true, OS: "linux"}
	for _, ip := range ips {
		n.TailscaleIPs = append(n.TailscaleIPs, netip.MustParseAddr(ip))
	}
	return n
}

// Close stops serving (tailscaled quit, or the app restarted elsewhere).
func (f *Fake) Close() { _ = f.srv.Close() }
