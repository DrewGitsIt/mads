// Package tailscale is mads's view of tailscaled's LocalAPI: who this node
// is, who a caller is (WhoIs), the tailnet's peers, this node's key expiry
// and sign-in state (docs/MESH-NETWORK-DESIGN.md, "Tailscale helpers").
// It's a helper: mads works without it, and nothing here is load-bearing
// for who gets in (that's the pinned device key).
//
// It speaks the LocalAPI with plain net/http rather than importing
// tailscale.com: mads needs a handful of read-only endpoints, and the
// dependency would dwarf the rest of mads.
//
// # Finding the LocalAPI
//
// tailscaled exposes its LocalAPI differently per platform and package.
// Locate mirrors how tailscale's own client finds it (tailscale.com's
// safesocket package, LocalTCPPortAndToken and DefaultTailscaledSocket),
// in the same order:
//
//   - Linux: a unix socket, /var/run/tailscale/tailscaled.sock (else
//     /run/tailscale/tailscaled.sock). World-writable (0666): no root.
//   - macOS, the Tailscale app. The app's network extension serves the
//     LocalAPI on a TCP port on localhost, protected by a token that only
//     this user (or an admin) can read: the "sameuserproof".
//   - TS_MACOS_CLI_SHARED_DIR (set by the app for its bundled CLI): a
//     file named sameuserproof-<port>-<token> in that directory.
//   - The standalone app (downloaded from tailscale.com, "macsys"): the
//     symlink /Library/Tailscale/ipnport points at the port number (its
//     target is the port, e.g. "61234"), and the file
//     /Library/Tailscale/sameuserproof-<port> holds the token (root:admin,
//     0640, so an admin user reads it).
//   - The Mac App Store app (sandboxed): a file named
//     sameuserproof-<port>-<token> in the app's group container,
//     ~/Library/Group Containers/<team>.io.tailscale.ipn.macos/ (team
//     W5364U7YZB). tailscale's CLI finds it with `lsof -c IPNExtension`
//     (the extension holds it open); Locate reads the directory first and
//     falls back to lsof.
//   - Then the open-source tailscaled run as a daemon (Homebrew, `sudo
//     tailscaled`): the unix socket /var/run/tailscaled.socket.
//
// With a TCP endpoint every request carries the token as HTTP basic auth
// (an empty user name, the token as the password), as tailscale's client
// sends it; with a socket the OS's peer credentials do that job. Every
// request's Host is "local-tailscaled.sock", which the LocalAPI checks
// (a DNS-rebinding defence). The port changes when the app restarts, so a
// client that can't connect locates again.
package tailscale

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Endpoint is one place a LocalAPI may answer.
type Endpoint struct {
	// Network is "unix" or "tcp".
	Network string `json:"network"`
	// Address is the socket's path, or 127.0.0.1:<port>.
	Address string `json:"address"`
	// Token is the TCP endpoint's sameuserproof, sent as basic auth.
	// Never logged or reported.
	Token string `json:"-"`
	// Variant says which tailscaled this is: "socket" (Linux, or the
	// open-source daemon on macOS, or a -localapi-socket flag),
	// "macos-cli-shared", "macos-standalone" or "macos-app-store".
	Variant string `json:"variant"`
}

// String describes e without its token.
func (e Endpoint) String() string { return e.Variant + " " + e.Network + ":" + e.Address }

// Linux and macOS socket paths (tailscale.com/paths.DefaultTailscaledSocket).
const (
	LinuxSocket    = "/var/run/tailscale/tailscaled.sock"
	linuxSocketAlt = "/run/tailscale/tailscaled.sock"
	DarwinSocket   = "/var/run/tailscaled.socket"
	// macsysDir is the standalone macOS app's state directory.
	macsysDir = "/Library/Tailscale"
	// appStoreGroup is the App Store app's group container's name, after
	// its team prefix.
	appStoreGroup = ".io.tailscale.ipn.macos"
	appStoreTeam  = "W5364U7YZB"
)

