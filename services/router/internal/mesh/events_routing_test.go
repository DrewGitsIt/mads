package mesh

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// A plain JSON GET /events?session_id= for a session on a peer (the UI's
// history reads: provisioning, the pipeline fold, delegate paging) reads
// that session's host's log, like /sessions/{id}/...; the SSE stream, a
// local session's read and a relayed request stay here.
func TestEventsHistoryForARemoteSessionGoesToItsHost(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	p.sessions = []map[string]any{{"id": "ses_remote"}}
	h.addPeer(t, p)
	h.mgr.RefreshIndex(context.Background())
	local := &contracts.Session{Name: "here", Provider: "fake"}
	if err := h.data.CreateSession(context.Background(), local); err != nil {
		t.Fatal(err)
	}

	code, body := get(t, h.srv.URL+"/events?session_id=ses_remote&types=session.created")
	if code != 200 || !strings.Contains(body, `"peer":"pop-os"`) {
		t.Fatalf("remote session's history: %d %s", code, body)
	}
	if n := countPrefix(p.forwarded(), "GET /events?session_id=ses_remote&types=session.created"); n != 1 {
		t.Fatalf("peer saw %v", p.forwarded())
	}

	before := len(p.forwarded())
	for name, req := range map[string]func() *http.Request{
		"the stream": func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/events?session_id=ses_remote", nil)
			r.Header.Set("Accept", "text/event-stream")
			return r
		},
		"a local session": func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/events?session_id="+local.ID, nil)
			return r
		},
		"no session": func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/events?types=session.created", nil)
			return r
		},
		"a relayed request": func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/events?session_id=ses_remote", nil)
			r.Header.Set("X-Mads-Mesh-Peer", "mac")
			return r
		},
	} {
		resp, err := http.DefaultClient.Do(req())
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		_ = resp.Body.Close()
		if !strings.Contains(string(buf[:n]), `"served_by":"local"`) {
			t.Errorf("%s: not answered here: %s", name, buf[:n])
		}
	}
	if fw := p.forwarded(); len(fw) != before {
		t.Fatalf("forwarded: %v", fw[before:])
	}
}

// /peers/{peer}/events/head reaches the peer like any peer path: the head a
// remote session's live-only bus starts from.
func TestPeerEventsHeadIsForwarded(t *testing.T) {
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	h.addPeer(t, p)
	code, body := get(t, h.srv.URL+"/peers/pop-os/events/head")
	if code != 200 || !strings.Contains(body, `"path":"/events/head"`) {
		t.Fatalf("%d %s", code, body)
	}
}
