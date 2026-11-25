package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// "Only accept devices on my tailnet" is live (SetRequireTailnet): off, a
// paired key from any network is enough; on again, the next request needs
// the tailnet factor, and a stream opened without it ends at once.
func TestRequireTailnetFollowsTheSettingLive(t *testing.T) {
	f := newFixture(t)
	if !f.srv.RequireTailnet() {
		t.Fatal("starts with the option's value (true)")
	}
	// 192.0.2.1 isn't on the tailnet: refused while it's required.
	off := req{from: "192.0.2.1", as: f.ids.mac, method: "GET", target: "/sessions"}
	if w := f.serve(t, off); w.Code != http.StatusForbidden {
		t.Fatalf("off the tailnet, required: %d %s", w.Code, w.Body)
	}

	f.srv.SetRequireTailnet(false)
	calls := f.tn.calls
	if w := f.serve(t, off); w.Code != http.StatusOK {
		t.Fatalf("off the tailnet, not required: %d %s", w.Code, w.Body)
	}
	if f.tn.calls != calls {
		t.Fatal("WhoIs was asked with the tailnet not required")
	}

	// A stream from a device the tailnet check now fails: it ends as soon
	// as the setting is turned back on.
	front := f.listen(t)
	f.tn.set("127.0.0.1", stranger)
	s := openEvents(t, meshClient(t, f.ids.mac, f.ids.host), front.URL)
	f.srv.SetRequireTailnet(true)
	if !s.endsWithin(3 * time.Second) {
		t.Fatal("a stream admitted without the tailnet outlived turning it back on")
	}
	if w := f.serve(t, off); w.Code != http.StatusForbidden {
		t.Fatalf("required again: %d %s", w.Code, w.Body)
	}
	// Setting the same value again changes nothing.
	f.srv.SetRequireTailnet(true)
	if !f.srv.RequireTailnet() {
		t.Fatal("still required")
	}
}

// The tailnet turned on between a request's admission (without it) and
// its registration in s.active: forward re-checks it there, so the
// switch's own re-check can't miss it (C).
func TestForwardRechecksATailnetTurnedOnMeanwhile(t *testing.T) {
	f := newFixture(t)
	f.srv.SetRequireTailnet(false)
	dev, _ := f.dirs.Lookup(string(f.ids.mac.Fingerprint()))
	admittedWithout := caller{peer: dev, fp: f.ids.mac.Fingerprint(), level: dev.Level()}
	f.srv.SetRequireTailnet(true)

	off := httptest.NewRequest("GET", "/sessions", nil)
	off.RemoteAddr = "192.0.2.1:41000"
	w := httptest.NewRecorder()
	f.srv.forward(w, off, admittedWithout)
	if w.Code != http.StatusForbidden || errorCode(t, w) != "grant_changed" {
		t.Fatalf("off the tailnet, admitted before it was required: %d %s", w.Code, w.Body)
	}
	// On the tailnet, with a capability that allows its level: it goes on.
	on := httptest.NewRequest("GET", "/sessions", nil)
	on.RemoteAddr = "100.0.0.1:41000"
	w = httptest.NewRecorder()
	f.srv.forward(w, on, admittedWithout)
	if w.Code != http.StatusOK {
		t.Fatalf("on the tailnet: %d %s", w.Code, w.Body)
	}
}
