package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/localauth"
)

// TestAdversarialRouteVariantsStayLocked (adversarial, front door D3):
// unclean, re-cased, re-methoded and encoded spellings of API routes, sent
// with no credential, never reach an API handler. Whatever ServeMux does
// with them (redirect, fall through to the static "/" pattern, 404, 405),
// no anonymous variant may answer 2xx, and every redirect must land on a
// door that still refuses.
func TestAdversarialRouteVariantsStayLocked(t *testing.T) {
	s := newRouterStack(t)
	srv := httptest.NewServer(s.h)
	t.Cleanup(srv.Close)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cases := []struct{ method, path string }{
		{"HEAD", "/sessions"},
		{"get", "/sessions"},
		{"Get", "/config"},
		{"GET", "//sessions"},
		{"GET", "/sessions/"},
		{"GET", "/./sessions"},
		{"GET", "/sessions/../config"},
		{"GET", "/static/../credentials"},
		{"GET", "/%73essions"},
		{"GET", "/sessions%2Fx"},
		{"GET", "/SESSIONS"},
		{"GET", "/config;x"},
		{"GET", "/config?x=/health"},
		{"GET", "/health/../config"},
		{"POST", "/health"},
		{"PUT", "/config"},
		{"PATCH", "/config"},
		{"OPTIONS", "/config"},
		{"GET", "/events"},
		{"GET", "/children"},
		{"GET", "/../../.mads/local.token"},
		{"GET", "/assets/../../local.token"},
	}
	for _, c := range cases {
		req, err := http.NewRequest(c.method, srv.URL+c.path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode/100 == 2 && c.method != "OPTIONS" {
			t.Errorf("%s %s anonymous: %d, want a refusal", c.method, c.path, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); resp.StatusCode/100 == 3 && loc != "" {
			req2, _ := http.NewRequest(http.MethodGet, srv.URL+loc, nil)
			if r2, err := noRedirect.Do(req2); err == nil {
				_ = r2.Body.Close()
				if r2.StatusCode/100 == 2 {
					t.Errorf("%s %s -> %s anonymous: %d", c.method, c.path, loc, r2.StatusCode)
				}
			}
		}
	}
	// A preflight is answered without a credential, but the real request
	// after it still needs one.
	pre, _ := http.NewRequest(http.MethodOptions, srv.URL+"/sessions", nil)
	pre.Header.Set("Origin", "http://localhost:17373")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	if r, err := noRedirect.Do(pre); err == nil {
		_ = r.Body.Close()
	}
	real, _ := http.NewRequest(http.MethodPost, srv.URL+"/sessions", strings.NewReader(`{"name":"x","provider":"fake"}`))
	real.Header.Set("Origin", "http://localhost:17373")
	real.Header.Set("Content-Type", "application/json")
	if r, err := noRedirect.Do(real); err != nil || r.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /sessions after a preflight, anonymous: %v %v", r.StatusCode, err)
	}
}

// TestAdversarialOnlyTheBearerIsACredential: a token by cookie, or under
// another scheme, is no credential at all (H7).
func TestAdversarialOnlyTheBearerIsACredential(t *testing.T) {
	s := newRouterStack(t)
	if w := s.do(t, "GET", "/sessions", withCookie(s.tokens.Token())); w.Code != http.StatusUnauthorized {
		t.Errorf("install token as a cookie: %d, want 401", w.Code)
	}
	// A scoped token by cookie is no credential at all.
	scoped, _ := s.auth.scoped.Mint("x1")
	if w := s.do(t, "GET", "/sessions/x1/orchestrate/summary", withCookie(scoped)); w.Code != http.StatusUnauthorized {
		t.Errorf("scoped token as a cookie: %d, want 401", w.Code)
	}
	// Non-bearer schemes don't fall back to the cookie or pass as a token.
	if w := s.do(t, "GET", "/sessions", func(r *http.Request) {
		r.Header.Set("Authorization", "Basic "+s.tokens.Token())
	}); w.Code != http.StatusUnauthorized {
		t.Errorf("Basic <install token>: %d, want 401", w.Code)
	}
}

// TestEventStreamEndsOnRotation (adversarial, front door D1; fixed by the
// round-1 stream revocation):
// the door used to be checked once, when a request started, so an
// EventSource opened before `mads token rotate` kept receiving every
// control-plane event afterwards. Now a stream opened with a credential
// ends when that credential is revoked: no post-rotation event reaches it.
// The same holds for proxied session output streams.
func TestEventStreamEndsOnRotation(t *testing.T) {
	s := newRouterStack(t)
	srv := httptest.NewServer(s.h)
	t.Cleanup(srv.Close)
	// What the app's window carries (H7): the install token, which
	// Electron's main process adds below the page.
	old := s.tokens.Token()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+old)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events with the token: %d", resp.StatusCode)
	}

	if _, err := localauth.Rotate(s.home); err != nil {
		t.Fatal(err)
	}
	// The old token is refused on a fresh request...
	if w := s.do(t, "GET", "/sessions", withBearer(old)); w.Code != http.StatusUnauthorized {
		t.Fatalf("old token after rotation: %d", w.Code)
	}
	// ...but an event appended after the rotation still reaches the old
	// stream.
	sess := contracts.Session{Name: "after-rotation-secret", Provider: "fake"}
	if err := s.data.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.Contains(sc.Text(), sess.ID) {
				got <- sc.Text()
				return
			}
		}
		close(got)
	}()
	select {
	case line, ok := <-got:
		if ok {
			t.Errorf("a stream opened with the pre-rotation token still delivers post-rotation events: %.200s", line)
		}
	case <-time.After(5 * time.Second):
		// Nothing arrived: the stream was cut or stopped feeding — the
		// expected behavior.
	}
}