// Locator finds tailscaled's LocalAPI. Its file-system and process hooks
// are seams for tests (a fake macOS layout); the zero value reads the real
// machine.
type Locator struct {
	// GOOS is the platform to locate for (default runtime.GOOS).
	GOOS string
	// Socket, when set, is the only endpoint (the -localapi-socket flag).
	Socket string
	// Home is this user's home directory (default os.UserHomeDir).
	Home string
	// Root prefixes every absolute path read (tests: a fake file system).
	Root string

	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	Readlink func(string) (string, error)
	ReadDir  func(string) ([]os.DirEntry, error)
	Stat     func(string) (os.FileInfo, error)
	// Lsof lists the files the App Store app's network extension holds
	// open (`/usr/sbin/lsof -n -a -u <uid> -c IPNExtension -F n`).
	Lsof func() ([]byte, error)
	// FileOwner is a path's owner (not following a symlink).
	FileOwner func(path string) (int, error)
	// ListenerOwner is every process listening on port: each one's uid
	// and command. By default from `/usr/sbin/netstat -anv -p tcp` (it
	// names every listener's pid without root, where an unprivileged lsof
	// can't see root's sockets) and `/bin/ps -o pid=,uid=,comm= -p ...`.
	ListenerOwner func(port int) ([]Listener, error)
	// UID is this process's user (default os.Getuid).
	UID func() int
}

// lsofPath is macOS's lsof: never one found on $PATH.
const lsofPath = "/usr/sbin/lsof"

// netstatPath and psPath are macOS's, never ones found on $PATH.
const (
	netstatPath = "/usr/sbin/netstat"
	psPath      = "/bin/ps"
)

func (l Locator) defaults() Locator {
	if l.GOOS == "" {
		l.GOOS = runtime.GOOS
	}
	if l.Getenv == nil {
		l.Getenv = os.Getenv
	}
	if l.ReadFile == nil {
		l.ReadFile = os.ReadFile
	}
	if l.Readlink == nil {
		l.Readlink = os.Readlink
	}
	if l.ReadDir == nil {
		l.ReadDir = os.ReadDir
	}
	if l.Stat == nil {
		l.Stat = os.Stat
	}
	if l.Home == "" {
		l.Home, _ = os.UserHomeDir()
	}
	if l.Lsof == nil {
		l.Lsof = func() ([]byte, error) {
			return exec.Command(lsofPath, "-n", "-a", "-u", strconv.Itoa(os.Getuid()), "-c", "IPNExtension", "-F", "n").Output()
		}
	}
	if l.FileOwner == nil {
		l.FileOwner = fileOwner
	}
	if l.ListenerOwner == nil {
		l.ListenerOwner = func(port int) ([]Listener, error) {
			return listenersOn(port,
				func() ([]byte, error) { return exec.Command(netstatPath, "-anv", "-p", "tcp").Output() },
				func(pids string) ([]byte, error) {
					return exec.Command(psPath, "-o", "pid=,uid=,comm=", "-p", pids).Output()
				})
		}
	}
	if l.UID == nil {
		l.UID = os.Getuid
	}
	return l
}

// Listener is a process listening on a port.
type Listener struct {
	PID     int
	UID     int
	Command string
}

