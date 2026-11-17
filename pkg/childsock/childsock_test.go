package childsock

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortDir is a temp dir short enough for a socket path on macOS
// (t.TempDir under /var/folders often isn't).
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "cs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func TestPathUsesAPrivateRunDir(t *testing.T) {
	home := shortDir(t)
	p, err := Path(home, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(home, "run", "ses_1.sock") {
		t.Fatalf("Path = %s", p)
	}
	fi, err := os.Stat(filepath.Dir(p))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("run dir: %v %v", fi.Mode(), err)
	}
	// An existing looser run dir is tightened.
	if err := os.Chmod(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Path(home, "ses_1"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Dir(p)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("run dir not tightened: %v", fi.Mode())
	}
}

func TestPathFallsBackWhenTooLong(t *testing.T) {
	oldMax, oldTmp := maxPath, tmpRoot
	t.Cleanup(func() { maxPath, tmpRoot = oldMax, oldTmp })
	tmpRoot = shortDir(t)
	home := filepath.Join(shortDir(t), strings.Repeat("h", 60))
	maxPath = len(filepath.Join(home, "run", "ses_1.sock")) - 1 // the primary just misses

	p, err := Path(home, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, tmpRoot+string(os.PathSeparator)+fmt.Sprintf("mads-%d-", os.Getuid())) || len(p) > maxPath {
		t.Fatalf("fallback path %s (limit %d)", p, maxPath)
	}
	fi, err := os.Lstat(filepath.Dir(p))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("fallback dir: %v %v", fi, err)
	}
	// Stable per install, distinct between installs.
	again, _ := Path(home, "ses_1")
	other, _ := Path(home+"x", "ses_1")
	if again != p || other == p {
		t.Fatalf("fallback dirs: %s %s %s", p, again, other)
	}
	if !strings.Contains(strings.Join(Dirs(home), " "), filepath.Dir(p)) {
		t.Fatal("Dirs doesn't list the fallback")
	}

	// Too long even there: a clear error, not a bind failure.
	maxPath = 10
	_, err = Path(home, "ses_1")
	if err == nil || !strings.Contains(err.Error(), "too long") || !strings.Contains(err.Error(), "limit of 10") {
		t.Fatalf("err = %v", err)
	}
}

func TestFallbackDirRefusesASymlink(t *testing.T) {
	oldMax, oldTmp := maxPath, tmpRoot
	t.Cleanup(func() { maxPath, tmpRoot = oldMax, oldTmp })
	tmpRoot = shortDir(t)
	home := filepath.Join(shortDir(t), strings.Repeat("h", 60))
	maxPath = 60
	if err := os.Symlink(shortDir(t), fallbackDir(home)); err != nil {
		t.Fatal(err)
	}
	if _, err := Path(home, "s"); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("a symlinked fallback dir was accepted: %v", err)
	}
}

func TestTheRealLimitIsEnforcedClearly(t *testing.T) {
	long := filepath.Join(shortDir(t), strings.Repeat("x", sunPathLen))
	if _, err := Listen(long); err == nil || !strings.Contains(err.Error(), "over this OS's limit") {
		t.Fatalf("Listen on an overlong path: %v", err)
	}
}

