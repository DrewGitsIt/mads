// Package tailnet is the Mesh Service's view of tailscaled: who this node
// is, and who is calling (docs/MESH-MODE-DESIGN.md D2).
//
// It speaks tailscaled's LocalAPI directly over its unix socket with plain
// net/http rather than importing tailscale.com: the Mesh Service needs two
// read-only endpoints, and the dependency would dwarf the rest of mads.
// On Linux the socket is world-writable (0666), so no root is needed.
// macOS daemons expose the LocalAPI differently; that's P3.
package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultSocket is tailscaled's LocalAPI socket on Linux.
const DefaultSocket = "/var/run/tailscale/tailscaled.sock"

// MeshCap is the tailnet app capability that admits a peer to the mesh.
// Its values carry the access list: [{"access":["control"]}] (or ["view"]).
const MeshCap = "github.com/drewgitsit/mads/cap/mesh"

// Self is this node as tailscaled sees it.
type Self struct {
	HostName string
	DNSName  string
	UserID   int64
	IPv4     string // the node's tailnet IPv4, the mesh listener's address
}

// Caller is the tailnet identity behind a remote address.
type Caller struct {
	NodeName  string // MagicDNS name, without the trailing dot
	UserID    int64
	LoginName string
	Tags      []string
	// Access is the union of every grant's access list for MeshCap, nil when
	// the caller has no MeshCap at all.
	Access []string
	HasCap bool
}

// Client talks to one tailscaled LocalAPI socket.
type Client struct {
	http *http.Client
}

// New returns a Client for the LocalAPI socket at path.
func New(path string) *Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &Client{http: &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", path)
			},
		},
	}}
}

// The LocalAPI checks Host; this is the name its own client uses.
const localAPIBase = "http://local-tailscaled.sock/localapi/v0/"

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, localAPIBase+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tailscaled localapi: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tailscaled localapi %s: status %d", strings.SplitN(path, "?", 2)[0], resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ErrNotFound is WhoIs on an address that isn't a tailnet peer.
var ErrNotFound = errors.New("not a tailnet peer")

// Self reads this node's identity and tailnet IPv4.
func (c *Client) Self(ctx context.Context) (Self, error) {
	var st struct {
		Self struct {
			HostName     string
			DNSName      string
			UserID       int64
			TailscaleIPs []string
		}
	}
	if err := c.get(ctx, "status?peers=false", &st); err != nil {
		return Self{}, err
	}
	s := Self{HostName: st.Self.HostName, DNSName: strings.TrimSuffix(st.Self.DNSName, "."), UserID: st.Self.UserID}
	for _, ip := range st.Self.TailscaleIPs {
		if p := net.ParseIP(ip); p != nil && p.To4() != nil {
			s.IPv4 = ip
			break
		}
	}
	if s.IPv4 == "" {
		return s, errors.New("tailscaled reports no tailnet IPv4 for this node (is it logged in?)")
	}
	return s, nil
}

// whoisResponse is the subset of tailscaled's apitype.WhoIsResponse we read.
type whoisResponse struct {
	Node struct {
		Name string
		User int64
		Tags []string
	}
	UserProfile struct {
		ID        int64
		LoginName string
	}
	CapMap map[string][]json.RawMessage
}

// WhoIs identifies the tailnet node behind remoteAddr ("ip:port").
func (c *Client) WhoIs(ctx context.Context, remoteAddr string) (Caller, error) {
	var w whoisResponse
	if err := c.get(ctx, "whois?addr="+url.QueryEscape(remoteAddr), &w); err != nil {
		return Caller{}, err
	}
	return callerFrom(w)
}

func callerFrom(w whoisResponse) (Caller, error) {
	caller := Caller{
		NodeName:  strings.TrimSuffix(w.Node.Name, "."),
		UserID:    w.Node.User,
		LoginName: w.UserProfile.LoginName,
		Tags:      w.Node.Tags,
	}
	raws, ok := w.CapMap[MeshCap]
	if !ok {
		return caller, nil
	}
	caller.HasCap = true
	seen := map[string]bool{}
	for _, raw := range raws {
		var v struct {
			Access []string `json:"access"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return Caller{}, fmt.Errorf("malformed %s grant value %s: %w", MeshCap, raw, err)
		}
		for _, a := range v.Access {
			if !seen[a] {
				seen[a] = true
				caller.Access = append(caller.Access, a)
			}
		}
	}
	return caller, nil
}
