package server

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Refusals are logged as device.refused (docs/MESH-AUTH-DESIGN.md D8),
// debounced the way peer.reachability_changed is, so a device retrying in a
// loop — or a hostile client minting a new key per connection — can't
// flood the event log:
//   - the same device, code and route is logged at most once per
//     refusalDebounce; the refusals folded in are counted and reported on
//     the next one logged (Repeats);
//   - an unpaired key is debounced by key alone, whatever path it asks
//     for (its route is recorded, clipped, but not part of the key);
//   - at most refusalRate are logged per minute for unpaired keys, and as
//     many again for paired devices, each budget on its own: throwaway
//     keys can't use up the budget that records what paired devices try.
//     The rest are counted and reported on the next one logged (Dropped).
const (
	refusalDebounce = 10 * time.Minute
	refusalRate     = 30
	// refusalMemory bounds the keys remembered; past it, expired keys are
	// forgotten, and if none have expired, all are.
	refusalMemory = 4096
)

type refusalKey struct{ fp, code, route string }

type refusalSeen struct {
	at      time.Time
	repeats int
}

// budget is one minute-window rate cap.
type budget struct {
	windowStart time.Time
	inWindow    int
	dropped     int
}

type refusals struct {
	sink Appender
	now  func() time.Time

	mu   sync.Mutex
	seen map[refusalKey]*refusalSeen
	// unpaired and paired are the two budgets: keys this host doesn't
	// know, and devices it paired.
	unpaired, paired budget
}

func newRefusals(sink Appender, now func() time.Time) *refusals {
	return &refusals{sink: sink, now: now, seen: map[refusalKey]*refusalSeen{}}
}

// record notes one refusal and reports whether it was logged (as an event
// and a log line) rather than folded into a later one.
func (f *refusals) record(ref contracts.DeviceRefusal) bool {
	now := f.now()
	k := refusalKey{ref.Fingerprint, ref.Code, ref.Route}
	b := &f.paired
	if ref.PeerID == "" {
		k.route, b = "", &f.unpaired
	}
	f.mu.Lock()
	prev, ok := f.seen[k]
	if ok && now.Sub(prev.at) < refusalDebounce {
		prev.repeats++
		f.mu.Unlock()
		return false
	}
	if now.Sub(b.windowStart) >= time.Minute {
		b.windowStart, b.inWindow = now, 0
	}
	if b.inWindow >= refusalRate {
		b.dropped++
		f.mu.Unlock()
		return false
	}
	b.inWindow++
	if ok {
		ref.Repeats = prev.repeats
	}
	ref.Dropped, b.dropped = b.dropped, 0
	if len(f.seen) >= refusalMemory {
		for key, s := range f.seen {
			if now.Sub(s.at) >= refusalDebounce {
				delete(f.seen, key)
			}
		}
		if len(f.seen) >= refusalMemory {
			f.seen = map[refusalKey]*refusalSeen{}
		}
	}
	f.seen[k] = &refusalSeen{at: now}
	f.mu.Unlock()

	if f.sink != nil {
		// Off the request path: a slow or down Data Service never holds up
		// the refusal itself. Bounded by refusalRate, so these can't pile up.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			payload, _ := json.Marshal(ref)
			if err := f.sink.Append(ctx, &contracts.Envelope{Type: contracts.DeviceRefused, Actor: "mesh", Payload: payload}); err != nil {
				log.Printf("mesh: log device.refused for %s: %v", ref.Fingerprint, err)
			}
		}()
	}
	return true
}
