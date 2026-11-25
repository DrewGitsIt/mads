package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
)

// Discovery (docs/MESH-NETWORK-DESIGN.md U4, S6): while this device's
// setup window is open (at most ten minutes, opened by a person at the
// device: device.discoverable), GET /mesh/discover answers anyone who
// completes the TLS handshake with its own device key, with just enough
// for a person to recognise this device: {mads, name, platform,
// fingerprint, mesh_api}. The fingerprint is the one this listener's
// certificate carries, which the finder checks against its handshake.
// Outside the window the route doesn't exist: the same unpaired_device as
// any stranger gets. Rate-limited per address like /mesh/pair, and audited
// as device.discovered, debounced per address.

// Discover is GET /mesh/discover's answer.
type Discover struct {
	Mads        int    `json:"mads"`
	Name        string `json:"name"`
	Platform    string `json:"platform,omitempty"`
	Fingerprint string `json:"fingerprint"`
	MeshAPI     int    `json:"mesh_api"`
	// PairedWithYou: the asking key (the one its handshake proved) is
	// paired here (docs/MESH-NETWORK-DESIGN.md F6, R5). Nothing about any
	// other device.
	PairedWithYou bool `json:"paired_with_you"`
}

// SetDiscoverable opens this device's setup window until until, or closes
// it (zero, or a time past). The Mesh Service follows device.discoverable
// into this; nothing a remote device sends reaches it. A window is never
// longer than contracts.DiscoverableMax from now, whatever until says.
func (s *Server) SetDiscoverable(until time.Time) {
	s.SetWindow(contracts.Discoverable{Until: until})
}

// SetWindow is SetDiscoverable from a device.discoverable: the window is
// also never longer than contracts.DiscoverableAbsoluteMax from when it
// was first opened.
func (s *Server) SetWindow(d contracts.Discoverable) {
	until := d.Until
	if d.ClosedAt != nil {
		until = time.Time{}
	}
	if !d.OpenedAt.IsZero() {
		if max := d.OpenedAt.Add(contracts.DiscoverableAbsoluteMax); until.After(max) {
			until = max
		}
	}
	now := s.opts.Now()
	if max := now.Add(contracts.DiscoverableMax); until.After(max) {
		until = max
	}
	var v int64
	if until.After(now) {
		v = until.UnixNano()
	}
	// A new window (opened again, or first opened) starts its counts
	// afresh; renewals of one closed for abandoned pairings don't reopen it.
	opened := d.OpenedAt.UnixNano()
	if d.OpenedAt.IsZero() {
		opened = 0
	}
	same := opened != 0 && s.windowOpened.Load() == opened
	if same && s.abuseClosed.Load() {
		v = 0
	}
	if !same || (s.discoverUntil.Load() == 0 && v != 0 && opened == 0) {
		s.windowOpened.Store(opened)
		s.abuseClosed.Store(false)
		s.commits.reset()
	}
	if old := s.discoverUntil.Swap(v); (old != 0) != (v != 0) {
		if v != 0 {
			log.Printf("mesh: setup window open until %s: other mads devices on this device's mesh networks can find it", until.Local().Format("15:04:05"))
		} else {
			log.Printf("mesh: setup window closed")
		}
	}
}

// DiscoverableNow reports whether the setup window is open.
func (s *Server) DiscoverableNow() bool {
	v := s.discoverUntil.Load()
	return v != 0 && s.opts.Now().Before(time.Unix(0, v))
}

// DiscoverableUntil is the window's end (zero: closed).
func (s *Server) DiscoverableUntil() time.Time {
	if !s.DiscoverableNow() {
		return time.Time{}
	}
	return time.Unix(0, s.discoverUntil.Load())
}

// Discovery probes: generous enough for a finder re-probing every few
// seconds while its dialog is open, never enough to load the host.
var (
	discoverPerAddr = rate{burst: 10, every: 3 * time.Second}
	discoverGlobal  = rate{burst: 120, every: 250 * time.Millisecond}
)

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request, fp devicekey.Fingerprint) {
	if r.Method != http.MethodGet {
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	if wait, ok := s.pairLimits.allowDiscover(limitKey(r.RemoteAddr)); !ok {
		closeIfUnadmitted(w, r)
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many discovery probes; slow down")
		return
	}
	s.discovered.record(contracts.DeviceDiscovery{Kind: "probe", Remote: r.RemoteAddr, Fingerprint: string(fp), Network: connNet(r.Context()).ID})
	closeIfUnadmitted(w, r)
	writeJSON(w, http.StatusOK, Discover{Mads: 1, Name: clip(s.name()), Platform: s.opts.Platform, Fingerprint: string(s.opts.Device), MeshAPI: MeshAPI, PairedWithYou: s.paired(fp)})
}

func (l *pairLimits) allowDiscover(addr string) (time.Duration, bool) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if wait, ok := l.perAddr(l.discover, addr, discoverPerAddr, now); !ok {
		return wait, false
	}
	return l.discoverAll.take(discoverGlobal, now)
}

// discoveries debounces device.discovered: one per address and kind per
// discoveredDebounce, the rest counted into the next.
type discoveries struct {
	sink Appender
	now  func() time.Time

	mu   sync.Mutex
	seen map[string]*refusalSeen
}

const discoveredDebounce = 10 * time.Minute

func newDiscoveries(sink Appender, now func() time.Time) *discoveries {
	return &discoveries{sink: sink, now: now, seen: map[string]*refusalSeen{}}
}

func (d *discoveries) record(ev contracts.DeviceDiscovery) {
	now := d.now()
	key := ev.Kind + " " + limitKey(ev.Remote)
	d.mu.Lock()
	if prev, ok := d.seen[key]; ok && now.Sub(prev.at) < discoveredDebounce {
		prev.repeats++
		d.mu.Unlock()
		return
	} else if ok {
		ev.Repeats = prev.repeats
	}
	if len(d.seen) >= refusalMemory {
		clear(d.seen)
	}
	d.seen[key] = &refusalSeen{at: now}
	d.mu.Unlock()
	log.Printf("mesh: setup window: %s from %s (%s)", ev.Kind, ev.Remote, devicekey.Fingerprint(ev.Fingerprint).Short())
	if d.sink == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		payload, _ := json.Marshal(ev)
		if err := d.sink.Append(ctx, &contracts.Envelope{Type: contracts.DeviceDiscovered, Actor: "mesh", Payload: payload}); err != nil {
			log.Printf("mesh: log device.discovered: %v", err)
		}
	}()
}
