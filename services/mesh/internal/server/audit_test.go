package server

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func waitEvents(t *testing.T, a *fakeAudit, n int) []contracts.DeviceRefusal {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs := a.events()
		if len(evs) >= n || time.Now().After(deadline) {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A refused device is logged as device.refused with who and why; repeats
// are folded (debounced per device, code and route) and reported on the
// next one logged, so a device retrying in a loop writes one event.
func TestRefusalsAreAuditedAndDebounced(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	now := time.Unix(1_000_000, 0)
	f.srv.refused.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	for range 5 {
		f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: "/sessions"})
	}
	f.serve(t, req{from: "100.0.0.1", as: f.ids.phone, method: "POST", target: "/sessions/ses_1/land"})
	// Not audited: a route that's Deny for everyone, and a browser.
	f.do(t, "100.0.0.1", "GET", "/credentials", nil)
	f.do(t, "100.0.0.1", "GET", "/sessions", map[string]string{"Origin": "http://x"})

	waitEvents(t, f.audit, 2)
	time.Sleep(50 * time.Millisecond) // nothing more arrives
	evs := f.audit.events()
	if len(evs) != 2 {
		t.Fatalf("events = %+v, want one per device, code and route", evs)
	}
	byCode := map[string]contracts.DeviceRefusal{}
	for _, e := range evs {
		byCode[e.Code] = e
	}
	if u := byCode["unpaired_device"]; u.Fingerprint != string(f.ids.stranger.Fingerprint()) || u.Name != "" || u.Method != "GET" || u.Route != "/sessions" || u.Repeats != 0 {
		t.Fatalf("unpaired refusal = %+v", u)
	}
	if l := byCode["no_access"]; l.Fingerprint != string(f.ids.phone.Fingerprint()) || l.Name != "phone" || l.Level != contracts.GrantView || l.Route != "POST /sessions/{id}/land" {
		t.Fatalf("level refusal = %+v", l)
	}

	// Past the debounce, the next one is logged with the four folded in.
	advance(refusalDebounce)
	f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: "/sessions"})
	evs = waitEvents(t, f.audit, 3)
	if len(evs) != 3 || evs[2].Code != "unpaired_device" || evs[2].Repeats != 4 {
		t.Fatalf("after the debounce: %+v", evs)
	}
}

// A hostile client minting a new key per connection can't flood the log:
// past refusalRate a minute, refusals are only counted, and the count
// rides on the next one logged.
func TestRefusalsAreRateCapped(t *testing.T) {
	a := &fakeAudit{}
	now := time.Unix(1_000_000, 0)
	r := newRefusals(a, func() time.Time { return now })
	logged := 0
	for i := range refusalRate + 10 {
		if r.record(contracts.DeviceRefusal{Fingerprint: string(rune('A'+i%26)) + string(rune('a'+i/26)), Code: "unpaired_device", Route: "/sessions"}) {
			logged++
		}
	}
	if logged != refusalRate {
		t.Fatalf("logged %d in a minute, want the cap %d", logged, refusalRate)
	}
	now = now.Add(time.Minute)
	if !r.record(contracts.DeviceRefusal{Fingerprint: "NEW", Code: "unpaired_device", Route: "/sessions"}) {
		t.Fatal("a new minute logs again")
	}
	evs := waitEvents(t, a, refusalRate+1)
	last := evs[len(evs)-1]
	for _, e := range evs {
		if e.Fingerprint == "NEW" {
			last = e
		}
	}
	if last.Dropped != 10 {
		t.Fatalf("next logged refusal reports dropped=%d, want 10", last.Dropped)
	}
}

// Throwaway keys spend their own audit budget, never the paired devices'.
func TestUnpairedRefusalsCantBlindThePairedAudit(t *testing.T) {
	r := newRefusals(nil, func() time.Time { return time.Unix(1000, 0) })
	for i := range refusalRate + 5 {
		r.record(contracts.DeviceRefusal{Fingerprint: "K" + strings.Repeat("x", i), Code: "unpaired_device"})
	}
	if !r.record(contracts.DeviceRefusal{Fingerprint: "PHONE", PeerID: "per_phone", Code: "no_access", Route: "POST /sessions/{id}/land"}) {
		t.Fatal("a paired device's refusal was dropped after a flood of unpaired keys")
	}
}

// An unpaired key is debounced by key alone, whatever paths it tries; and
// what it asked for is clipped in the event and the log.
func TestUnpairedRefusalsDebounceByKeyAndClipPaths(t *testing.T) {
	f := newFixture(t)
	f.srv.refused.now = func() time.Time { return time.Unix(1000, 0) }
	long := "/" + strings.Repeat("a", 4096)
	for _, target := range []string{long, "/sessions", "/b", "/c"} {
		f.serve(t, req{from: "100.0.0.1", as: f.ids.stranger, method: "GET", target: target})
	}
	waitEvents(t, f.audit, 1)
	time.Sleep(50 * time.Millisecond) // nothing more arrives
	evs := f.audit.events()
	if len(evs) != 1 {
		t.Fatalf("events = %d, want one per unpaired key", len(evs))
	}
	if len(evs[0].Route) > maxLogged+len("…") {
		t.Fatalf("the event carries a %d-byte route", len(evs[0].Route))
	}
}

// Tailnet refusals are logged once a minute per address and code.
func TestTailnetRefusalLogsAreDebounced(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	f := newFixture(t)
	for range 5 {
		f.do(t, "100.0.0.3", "GET", "/sessions", nil)
	}
	if n := strings.Count(buf.String(), "grants this device no"); n != 1 {
		t.Fatalf("%d log lines for 5 refusals from one address:\n%s", n, buf.String())
	}
}
