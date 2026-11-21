package mesh

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"sync"
	"testing"
)

// noFollow is a client that shows a redirect rather than following it, as
// a test must to see what the UI's browser would be told to do.
var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func relayHarness(t *testing.T) (*harness, *fakePeer) {
	t.Helper()
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	p.sessions = []map[string]any{{"id": "ses_remote"}}
	h.addPeer(t, p)
	h.mgr.RefreshIndex(context.Background())
	return h, p
}

func (p *fakePeer) setAnswer(f http.HandlerFunc) {
	p.mu.Lock()
	p.answer = f
	p.mu.Unlock()
}

// A peer's redirect is never passed to the UI: its browser would follow
// it on this node's origin, with this node's credentials, to any local
// route (a trust change, a delete, credentials). Every relayed path, every
// method, every redirect status becomes 502 peer_redirect with no Location.
func TestPeerRedirectsAreNotRelayed(t *testing.T) {
	h, p := relayHarness(t)
	for _, status := range []int{301, 302, 303, 307, 308} {
		p.setAnswer(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/sessions/ses_local/trust")
			w.WriteHeader(status)
		})
		for _, tc := range []struct{ method, target string }{
			{"POST", "/sessions/ses_remote/trust"},
			{"POST", "/sessions/ses_remote/rename"},
			{"DELETE", "/sessions/ses_remote"},
			{"GET", "/sessions/ses_remote/output"},
			{"GET", "/sessions/ses_remote/attachments/att_1"},
			{"GET", "/peers/pop-os/credentials"},
			{"GET", "/peers/pop-os/events"},
		} {
			req, _ := http.NewRequest(tc.method, h.srv.URL+tc.target, strings.NewReader(`{"trust":"full"}`))
			resp, err := noFollow.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("Location") != "" || !strings.Contains(string(body), "peer_redirect") {
				t.Errorf("%d for %s %s: relayed as %d, Location %q, %s", status, tc.method, tc.target, resp.StatusCode, resp.Header.Get("Location"), body)
			}
		}
	}
}

// Only the headers that describe a body survive the relay; a peer can't set
// cookies, navigate, loosen CORS or clear site data on this node's origin.
func TestPeerResponseHeadersAreFiltered(t *testing.T) {
	h, p := relayHarness(t)
	p.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range map[string]string{
			"Set-Cookie": "mads=stolen; Path=/", "Refresh": "0; url=/credentials", "Link": "</x>; rel=preload",
			"Clear-Site-Data": `"*"`, "Access-Control-Allow-Origin": "*", "Access-Control-Allow-Credentials": "true",
			"Content-Security-Policy": "script-src *", "Location": "/credentials", "Strict-Transport-Security": "max-age=1",
			"X-Frame-Options": "ALLOWALL", "Content-Type": "application/json", "Cache-Control": "no-store", "Etag": `"v1"`,
		} {
			w.Header().Set(k, v)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	resp, err := noFollow.Get(h.srv.URL + "/sessions/ses_remote/transcript")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	for _, k := range []string{"Set-Cookie", "Refresh", "Link", "Clear-Site-Data", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Location", "Strict-Transport-Security", "X-Frame-Options"} {
		if v := resp.Header.Get(k); v != "" {
			t.Errorf("%s reached the UI: %q", k, v)
		}
	}
	for k, want := range map[string]string{
		"Content-Type": "application/json", "Cache-Control": "no-store", "Etag": `"v1"`,
		"X-Content-Type-Options": "nosniff", "Content-Security-Policy": "sandbox; default-src 'none'",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// A peer's body can't run on this node's origin: an attachment opened in
// a new tab is the UI's own origin. HTML, SVG, scripts and untyped bodies
// become downloads; data and images keep their type; all are nosniff and
// sandboxed.
func TestPeerBodiesCantRunHere(t *testing.T) {
	h, p := relayHarness(t)
	for _, tc := range []struct {
		ctype, disposition string
		wantType, wantDisp string
	}{
		{"text/html; charset=utf-8", `inline; filename="report.html"`, "application/octet-stream", `attachment; filename=report.html`},
		{"image/svg+xml", "", "application/octet-stream", "attachment"},
		{"application/javascript", "", "application/octet-stream", "attachment"},
		{"text/html", "", "application/octet-stream", "attachment"},
		{"", "", "application/octet-stream", "attachment"},
		{"image/png", `inline; filename="shot.png"`, "image/png", `inline; filename="shot.png"`},
		{"text/plain; charset=utf-8", "", "text/plain; charset=utf-8", ""},
		{"text/event-stream", "", "text/event-stream", ""},
	} {
		p.setAnswer(func(w http.ResponseWriter, r *http.Request) {
			w.Header()["Content-Type"] = []string{tc.ctype}
			if tc.disposition != "" {
				w.Header().Set("Content-Disposition", tc.disposition)
			}
			_, _ = w.Write([]byte("<script>fetch('/credentials')</script>"))
		})
		resp, err := noFollow.Get(h.srv.URL + "/sessions/ses_remote/attachments/att_1")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("Content-Type"); got != tc.wantType {
			t.Errorf("%q: Content-Type %q, want %q", tc.ctype, got, tc.wantType)
		}
		if got := resp.Header.Get("Content-Disposition"); got != tc.wantDisp {
			t.Errorf("%q: Content-Disposition %q, want %q", tc.ctype, got, tc.wantDisp)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(resp.Header.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%q: not nosniff and sandboxed: %v", tc.ctype, resp.Header)
		}
	}
}

// localFront serves h's mesh layer behind a stand-in for this node's own
// outer layers, which set frame protections on every response.
func localFront(t *testing.T, h *harness) *httptest.Server {
	t.Helper()
	inner := h.mgr.Handler(h.local)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A peer's informational answer (103 Early Hints with a Link preload and a
// cookie) is never relayed, and this node's own headers survive it.
func TestPeerInformationalAnswersAreNotRelayed(t *testing.T) {
	h, p := relayHarness(t)
	front := localFront(t, h)
	p.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "</credentials>; rel=preload")
		w.Header().Set("Set-Cookie", "mads=stolen; Path=/")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Del("Link")
		w.Header().Del("Set-Cookie")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	var mu sync.Mutex
	var got1xx []int
	trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, _ textproto.MIMEHeader) error {
		mu.Lock()
		got1xx = append(got1xx, code)
		mu.Unlock()
		return nil
	}}
	req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, front.URL+"/sessions/ses_remote/transcript", nil)
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(got1xx) != 0 {
		t.Fatalf("the UI got informational answers %v from the peer", got1xx)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Link") != "" {
		t.Fatalf("final answer: %d %v", resp.StatusCode, resp.Header)
	}
	csp := strings.Join(resp.Header.Values("Content-Security-Policy"), ", ")
	if resp.Header.Get("X-Frame-Options") != "DENY" || !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "sandbox") {
		t.Fatalf("this node's own headers didn't survive: %v", resp.Header)
	}
}

// Trailers are a peer's headers too: none reach the UI.
func TestPeerTrailersAreNotRelayed(t *testing.T) {
	h, p := relayHarness(t)
	p.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "Set-Cookie, X-Evil")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		w.Header().Set("Set-Cookie", "mads=stolen")
		w.Header().Set("X-Evil", "1")
		w.Header().Set(http.TrailerPrefix+"X-Late", "1")
	})
	resp, err := noFollow.Get(h.srv.URL + "/sessions/ses_remote/transcript")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if len(resp.Trailer) != 0 || resp.Header.Get("Trailer") != "" {
		t.Fatalf("trailers relayed: %v (announced %q)", resp.Trailer, resp.Header.Get("Trailer"))
	}
}