// listenersOn is every process listening on TCP port: the pids netstat
// names, each one's uid and command from ps (final-pass review: lsof run
// unprivileged misses root's sockets, so the standalone app's root network
// extension read as "nothing listens").
func listenersOn(port int, netstat func() ([]byte, error), ps func(pids string) ([]byte, error)) ([]Listener, error) {
	out, err := netstat()
	if err != nil {
		return nil, fmt.Errorf("netstat: %w", err)
	}
	pids, err := parseNetstat(out, port)
	if err != nil {
		return nil, err
	}
	if len(pids) == 0 {
		return nil, errors.New("nothing listens there")
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	psOut, err := ps(strings.Join(list, ","))
	if err != nil && len(psOut) == 0 {
		return nil, fmt.Errorf("ps: %w", err)
	}
	byPID := parsePS(psOut)
	ls := make([]Listener, 0, len(pids))
	for _, p := range pids {
		li, ok := byPID[p]
		if !ok {
			// Gone between the two reads, or hidden: not known to be
			// tailscaled's.
			li = Listener{PID: p, UID: -1}
		}
		ls = append(ls, li)
	}
	return ls, nil
}

// parseNetstat reads `netstat -anv -p tcp`: the pids of the sockets in
// LISTEN on port, on any local address. Newer macOS prints a
// "process:pid" column, older ones a "pid" column; both are read from the
// header, never by fixed position.
func parseNetstat(out []byte, port int) ([]int, error) {
	suffix := "." + strconv.Itoa(port)
	pidCol := -1
	var pids []int
	seen := map[int]bool{}
	header := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if f[0] == "Proto" {
			header = true
			// "Local Address" and "Foreign Address" are two words each in
			// the header, one field in a row.
			for i, h := range f {
				if h == "pid" {
					pidCol = i - 2
				}
			}
			continue
		}
		if !strings.HasPrefix(f[0], "tcp") || len(f) < 6 || f[5] != "LISTEN" || !strings.HasSuffix(f[3], suffix) {
			continue
		}
		pid := -1
		for _, tok := range f[6:] {
			// "process:pid" (a name may hold spaces: the pid ends the token).
			if i := strings.LastIndexByte(tok, ':'); i > 0 {
				if n, err := strconv.Atoi(tok[i+1:]); err == nil && n > 0 {
					pid = n
					break
				}
			}
		}
		if pid < 0 && pidCol > 5 && pidCol < len(f) {
			if n, err := strconv.Atoi(f[pidCol]); err == nil && n > 0 {
				pid = n
			}
		}
		if pid < 0 {
			return nil, errors.New("netstat named no pid for a listener on port " + strconv.Itoa(port))
		}
		if !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	if !header {
		return nil, errors.New("netstat: unrecognised output")
	}
	return pids, nil
}

// parsePS reads `ps -o pid=,uid=,comm=`: each pid's uid and command (a
// command may hold spaces).
func parsePS(out []byte) map[int]Listener {
	m := map[int]Listener{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		uid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		m[pid] = Listener{PID: pid, UID: uid, Command: strings.Join(f[2:], " ")}
	}
	return m
}

// ErrUntrusted is a LocalAPI endpoint whose owner isn't tailscaled's: a
// stale port another local user took, or a file it planted.
var ErrUntrusted = errors.New("not tailscaled's: refusing it")

// Verify checks a TCP endpoint is tailscaled's before its token is sent
// (round-1 review S2): its listener runs as root (the standalone app's
// network extension) or as this user under the App Store app's
// IPNExtension. A socket's directory and owner protect it already.
func (l Locator) Verify(e Endpoint) error {
	l = l.defaults()
	if e.Network != "tcp" {
		return nil
	}
	_, portS, err := net.SplitHostPort(e.Address)
	if err != nil {
		return err
	}
	port, _ := strconv.Atoi(portS)
	ls, err := l.ListenerOwner(port)
	if err == nil && len(ls) == 0 {
		err = errors.New("nothing listens there")
	}
	if err != nil {
		return fmt.Errorf("%w (who listens on %d: %v)", ErrUntrusted, port, err)
	}
	// Every listener must be tailscaled's: another process sharing the
	// port (SO_REUSEPORT) could take the connection (round-2 R8).
	for _, li := range ls {
		switch {
		case li.UID == 0:
			continue
		case li.UID == l.UID() && (strings.Contains(li.Command, "IPNExtension") || strings.Contains(strings.ToLower(li.Command), "tailscale")) && e.Variant != "macos-standalone":
			continue
		}
		return fmt.Errorf("%w (port %d is held by %q, uid %d)", ErrUntrusted, port, li.Command, li.UID)
	}
	return nil
}

func (l Locator) path(p string) string {
	if l.Root == "" {
		return p
	}
	return filepath.Join(l.Root, p)
}

// ErrNotFound is no LocalAPI found on this machine: Tailscale isn't
// installed, or isn't running.
var ErrNotFound = errors.New("tailscaled's LocalAPI wasn't found (is Tailscale installed and running?)")

// Locate lists the LocalAPI endpoints found, best first. None is
// ErrNotFound.
func (l Locator) Locate() ([]Endpoint, error) {
	l = l.defaults()
	if l.Socket != "" {
		return []Endpoint{{Network: "unix", Address: l.Socket, Variant: "socket"}}, nil
	}
	var out []Endpoint
	switch l.GOOS {
	case "darwin":
		if dir := l.Getenv("TS_MACOS_CLI_SHARED_DIR"); dir != "" {
			out = append(out, l.proofFiles(dir, "macos-cli-shared")...)
		}
		if e, ok := l.macsys(); ok {
			out = append(out, e)
		}
		out = append(out, l.appStore()...)
		if l.isSocket(DarwinSocket) {
			out = append(out, Endpoint{Network: "unix", Address: l.path(DarwinSocket), Variant: "socket"})
		}
	default:
		for _, p := range []string{LinuxSocket, linuxSocketAlt} {
			if l.isSocket(p) {
				out = append(out, Endpoint{Network: "unix", Address: l.path(p), Variant: "socket"})
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

// isSocket reports whether p is a socket owned by root (tailscaled's).
func (l Locator) isSocket(p string) bool {
	st, err := l.Stat(l.path(p))
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		return false
	}
	uid, err := l.FileOwner(l.path(p))
	return err == nil && uid == 0
}

// macsys is the standalone app's endpoint: the port is ipnport's target,
// the token sameuserproof-<port>'s contents.
func (l Locator) macsys() (Endpoint, bool) {
	link := l.path(filepath.Join(macsysDir, "ipnport"))
	// Root's, as the app writes them (S2): a file planted by someone else
	// is no endpoint.
	if uid, err := l.FileOwner(link); err != nil || uid != 0 {
		return Endpoint{}, false
	}
	target, err := l.Readlink(link)
	if err != nil {
		return Endpoint{}, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(target))
	if err != nil || port <= 0 || port > 65535 {
		return Endpoint{}, false
	}
	proof := l.path(filepath.Join(macsysDir, "sameuserproof-"+strconv.Itoa(port)))
	if uid, err := l.FileOwner(proof); err != nil || uid != 0 {
		return Endpoint{}, false
	}
	raw, err := l.ReadFile(proof)
	if err != nil {
		return Endpoint{}, false
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return Endpoint{}, false
	}
	return Endpoint{Network: "tcp", Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Token: token, Variant: "macos-standalone"}, true
}

// appStore is the App Store app's endpoints: sameuserproof-<port>-<token>
// in its group container, newest first, else what lsof shows the network
// extension holding open.
func (l Locator) appStore() []Endpoint {
	var out []Endpoint
	groups := filepath.Join(l.Home, "Library", "Group Containers")
	entries, _ := l.ReadDir(l.path(groups))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), appStoreGroup) {
			out = append(out, l.proofFiles(filepath.Join(groups, e.Name()), "macos-app-store")...)
		}
	}
	if len(out) > 0 {
		return out
	}
	raw, err := l.Lsof()
	if err != nil {
		return nil
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	marker := []byte(appStoreGroup + "/sameuserproof-")
	for sc.Scan() {
		line := sc.Bytes()
		i := bytes.Index(line, marker)
		if i < 0 {
			continue
		}
		if e, ok := proofName(string(line[i+len(marker):]), "macos-app-store"); ok {
			out = append(out, e)
		}
	}
	return out
}

// proofFiles reads a directory's sameuserproof-<port>-<token> files,
// newest first (an app restart leaves the old one behind until cleaned).
func (l Locator) proofFiles(dir, variant string) []Endpoint {
	entries, err := l.ReadDir(l.path(dir))
	if err != nil {
		return nil
	}
	type found struct {
		e   Endpoint
		mod int64
	}
	var fs []found
	for _, de := range entries {
		rest, ok := strings.CutPrefix(de.Name(), "sameuserproof-")
		if !ok {
			continue
		}
		e, ok := proofName(rest, variant)
		if !ok {
			continue
		}
		var mod int64
		if info, err := de.Info(); err == nil {
			mod = info.ModTime().UnixNano()
		}
		fs = append(fs, found{e, mod})
	}
	slices.SortStableFunc(fs, func(a, b found) int {
		switch {
		case a.mod > b.mod:
			return -1
		case a.mod < b.mod:
			return 1
		}
		return 0
	})
	out := make([]Endpoint, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.e)
	}
	return out
}

// proofName parses "<port>-<token>".
func proofName(s, variant string) (Endpoint, bool) {
	portS, token, ok := strings.Cut(strings.TrimSpace(s), "-")
	port, err := strconv.Atoi(portS)
	if !ok || err != nil || port <= 0 || port > 65535 || token == "" || strings.ContainsAny(token, "/ \t") {
		return Endpoint{}, false
	}
	return Endpoint{Network: "tcp", Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Token: token, Variant: variant}, true
}
