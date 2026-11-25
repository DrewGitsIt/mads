package mesh

import (
	"context"
	"errors"
	"log"
	"slices"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// What each paired device lets this node do there ("Lets this device …",
// docs/MESH-AUTH-DESIGN.md D9), as its /mesh/hello last said. Viewed
// devices are probed anyway; paired devices this node doesn't view are
// probed too when they can be dialled (a mesh address, and not known to
// host no sessions), only for this. The answer is kept in memory with the
// time it was read, and written to the device's row (remote) when it
// changes, or the first time this Router hears it: so a device offline
// after a restart still shows its last known answer, and a steady device
// writes nothing.

// pairedState is a paired device this node doesn't view but probes.
type pairedState struct {
	peer      contracts.Peer
	reach     contracts.PeerReachability
	reason    string
	rtt       time.Duration
	checkedAt time.Time
}

// remoteSeen is a device's latest hello, and what this Router last wrote
// of it to the row.
type remoteSeen struct {
	remote    contracts.PeerRemote
	written   *contracts.PeerRemote
	writtenAt time.Time
}

// remoteWriteEvery is how often a device's row is written for a change
// that grants this node more there (or a new platform): at most once a
// minute, so a flapping device can't churn peer.updated. A change to less
// (lower, or none) is written at once.
const remoteWriteEvery = time.Minute

// dialsForRemote reports whether a row this node doesn't view is worth
// probing: paired, with an address, and granted something here (a device
// at none has no reason to be dialled, and its address is the other side's
// choice).
func dialsForRemote(p contracts.Peer) bool {
	return p.IsPaired() && p.MeshURL != "" && p.Level() != contracts.GrantNone
}

// hostsNothing reports whether p last said it hosts no sessions. Such a
// device is still asked now and then (rareProbeEvery): one that paired
// before its Mesh Service ran would otherwise read as hosting nothing for
// good.
func hostsNothing(p contracts.Peer) bool {
	return p.Remote != nil && p.Remote.HostsSessions != nil && !*p.Remote.HostsSessions
}

// rareProbeEvery is how often a device that said it hosts nothing is
// asked again.
const rareProbeEvery = 10 * time.Minute

// remoteFromProbe is what a probe says the device lets this node do: its
// hello's highest level, or none when it refused this node outright (it
// hasn't paired it, or the tailnet check failed there). ok is false when
// nothing definite answered (offline, a transport error): the last known
// answer stands.
func remoteFromProbe(p Probe) (contracts.PeerRemote, bool) {
	switch {
	case p.Status == 200 && p.Hello.MeshAPI != 0:
		r := contracts.PeerRemote{Level: topLevel(p.Hello.Access), HostsSessions: p.Hello.HostsSessions,
			Granted: contracts.GrantLevel(p.Hello.Grant), TailnetCapped: p.Hello.TailnetCapped, TailscaleKeyExpiry: p.Hello.TailscaleKeyExpiry}
		if r.Granted != contracts.GrantNone && !r.Granted.Valid() {
			r.Granted = contracts.GrantNone
		}
		if contracts.ValidPlatform(p.Hello.Platform) {
			r.Platform = p.Hello.Platform
		}
		return r, true
	case p.Status == 403:
		return contracts.PeerRemote{Level: contracts.GrantNone}, true
	}
	return contracts.PeerRemote{}, false
}

// topLevel is the highest grant level an access list names.
func topLevel(access []string) contracts.GrantLevel {
	best := contracts.GrantNone
	for _, l := range contracts.GrantLevels {
		if slices.Contains(access, string(l)) {
			best = l
		}
	}
	return best
}

// cloneRemote copies r with its own pointer fields: a row write decodes the
// Data Service's answer into the row, through those pointers, and the
// in-memory answer (m.remote) mustn't share them.
func cloneRemote(r contracts.PeerRemote) contracts.PeerRemote {
	if r.HostsSessions != nil {
		v := *r.HostsSessions
		r.HostsSessions = &v
	}
	for _, t := range []**time.Time{&r.AsOf, &r.TailscaleKeyExpiry, &r.PairedThereAt} {
		if *t != nil {
			v := **t
			*t = &v
		}
	}
	return r
}

func sameRemote(a, b *contracts.PeerRemote) bool {
	if a == nil || b == nil {
		return a == b
	}
	sameHosts := (a.HostsSessions == nil) == (b.HostsSessions == nil) && (a.HostsSessions == nil || *a.HostsSessions == *b.HostsSessions)
	sameExpiry := (a.TailscaleKeyExpiry == nil) == (b.TailscaleKeyExpiry == nil) && (a.TailscaleKeyExpiry == nil || a.TailscaleKeyExpiry.Equal(*b.TailscaleKeyExpiry))
	sameThere := (a.PairedThereAt == nil) == (b.PairedThereAt == nil) && (a.PairedThereAt == nil || a.PairedThereAt.Equal(*b.PairedThereAt))
	return a.Level == b.Level && a.Platform == b.Platform && sameHosts && (a.AsOf == nil) == (b.AsOf == nil) &&
		a.Granted == b.Granted && a.TailnetCapped == b.TailnetCapped && sameExpiry && sameThere
}

// noteRemote folds a probe of p into what p lets this node do, and writes
// it to p's row when it changed (or this Router hasn't written it yet).
func (m *Manager) noteRemote(ctx context.Context, p contracts.Peer, probe Probe) {
	r, ok := remoteFromProbe(probe)
	if !ok {
		return
	}
	now := m.opts.Now().UTC()
	m.mu.Lock()
	seen, had := m.remote[p.ID]
	merged := contracts.PeerRemote{}
	switch {
	case had:
		merged = seen.remote
	case p.Remote != nil:
		merged = *p.Remote
	}
	merged.Level, merged.AsOf, merged.Granted, merged.TailnetCapped = r.Level, &now, r.Granted, r.TailnetCapped
	if probe.Status == 200 {
		// Absent from a hello is "off" (or not on Tailscale), not unknown.
		merged.TailscaleKeyExpiry = r.TailscaleKeyExpiry
	}
	if probe.Status == 200 && p.IsPaired() {
		// It has this node paired: once per pairing (F6 R4's precondition).
		if at := pairedAtOf(p); !at.IsZero() && (merged.PairedThereAt == nil || !merged.PairedThereAt.Equal(at)) {
			merged.PairedThereAt = &at
		}
	}
	if r.Platform != "" {
		merged.Platform = r.Platform
	}
	if r.HostsSessions != nil {
		v := *r.HostsSessions
		merged.HostsSessions = &v
	}
	ref := seen.written
	if ref == nil {
		ref = p.Remote
	}
	changed := !sameRemote(&merged, ref)
	lower := ref != nil && rankOf(merged.Level) < rankOf(ref.Level)
	write := seen.written == nil || (changed && (lower || now.Sub(seen.writtenAt) >= remoteWriteEvery))
	seen.remote = merged
	m.remote[p.ID] = seen
	m.mu.Unlock()
	if !write {
		return
	}
	err := m.editPeer(ctx, p.ID, func(cur *contracts.Peer) (bool, error) {
		if cur.Fingerprint != p.Fingerprint {
			return false, errNotEditable // re-keyed meanwhile: not this device's answer
		}
		v := cloneRemote(merged)
		cur.Remote = &v
		return false, nil
	})
	switch {
	case err == nil:
		m.mu.Lock()
		if s, ok := m.remote[p.ID]; ok {
			v := merged
			s.written, s.writtenAt = &v, now
			m.remote[p.ID] = s
		}
		m.mu.Unlock()
	case errors.Is(err, errNotEditable) || errors.Is(err, dataclient.ErrNotFound):
	default:
		log.Printf("mesh: record what %s lets this device do: %v", p.Name, err)
	}
}

// flushRemote writes every device's latest answer that the debounce held
// back. The debounce bounds row writes while running; a write on the first
// answer after start (written is nil then) and this flush at shutdown keep
// the stored answer current across restarts, whether or not the device is
// reachable after one.
func (m *Manager) flushRemote(ctx context.Context) {
	type pending struct {
		p contracts.Peer
		r contracts.PeerRemote
	}
	var todo []pending
	m.mu.Lock()
	for id, seen := range m.remote {
		if seen.written != nil && !sameRemote(&seen.remote, seen.written) {
			if p, ok := m.all[id]; ok {
				todo = append(todo, pending{p, seen.remote})
			}
		}
	}
	m.mu.Unlock()
	for _, t := range todo {
		r := t.r
		err := m.editPeer(ctx, t.p.ID, func(cur *contracts.Peer) (bool, error) {
			if cur.Fingerprint != t.p.Fingerprint {
				return false, errNotEditable
			}
			v := cloneRemote(r)
			cur.Remote = &v
			return false, nil
		})
		if err != nil && !errors.Is(err, errNotEditable) && !errors.Is(err, dataclient.ErrNotFound) {
			log.Printf("mesh: record what %s lets this device do, at shutdown: %v", t.p.Name, err)
		}
	}
}

// observePaired records a probe of a paired device this node doesn't
// view. Its reachability isn't logged: this node never reads it.
func (m *Manager) observePaired(peerID string, p Probe) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.paired[peerID]; ok {
		st.reach, st.reason, st.rtt, st.checkedAt = p.State, p.Reason, p.RTT, m.opts.Now()
	}
}

// overlayLocked puts the latest hello's answer on s. m.mu must be held.
func (m *Manager) overlayLocked(s contracts.PeerStatus) contracts.PeerStatus {
	if rs, ok := m.remote[s.ID]; ok {
		r := rs.remote
		s.Remote = &r
	}
	return s
}
