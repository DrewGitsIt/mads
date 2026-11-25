package meshnet

import "github.com/drewkidwell/mads/pkg/contracts"

// FromNet is a contracts.NetAddress as a Candidate.
func FromNet(a contracts.NetAddress) Candidate {
	return Candidate{Via: a.Via, Addr: a.Addr, Kind: Kind(a.Kind), Label: a.Label, Network: a.Network}
}

// ToNet is c's wire form.
func ToNet(c Candidate) contracts.NetAddress {
	return contracts.NetAddress{Via: c.Via, Addr: c.Addr, Kind: string(c.Kind), Label: c.Label, Network: c.Network}
}

// FromNets converts a list.
func FromNets(in []contracts.NetAddress) []Candidate {
	out := make([]Candidate, 0, len(in))
	for _, a := range in {
		out = append(out, FromNet(a))
	}
	return out
}

// ToNets converts a list.
func ToNets(in []Candidate) []contracts.NetAddress {
	out := make([]contracts.NetAddress, 0, len(in))
	for _, c := range in {
		out = append(out, ToNet(c))
	}
	return out
}

// CleanNets is CleanCandidates on the wire form.
func CleanNets(in []contracts.NetAddress) []contracts.NetAddress {
	return ToNets(CleanCandidates(FromNets(in)))
}
