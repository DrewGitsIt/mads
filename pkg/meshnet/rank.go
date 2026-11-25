package meshnet

import (
	"net/netip"
	"slices"
	"strings"
)

// InferKind is the kind of an address that came without one (a v1
// pairing code's, a legacy mesh_url's): a tailnet address or MagicDNS
// name is an overlay, a private address a LAN, anything else public. A
// name that isn't a tailnet's is unknown ("").
func InferKind(c Candidate) Kind {
	if c.Kind.Valid() {
		return c.Kind
	}
	if ip, ok := c.IP(); ok {
		for _, p := range TailnetRanges {
			if p.Contains(ip) {
				return KindOverlay
			}
		}
		if IsPrivate(ip) || isLoopback(ip) {
			return KindLAN
		}
		return KindPublic
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSuffix(c.Host(), ".")), ".ts.net") {
		return KindOverlay
	}
	return ""
}

// OnNetwork reports whether c is on a network this device is on now (F3
// step 1, S2): an IP address the policy would dial; a MagicDNS name only
// with a Tailscale network here; any other name is tried (the policy
// checks what it resolves to at dial time). Nothing is dialled for an
// address that isn't, so failure is fast.
func OnNetwork(c Candidate, local []Network, pol *DialPolicy, userAdded bool) bool {
	if c.Via != "" && c.Via != DirectVia {
		return false // an adapter this build doesn't have
	}
	if ip, ok := c.IP(); ok {
		if pol == nil {
			pol = &DialPolicy{Local: func() []netip.Prefix { return Prefixes(local) }}
		}
		return pol.CheckIP(ip, userAdded) == ""
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSuffix(c.Host(), ".")), ".ts.net") {
		// A MagicDNS name needs Tailscale here: the network tailscaled
		// confirmed, or a VPN on its range when it couldn't be asked.
		return slices.ContainsFunc(local, func(n Network) bool {
			return n.ID == TailscaleID || (n.Kind == KindOverlay && slices.Contains(n.Prefixes, TailnetRanges[0]))
		})
	}
	return true
}

// Rank orders candidates for a race (F3 step 2): the last address that
// worked first, the rest by network (same LAN, then VPN or tailnet, then
// public), then in the order the host listed them. lastGood is a
// Candidate.Addr ("" none). Ranking never drops anything: filtering is
// OnNetwork's.
func Rank(cands []Candidate, lastGood string) []Candidate {
	out := slices.Clone(cands)
	idx := map[string]int{}
	for i, c := range cands {
		if _, ok := idx[c.Key()]; !ok {
			idx[c.Key()] = i
		}
	}
	slices.SortStableFunc(out, func(a, b Candidate) int {
		ag, bg := lastGood != "" && strings.EqualFold(a.Addr, lastGood), lastGood != "" && strings.EqualFold(b.Addr, lastGood)
		switch {
		case ag && !bg:
			return -1
		case bg && !ag:
			return 1
		}
		if pa, pb := InferKind(a).Priority(), InferKind(b).Priority(); pa != pb {
			return pa - pb
		}
		return idx[a.Key()] - idx[b.Key()]
	})
	return out
}

// Better reports whether a ranks before b by network alone (the upgrade
// loop's question: is there a better path than the one in use).
func Better(a, b Candidate) bool { return InferKind(a).Priority() < InferKind(b).Priority() }
