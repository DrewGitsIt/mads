package mesh

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Candidate is a tailnet device "Add device" can offer (D3).
type Candidate struct {
	Name         string                     `json:"name"`
	DNSName      string                     `json:"dns_name"`
	OS           string                     `json:"os,omitempty"`
	Online       bool                       `json:"online"`
	Added        bool                       `json:"added"`
	Reachability contracts.PeerReachability `json:"reachability"`
	Reason       string                     `json:"reason,omitempty"`
	MadsCommit   string                     `json:"mads_commit,omitempty"`
	Access       []string                   `json:"access,omitempty"`
}

// tailscaleBinaries are where the CLI lives, in the order tried. The
// Router reads tailnet status through the CLI rather than the LocalAPI
// because macOS daemons don't expose a fixed LocalAPI socket (P3).
var tailscaleBinaries = []string{
	"tailscale",
	"/opt/homebrew/bin/tailscale",
	"/usr/local/bin/tailscale",
	"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
}

// StatusSource lists the tailnet's other devices; a seam for tests.
type StatusSource func(ctx context.Context) ([]Candidate, error)

// tailscaleStatus runs `tailscale status --json`.
func tailscaleStatus(ctx context.Context) ([]Candidate, error) {
	var lastErr error
	for _, bin := range tailscaleBinaries {
		path, err := exec.LookPath(bin)
		if err != nil {
			if _, statErr := os.Stat(bin); statErr != nil {
				lastErr = err
				continue
			}
			path = bin
		}
		out, err := exec.CommandContext(ctx, path, "status", "--json").Output()
		if err != nil {
			lastErr = err
			continue
		}
		var st struct {
			Peer map[string]struct {
				HostName string
				DNSName  string
				OS       string
				Online   bool
			}
		}
		if err := json.Unmarshal(out, &st); err != nil {
			return nil, err
		}
		var list []Candidate
		for _, p := range st.Peer {
			list = append(list, Candidate{
				Name:    p.HostName,
				DNSName: strings.TrimSuffix(p.DNSName, "."),
				OS:      p.OS,
				Online:  p.Online,
			})
		}
		return list, nil
	}
	return nil, lastErr
}

// Candidates lists this tailnet's other devices and marks the ones already
// added, with their live state. Best effort: an unreadable tailnet yields
// an empty list, never an error page.
//
// A device not added yet isn't probed: with mesh TLS nothing can be
// dialled before its key is pinned (docs/MESH-AUTH-DESIGN.md D2), and an
// unauthenticated "are you mads?" endpoint would be exactly the
// pre-pairing surface D6 leaves to the pairing ceremony (A6). So it reads
// as unpaired until its fingerprint is given (`mads device` on it).
func (m *Manager) Candidates(ctx context.Context) []Candidate {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	source := m.statusSource
	if source == nil {
		source = tailscaleStatus
	}
	list, err := source(ctx)
	if err != nil {
		return []Candidate{}
	}

	m.mu.Lock()
	added := map[string]contracts.PeerStatus{}
	for _, st := range m.peers {
		added[st.peer.DNSName] = st.status()
	}
	m.mu.Unlock()

	for i := range list {
		c := &list[i]
		st, ok := added[c.DNSName]
		c.Added = ok
		switch {
		case ok:
			c.Reachability, c.Reason = st.Reachability, st.Reason
			c.MadsCommit, c.Access = st.MadsCommit, st.Access
		case !c.Online || c.DNSName == "":
			c.Reachability, c.Reason = contracts.PeerUnreachable, "offline"
		default:
			c.Reachability = contracts.PeerUnpaired
			c.Reason = "not paired: run `mads device` on " + firstNonEmpty(c.Name, "it") + " and add it with that fingerprint"
		}
	}
	// Devices running mads first, then by name.
	slices.SortFunc(list, func(a, b Candidate) int {
		ra, rb := a.Reachability == contracts.PeerConnected, b.Reachability == contracts.PeerConnected
		if ra != rb {
			if ra {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	if list == nil {
		list = []Candidate{}
	}
	return list
}
