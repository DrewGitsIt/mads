package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Host is the name every LocalAPI request carries: the LocalAPI refuses
// others (DNS-rebinding protection).
const Host = "local-tailscaled.sock"

const base = "http://" + Host + "/localapi/v0/"

// MeshCap is the tailnet app capability earlier builds required. mads no
// longer reads it for admission (docs/MESH-NETWORK-DESIGN.md, "The WhoIs
// change"); it's parsed only so a WhoIs answer that carries it still
// decodes as before.
const MeshCap = "github.com/drewgitsit/mads/cap/mesh"

// Client talks to tailscaled's LocalAPI wherever Locate finds it, finding
// it again when it stops answering (the macOS app's port changes on a
// restart).
type Client struct {
	loc  Locator
	http *http.Client

	mu  sync.Mutex
	cur *Endpoint
	// verified is when each TCP endpoint's listener was last checked as
	// tailscaled's (re-checked every verifyEvery).
	verified map[Endpoint]time.Time
	// lastErr is why the LocalAPI isn't reachable now, for status.
	lastErr error
}

// verifyEvery is how often a TCP endpoint's listener is checked again: a
// port tailscaled gave up (it restarted elsewhere) may be someone else's.
const verifyEvery = 10 * time.Second

// trusted checks e (S2), at most every verifyEvery.
func (c *Client) trusted(e Endpoint) error {
	if e.Network != "tcp" {
		return nil
	}
	c.mu.Lock()
	at, ok := c.verified[e]
	c.mu.Unlock()
	if ok && time.Since(at) < verifyEvery {
		return nil
	}
	if err := c.loc.Verify(e); err != nil {
		return err
	}
	c.mu.Lock()
	if c.verified == nil {
		c.verified = map[Endpoint]time.Time{}
	}
	c.verified[e] = time.Now()
	c.mu.Unlock()
	return nil
}

// forget drops e's verification.
func (c *Client) forget(e Endpoint) {
	c.mu.Lock()
	delete(c.verified, e)
	c.mu.Unlock()
}

// New is a Client for whatever Locate finds.
func New(loc Locator) *Client {
	c := &Client{loc: loc}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	c.http = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true, // the endpoint can change between calls
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				e, ok := endpointFrom(ctx)
				if !ok {
					return nil, ErrNotFound
				}
				return dialer.DialContext(ctx, e.Network, e.Address)
			},
		},
	}
	return c
}

// NewSocket is a Client for one unix socket (a -localapi-socket flag, or
// a test's fake).
func NewSocket(path string) *Client { return New(Locator{Socket: path}) }

type endpointKey struct{}

func endpointFrom(ctx context.Context) (Endpoint, bool) {
	e, ok := ctx.Value(endpointKey{}).(Endpoint)
	return e, ok
}

// Endpoint is where the LocalAPI last answered, if it has.
func (c *Client) Endpoint() (Endpoint, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur == nil {
		return Endpoint{}, false
	}
	return *c.cur, true
}

// ErrNotPeer is WhoIs on an address that isn't a tailnet peer.
var ErrNotPeer = errors.New("not a tailnet peer")

// StatusError is a LocalAPI answer that wasn't 200.
type StatusError struct {
	Path   string
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("tailscaled localapi %s: status %d", e.Path, e.Status)
}