func serve(t *testing.T, ln net.Listener, h http.Handler) {
	t.Helper()
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func TestSameUserCheck(t *testing.T) {
	sock := filepath.Join(shortDir(t), "c.sock")
	raw, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	// An injected peer source: the first connection claims another uid.
	calls := 0
	ln := SameUser(raw.(*net.UnixListener), func(*net.UnixConn) (int, error) {
		calls++
		if calls == 1 {
			return os.Getuid() + 1, nil
		}
		return os.Getuid(), nil
	})
	serve(t, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))

	// The "other user" is closed without an answer.
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := c.Read(make([]byte, 64)); n != 0 || err == nil {
		t.Fatalf("another uid got an answer: n=%d err=%v", n, err)
	}
	_ = c.Close()

	// The same user is served.
	resp, err := Client.Get(URL(sock, "/"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("same uid: %q", body)
	}
}

func TestRealPeerCredentialsAreThisUser(t *testing.T) {
	sock := filepath.Join(shortDir(t), "c.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(sock); fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", fi.Mode())
	}
	got := make(chan int, 1)
	serve(t, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	// PeerUID on a real connection from this process.
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	go func() {
		uid, err := peerUID(c.(*net.UnixConn))
		if err != nil {
			t.Error(err)
		}
		got <- uid
	}()
	if uid := <-got; uid != os.Getuid() {
		t.Fatalf("peerUID = %d, want %d", uid, os.Getuid())
	}
	resp, err := Client.Get(URL(sock, "/x"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func TestListenReplacesOnlyStaleSockets(t *testing.T) {
	dir := shortDir(t)
	file := filepath.Join(dir, "f.sock")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(file); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
	live := filepath.Join(dir, "live.sock")
	ln, err := Listen(live)
	if err != nil {
		t.Fatal(err)
	}
	serve(t, ln, http.NotFoundHandler())
	if _, err := Listen(live); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("Listen took over a live socket: %v", err)
	}
	stale := filepath.Join(dir, "stale.sock")
	l2, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	l2.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l2.Close()
	l3, err := Listen(stale)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	_ = l3.Close()
	if p, ok := Find(filepath.Dir(dir), "x"); ok {
		t.Fatalf("Find invented %s", p)
	}
}

// TestProxyStreamsAndUpgradesOverTheSocket: the Router's reverse proxy over
// the socket transport passes SSE as it's written and a WebSocket-style
// upgrade both ways (D9).
func TestProxyStreamsAndUpgradesOverTheSocket(t *testing.T) {
	sock := filepath.Join(shortDir(t), "c.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	serve(t, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/output":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: one\n\n")
			w.(http.Flusher).Flush()
			<-release
			_, _ = io.WriteString(w, "data: two\n\n")
		case "/ws":
			if r.Header.Get("Upgrade") != "websocket" {
				http.Error(w, "no upgrade", 400)
				return
			}
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			_ = rw.Flush()
			line, _ := rw.ReadString('\n')
			_, _ = rw.WriteString("echo:" + line)
			_ = rw.Flush()
		}
	}))
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: Host(sock)})
	proxy.Transport = Transport
	front := httptest.NewServer(proxy)
	defer front.Close()

	resp, err := http.Get(front.URL + "/output")
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(resp.Body)
	if line, _ := br.ReadString('\n'); line != "data: one\n" {
		t.Fatalf("first SSE frame %q (not flushed through?)", line)
	}
	close(release)
	_, _ = br.ReadString('\n')
	if line, _ := br.ReadString('\n'); line != "data: two\n" {
		t.Fatalf("second SSE frame %q", line)
	}
	_ = resp.Body.Close()

	c, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, _ = io.WriteString(c, "GET /ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	cr := bufio.NewReader(c)
	status, _ := cr.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("upgrade status %q", status)
	}
	for {
		l, err := cr.ReadString('\n')
		if err != nil || l == "\r\n" {
			break
		}
	}
	_, _ = io.WriteString(c, "hello\n")
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if got, _ := cr.ReadString('\n'); got != "echo:hello\n" {
		t.Fatalf("upgraded stream echoed %q", got)
	}
}

func TestURLAndHost(t *testing.T) {
	if URL("127.0.0.1:5", "/a?b=1") != "http://127.0.0.1:5/a?b=1" {
		t.Fatal("TCP addr URL changed")
	}
	a, b := Host("/tmp/a.sock"), Host("/tmp/b.sock")
	if a == b || !strings.HasSuffix(a, hostSuffix) || Host("/tmp/a.sock") != a {
		t.Fatalf("hosts %s %s", a, b)
	}
}
