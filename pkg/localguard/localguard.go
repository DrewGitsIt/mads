// Package localguard keeps a mads service's HTTP API to callers on this
// machine — and, among browser pages, to the mads app itself.
//
// The services bind to 127.0.0.1, so other machines can't connect. A web
// page in the user's browser can, though: the browser runs on this machine.
// Two browser tricks reach a loopback server from any website:
//
//   - DNS rebinding: attacker.example re-points its name at 127.0.0.1, so
//     the page's own-origin requests land on the service and the browser
//     lets the page read the answers. The only trace is the Host header,
//     which still names attacker.example — so a request must name a
//     loopback host.
//   - Blind cross-site requests: a page may send some requests anywhere
//     without asking (a text/plain POST, a form, an <img>), and the work is
//     done even though the page can't read the reply. Browsers label such
//     requests with Origin and/or Sec-Fetch-Site, so a request carrying a
//     foreign Origin, or marked cross-site without an allowed Origin, is
//     refused before any handler runs.
//
// Programs that aren't browsers (the services calling each other, the mads
// CLI, curl in an agent's shell) send neither header and are unaffected.
//
// This is "local mode" trust: the network location is the credential. A
// mads reachable from other machines would need real authentication in
// front of the same routes; this package is where that would plug in.
package localguard

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// DevServerOrigins are the Vite dev server's origins (ui/, `npm run dev`),
// whose page calls the Router cross-origin during UI development.
var DevServerOrigins = []string{"http://localhost:5173", "http://127.0.0.1:5173", "http://[::1]:5173"}

// loopbackHosts are the names a legitimate local caller puts in Host.
var loopbackHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

// Guard decides which requests a service accepts.
type Guard struct {
	origins map[string]bool
	name    string
}

// New returns a guard for a service called name (for its log lines)
// listening on port. Pages served by the service itself — its own origin in
// each loopback spelling — are allowed, plus extraOrigins. port may be
// empty, which allows only extraOrigins.
func New(name, port string, extraOrigins []string) *Guard {
	g := &Guard{origins: map[string]bool{}, name: name}
	if port != "" {
		for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
			g.origins["http://"+host+":"+port] = true
		}
	}
	for _, o := range extraOrigins {
		if n, err := NormalizeOrigin(o); err == nil {
			g.origins[n] = true
		}
	}
	return g
}

// PortOf returns the port of a listener address ("127.0.0.1:17373" → "17373").
func PortOf(addr net.Addr) string {
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return ""
	}
	return port
}

// OriginAllowed reports whether a browser page at origin may call the
// service (and so whether CORS may grant it access).
func (g *Guard) OriginAllowed(origin string) bool {
	n, err := NormalizeOrigin(origin)
	return err == nil && g.origins[n]
}

// HostAllowed reports whether a Host header names this machine's loopback
// interface, with or without a port.
func HostAllowed(host string) bool {
	if host == "" {
		return false
	}
	name := host
	if h, port, err := net.SplitHostPort(host); err == nil {
		if !isPort(port) {
			return false
		}
		name = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		name = host[1 : len(host)-1]
	}
	return loopbackHosts[strings.ToLower(name)]
}

func isPort(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Check returns why r must be refused, or "" to let it through.
func (g *Guard) Check(r *http.Request) string {
	if !HostAllowed(r.Host) {
		return fmt.Sprintf("host %q is not this machine", r.Host)
	}
	origin, hasOrigin := r.Header["Origin"]
	if hasOrigin {
		// Present but empty, "null" (a sandboxed frame or file:// page), or
		// anything not on the list: a browser page that isn't mads.
		if len(origin) != 1 || !g.OriginAllowed(origin[0]) {
			return fmt.Sprintf("origin %q is not allowed", strings.Join(origin, ","))
		}
		return ""
	}
	// No Origin: a program, a typed URL, or a browser request that leaves
	// it out (an <img>, a top-level link). Browsers mark the last kind.
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return "cross-site browser request without an allowed origin"
	}
	return ""
}

// Wrap refuses with 403 whatever Check refuses, before next sees it.
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reason := g.Check(r); reason != "" {
			log.Printf("%s: refused %s %s: %s", g.name, r.Method, r.URL.Path, reason)
			http.Error(w, "forbidden: mads only accepts requests from this machine's mads app ("+reason+")", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NormalizeOrigin returns origin as scheme://host[:port], lower-cased, with
// a default port dropped — the exact form browsers send — or an error when
// it isn't an http(s) origin.
func NormalizeOrigin(origin string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("not an http(s) origin: %q", origin)
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + host + ":" + port, nil
	}
	return scheme + "://" + host, nil
}

// ParseOrigins splits a comma-separated -allowed-origins flag value.
func ParseOrigins(csv string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(csv, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		n, err := NormalizeOrigin(part)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}