// do sends one request, trying the current endpoint and then whatever
// Locate finds now, until one answers.
func (c *Client) do(ctx context.Context, method, path string, out any) error {
	c.mu.Lock()
	var tries []Endpoint
	if c.cur != nil {
		tries = append(tries, *c.cur)
	}
	c.mu.Unlock()
	located := false
	var lastErr error
	for i := 0; ; i++ {
		if i >= len(tries) {
			if located {
				break
			}
			located = true
			found, err := c.loc.Locate()
			if err != nil {
				lastErr = err
				break
			}
			for _, e := range found {
				if len(tries) == 0 || e != tries[0] {
					tries = append(tries, e)
				}
			}
			if i >= len(tries) {
				break
			}
		}
		e := tries[i]
		if err := c.trusted(e); err != nil {
			lastErr = err
			continue
		}
		err := c.doAt(ctx, e, method, path, out)
		if err != nil {
			// Any failure and the port is checked again before its token is
			// next sent (round-2 R8): tailscaled may have gone and someone
			// else taken it within verifyEvery.
			c.forget(e)
		}
		var se *StatusError
		if errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) {
			// A token refused: never try it, or another, elsewhere (S2).
			lastErr = err
			break
		}
		if err == nil || errors.Is(err, ErrNotPeer) || errors.As(err, &se) {
			// It answered (an answer we don't like is still an answer).
			c.mu.Lock()
			ec := e
			c.cur, c.lastErr = &ec, nil
			c.mu.Unlock()
			return err
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	c.mu.Lock()
	c.cur, c.lastErr = nil, lastErr
	c.mu.Unlock()
	return lastErr
}

func (c *Client) doAt(ctx context.Context, e Endpoint, method, path string, out any) error {
	req, err := http.NewRequestWithContext(context.WithValue(ctx, endpointKey{}, e), method, base+path, nil)
	if err != nil {
		return err
	}
	req.Host = Host
	if e.Network == "tcp" {
		req.SetBasicAuth("", e.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tailscaled localapi (%s): %w", e.Variant, err)
	}
	defer func() { _ = resp.Body.Close() }()
	name := strings.SplitN(path, "?", 2)[0]
	switch {
	case resp.StatusCode == http.StatusNotFound && name == "whois":
		return ErrNotPeer
	case resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent:
		return &StatusError{Path: name, Status: resp.StatusCode}
	case out == nil:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

// --- Status -------------------------------------------------------------------

// Node is one tailnet node, as status reports it.
type Node struct {
	HostName     string       `json:"HostName"`
	DNSName      string       `json:"DNSName"`
	OS           string       `json:"OS"`
	UserID       int64        `json:"UserID"`
	TailscaleIPs []netip.Addr `json:"TailscaleIPs"`
	Tags         []string     `json:"Tags"`
	Online       bool         `json:"Online"`
	// KeyExpiry is when this node's key expires; nil when expiry is off
	// (disabled in the admin console, or a tagged node).
	KeyExpiry *time.Time `json:"KeyExpiry"`
	Expired   bool       `json:"Expired"`
}

// Status is the subset of tailscaled's ipnstate.Status mads reads.
type Status struct {
	// BackendState: "Running", "NeedsLogin", "NeedsMachineAuth",
	// "Stopped", "Starting", "NoState".
	BackendState string `json:"BackendState"`
	// AuthURL is where to sign in, while one is pending (after a login
	// was started: StartLogin).
	AuthURL string          `json:"AuthURL"`
	Self    *Node           `json:"Self"`
	Peer    map[string]Node `json:"Peer"`
	// CurrentTailnet names the tailnet.
	CurrentTailnet *struct {
		Name           string `json:"Name"`
		MagicDNSSuffix string `json:"MagicDNSSuffix"`
	} `json:"CurrentTailnet"`
}

// NeedsLogin reports whether this node must sign in again (logged out, or
// its key expired).
func (s Status) NeedsLogin() bool {
	return s.BackendState == "NeedsLogin" || (s.Self != nil && s.Self.Expired)
}

// Peers is every other node, sorted by name.
func (s Status) Peers() []Node {
	out := make([]Node, 0, len(s.Peer))
	for _, n := range s.Peer {
		out = append(out, n)
	}
	sortNodes(out)
	return out
}

// Status reads this node's status, with its peers when peers is set.
func (c *Client) Status(ctx context.Context, peers bool) (Status, error) {
	var st Status
	path := "status?peers=false"
	if peers {
		path = "status"
	}
	if err := c.do(ctx, http.MethodGet, path, &st); err != nil {
		return Status{}, err
	}
	if st.Self != nil {
		st.Self.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
	}
	for k, n := range st.Peer {
		n.DNSName = strings.TrimSuffix(n.DNSName, ".")
		st.Peer[k] = n
	}
	return st, nil
}

// Self is this node's identity and tailnet IPv4.
type Self struct {
	HostName string
	DNSName  string
	UserID   int64
	IPv4     string
	IPs      []netip.Addr
	// KeyExpiry is this node's key expiry; nil: off.
	KeyExpiry *time.Time
}

// Self reads this node: it must be logged in (an IPv4 on the tailnet).
func (c *Client) Self(ctx context.Context) (Self, error) {
	st, err := c.Status(ctx, false)
	if err != nil {
		return Self{}, err
	}
	if st.Self == nil {
		return Self{}, errors.New("tailscaled reports no node for this machine (is it logged in?)")
	}
	s := Self{HostName: st.Self.HostName, DNSName: st.Self.DNSName, UserID: st.Self.UserID, IPs: st.Self.TailscaleIPs, KeyExpiry: st.Self.KeyExpiry}
	for _, ip := range st.Self.TailscaleIPs {
		if ip.Is4() {
			s.IPv4 = ip.String()
			break
		}
	}
	if s.IPv4 == "" {
		return s, errors.New("tailscaled reports no tailnet IPv4 for this node (is it logged in?)")
	}
	return s, nil
}

// ControlURL is the control server this node signs in with (prefs'
// ControlURL; "" is Tailscale's own): where a sign-in link may point.
func (c *Client) ControlURL(ctx context.Context) (string, error) {
	var p struct {
		ControlURL string `json:"ControlURL"`
	}
	if err := c.do(ctx, http.MethodGet, "prefs", &p); err != nil {
		return "", err
	}
	return p.ControlURL, nil
}

// StartLogin asks tailscaled to start an interactive sign-in; its AuthURL
// then shows in Status (it may take a moment).
func (c *Client) StartLogin(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "login-interactive", nil)
}

// --- WhoIs --------------------------------------------------------------------

// Caller is the tailnet identity behind a remote address.
type Caller struct {
	NodeName  string // MagicDNS name, without the trailing dot
	UserID    int64
	LoginName string
	Tags      []string
	// Access and HasCap are what the (no longer used) mesh capability
	// said, kept only so a caller can log it.
	Access []string
	HasCap bool
	// Routes: the node is a subnet router or an exit node (it carries
	// traffic for others), so its WhoIs answer doesn't say which device
	// is on the other end.
	Routes bool
}

type whoisResponse struct {
	Node struct {
		Name          string
		User          int64
		Tags          []string
		PrimaryRoutes []netip.Prefix
		AllowedIPs    []netip.Prefix
	}
	UserProfile struct {
		ID        int64
		LoginName string
	}
	CapMap map[string][]json.RawMessage
}

// WhoIs identifies the tailnet node behind remoteAddr ("ip:port");
// ErrNotPeer when it isn't one.
func (c *Client) WhoIs(ctx context.Context, remoteAddr string) (Caller, error) {
	var w whoisResponse
	if err := c.do(ctx, http.MethodGet, "whois?addr="+url.QueryEscape(remoteAddr), &w); err != nil {
		return Caller{}, err
	}
	caller := Caller{NodeName: strings.TrimSuffix(w.Node.Name, "."), UserID: w.Node.User, LoginName: w.UserProfile.LoginName, Tags: w.Node.Tags}
	caller.Routes = len(w.Node.PrimaryRoutes) > 0
	for _, p := range w.Node.AllowedIPs {
		if !p.IsSingleIP() {
			caller.Routes = true // a subnet route, or an exit node's 0.0.0.0/0
		}
	}
	if raws, ok := w.CapMap[MeshCap]; ok {
		caller.HasCap = true
		for _, raw := range raws {
			var v struct {
				Access []string `json:"access"`
			}
			if json.Unmarshal(raw, &v) == nil {
				caller.Access = append(caller.Access, v.Access...)
			}
		}
	}
	return caller, nil
}

// Vouches reports whether a WhoIs answer vouches for pairing without a
// check code: the same tailnet user as self, not tagged (a tagged node
// has no user), and not routing for others (a subnet router or exit node
// forwards traffic that isn't its own; round-1 review M3).
func Vouches(self int64, c Caller) bool {
	return self != 0 && c.UserID == self && len(c.Tags) == 0 && !c.Routes
}
