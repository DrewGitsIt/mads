package mesh

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// A peer's redirect is never followed — not to plain HTTP, not to another
// host — by any call this Router makes to it: probes, the session index,
// links, forwards.
func TestPeerRedirectsAreNotFollowed(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	p.sessions = []map[string]any{{"id": "ses_remote"}}
	h.addPeer(t, p)
	h.mgr.RefreshIndex(context.Background())

	var lured atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lured.Add(1)
		writeJSON(w, http.StatusOK, Hello{Node: "pop-os", MeshAPI: MeshAPI, Access: []string{"view", "control"}})
	}))
	t.Cleanup(elsewhere.Close)
	p.mu.Lock()
	p.redirect = elsewhere.URL
	p.mu.Unlock()

	ctx := context.Background()
	// The redirect itself is the answer (not followed, so not even tried).
	if got := h.mgr.ProbePeer(ctx, *p.row("pop-os")); got.State == contracts.PeerConnected || !strings.Contains(got.Reason, "302") {
		t.Fatalf("a redirected hello: %+v, want its 302 as the answer", got)
	}
	if _, err := h.mgr.peerSessions(ctx, *p.row("pop-os"), ""); err == nil {
		t.Fatal("a redirected session list read as a list")
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(h.srv.URL + "/sessions/ses_remote/transcript")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if lured.Load() != 0 {
		t.Fatalf("the Router followed a peer's redirect %d times", lured.Load())
	}
}

// Every request to a peer goes through a transport that sends only https
// to that peer's own address.
func TestPinnedTransportRefusesOtherDestinations(t *testing.T) {
	var sent atomic.Int32
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		sent.Add(1)
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	rt := &pinnedTransport{base: base, host: "pop-os:17380"}
	for _, u := range []string{"http://pop-os:17380/x", "https://evil:17380/x", "https://pop-os:17381/x"} {
		req, _ := http.NewRequest(http.MethodGet, u, strings.NewReader("body"))
		if _, err := rt.RoundTrip(req); err == nil {
			t.Errorf("%s was sent", u)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://pop-os:17380/x", nil)
	if _, err := rt.RoundTrip(req); err != nil || sent.Load() != 1 {
		t.Fatalf("the peer's own address: %v (sent %d)", err, sent.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
