package tailscale

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/meshnet"
)

func sortNodes(ns []Node) {
	slices.SortFunc(ns, func(a, b Node) int { return strings.Compare(a.DNSName+a.HostName, b.DNSName+b.HostName) })
}

// Helper is the Tailscale meshnet.Helper: it confirms that a VPN
// interface is tailscaled's own (its addresses are this node's tailnet
// addresses) and names it, with its MagicDNS name. 100.64.0.0/10 alone
// never says "Tailscale" (NetBird and carrier NAT use it too).
type Helper struct {
	Client *Client
	// CacheFor reuses one status read across a scan's networks (default
	// 2 s).
	CacheFor time.Duration

	mu  sync.Mutex
	at  time.Time
	st  Status
	err error
}

var _ meshnet.Helper = (*Helper)(nil)

// Status is tailscaled's status as last read (cached CacheFor), or why
// it couldn't be read.
func (h *Helper) Status(ctx context.Context) (Status, error) {
	every := h.CacheFor
	if every <= 0 {
		every = 2 * time.Second
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.at.IsZero() && time.Since(h.at) < every {
		return h.st, h.err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	h.st, h.err = h.Client.Status(ctx, false)
	h.at = time.Now()
	return h.st, h.err
}

func (h *Helper) Describe(ctx context.Context, n *meshnet.Network) {
	if n.Kind != meshnet.KindOverlay || h == nil || h.Client == nil {
		return
	}
	st, err := h.Status(ctx)
	if err != nil || st.Self == nil {
		return
	}
	for _, a := range n.Addrs {
		if slices.Contains(st.Self.TailscaleIPs, a) {
			n.ID, n.Label, n.Confirmed, n.DNSName = meshnet.TailscaleID, "Tailscale", true, st.Self.DNSName
			for _, p := range meshnet.TailnetRanges {
				if !slices.Contains(n.Prefixes, p) {
					n.Prefixes = append(n.Prefixes, p)
				}
			}
			return
		}
	}
}
