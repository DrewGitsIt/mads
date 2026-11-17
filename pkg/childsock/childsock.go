// Package childsock is how the Router talks to the session and pipeline
// children it spawns (docs/LOCAL-FRONT-DOOR-DESIGN.md D9): over a Unix socket
// in a 0700 directory, never a TCP port, with a same-user check on every
// connection the child accepts.
//
// A child's address is a socket path ($MADS_HOME/run/<id>.sock, or a short
// fallback under /tmp when that path is too long for the OS). The Router
// keeps handling it as an opaque addr string: URL turns (addr, path) into a
// request URL and Client / Transport dial it. A host:port addr (a service run
// by hand with -addr) still works, over TCP.
package childsock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RunDir is the directory, inside $MADS_HOME, that holds child sockets.
const RunDir = "run"

// IsSocketAddr reports whether addr names a Unix socket (an absolute path)
// rather than a TCP host:port.
func IsSocketAddr(addr string) bool { return strings.HasPrefix(addr, "/") }

// maxPath is the longest socket path this OS accepts: sun_path less its NUL.
var maxPath = sunPathLen - 1

// tmpRoot is where the short fallback directory goes; a var for tests.
var tmpRoot = "/tmp"

// Dirs are the directories child sockets for madsHome can live in: the
// primary $MADS_HOME/run, then the short fallback. Neither need exist.
func Dirs(madsHome string) []string {
	return []string{filepath.Join(madsHome, RunDir), fallbackDir(madsHome)}
}

// fallbackDir is /tmp/mads-<uid>-<hash of madsHome>: short, per user and per
// install, so two installs never share sockets.
func fallbackDir(madsHome string) string {
	abs, err := filepath.Abs(madsHome)
	if err != nil {
		abs = madsHome
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(tmpRoot, fmt.Sprintf("mads-%d-%s", os.Getuid(), hex.EncodeToString(sum[:])[:10]))
}

// ensurePrivateDir makes dir (0700) or checks an existing one is a real
// directory this user owns, then tightens it to 0700. A directory another
// user owns, or a symlink — likely under a shared /tmp — is refused.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (a symlink?); remove it", dir)
	}
	if uid, ok := ownerUID(fi); ok && uid != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not you; remove it", dir, uid)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("chmod %s: %w", dir, err)
		}
	}
	return nil
}

// Path returns, and prepares the directory for, the socket child id listens
// on: $MADS_HOME/run/<id>.sock, or the /tmp fallback when that's longer than
// the OS allows. A path that won't fit even there is an error saying why.
func Path(madsHome, id string) (string, error) {
	name := id + ".sock"
	primary := filepath.Join(madsHome, RunDir, name)
	if len(primary) <= maxPath {
		if err := ensurePrivateDir(filepath.Dir(primary)); err != nil {
			return "", err
		}
		return primary, nil
	}
	fallback := filepath.Join(fallbackDir(madsHome), name)
	if len(fallback) > maxPath {
		return "", fmt.Errorf("socket path for %s is too long: %s is %d bytes and the fallback %s is %d, over this OS's limit of %d; use a shorter $MADS_HOME or child id", id, primary, len(primary), fallback, len(fallback), maxPath)
	}
	if err := ensurePrivateDir(filepath.Dir(fallback)); err != nil {
		return "", err
	}
	return fallback, nil
}

// CheckDir reports why dir can't hold this user's child sockets — the same
// rules Path applies (a real directory, not a symlink, this user's, 0700),
// without creating or changing anything — or nil when it can.
func CheckDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (a symlink?)", dir)
	}
	if uid, ok := ownerUID(fi); ok && uid != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not you", dir, uid)
	}
	if fi.Mode().Perm() != 0o700 {
		return fmt.Errorf("%s is mode %04o, not 0700", dir, fi.Mode().Perm())
	}
	return nil
}

// CheckSocket reports why path isn't a socket of this user's, or nil.
func CheckSocket(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is not a socket", path)
	}
	if uid, ok := ownerUID(fi); ok && uid != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not you", path, uid)
	}
	return nil
}

// Find returns where child id's socket is, if one exists in a directory
// that passes CheckDir: a planted fallback directory (another user's, or a
// symlink) is never trusted, even though its name is predictable.
func Find(madsHome, id string) (string, bool) {
	for _, dir := range Dirs(madsHome) {
		if CheckDir(dir) != nil {
			continue
		}
		p := filepath.Join(dir, id+".sock")
		if CheckSocket(p) == nil {
			return p, true
		}
	}
	return "", false
}

// Listen listens on the socket at path, replacing a stale socket left by a
// dead child (never any other kind of file), mode 0600, and wraps it in the
// same-user check.
func Listen(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket; not replacing it", path)
		}
		if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("%s is in use by a live process", path)
		}
		_ = os.Remove(path)
	}
	if len(path) > maxPath {
		return nil, fmt.Errorf("socket path %s is %d bytes, over this OS's limit of %d", path, len(path), maxPath)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return SameUser(ln.(*net.UnixListener), PeerUID), nil
}

// PeerUID returns the uid of the process at the other end of c
// (SO_PEERCRED on Linux, LOCAL_PEERCRED — what getpeereid(3) reads — on
// macOS).
var PeerUID = peerUID

// SameUser wraps ln so Accept only returns connections from this process's
// own uid, as peer reports it; anything else is closed and logged.
func SameUser(ln *net.UnixListener, peer func(*net.UnixConn) (int, error)) net.Listener {
	return &sameUser{UnixListener: ln, peer: peer, uid: os.Getuid()}
}

type sameUser struct {
	*net.UnixListener
	peer func(*net.UnixConn) (int, error)
	uid  int
}

func (l *sameUser) Accept() (net.Conn, error) {
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, err := l.peer(c)
		if err == nil && uid == l.uid {
			return c, nil
		}
		if err != nil {
			log.Printf("childsock: refused a connection: can't read its peer credentials: %v", err)
		} else {
			log.Printf("childsock: refused a connection from uid %d (this process runs as uid %d)", uid, l.uid)
		}
		_ = c.Close()
	}
}

// hosts maps the synthetic host names URL hands out back to socket paths.
var hosts sync.Map // host -> socket path

const hostSuffix = ".sock.mads"

// host is the stable, unique URL host for a socket path, so the Transport's
// connection pool never mixes two children's connections.
func host(path string) string {
	sum := sha256.Sum256([]byte(path))
	h := hex.EncodeToString(sum[:])[:16] + hostSuffix
	hosts.Store(h, path)
	return h
}

// Host is the URL host for addr: a synthetic name for a socket path, addr
// itself for host:port.
func Host(addr string) string {
	if IsSocketAddr(addr) {
		return host(addr)
	}
	return addr
}

// URL is the request URL for path (with any query) on the child at addr.
func URL(addr, path string) string {
	return "http://" + Host(addr) + path
}

// Transport dials socket addrs (by the host URL gave them) and anything
// else over TCP, as http.DefaultTransport would.
var Transport http.RoundTripper = newTransport()

func newTransport() *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		h, _, err := net.SplitHostPort(address)
		if err == nil && strings.HasSuffix(h, hostSuffix) {
			p, ok := hosts.Load(h)
			if !ok {
				return nil, errors.New("childsock: unknown child socket host " + h)
			}
			return dialer.DialContext(ctx, "unix", p.(string))
		}
		return dialer.DialContext(ctx, network, address)
	}
	base.Proxy = nil
	return base
}

// Client is http.DefaultClient for children: no timeout (streams live as
// long as their context), the socket-aware Transport.
var Client = &http.Client{Transport: Transport}
