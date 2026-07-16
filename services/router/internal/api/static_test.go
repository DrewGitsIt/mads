package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/static"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// newTestServerWithUIDist is like newTestServer but wires static.New(distPath)
// as the staticHandler, for tests exercising the Router serving a real UI
// build end to end (proving API-route priority, not just internal/static's
// own file-serving logic in isolation).
func newTestServerWithUIDist(t *testing.T, distPath string) *httptest.Server {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	opts := testOptions(t, data, sup)
	opts.StaticHandler = static.New(distPath)
	opts.Registry = nil
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	return srv
}

// writeUIDist builds a minimal fake UI build under a fresh temp dir: an
// index.html (the SPA shell) and a real asset file, mirroring what `vite
// build` actually produces.
func writeUIDist(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><body>mads-ui-shell</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStaticServesIndexAtRoot(t *testing.T) {
	srv := newTestServerWithUIDist(t, writeUIDist(t))

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := string(body); got != "<html><body>mads-ui-shell</body></html>" {
		t.Fatalf("GET / body = %q, want the built index.html", got)
	}
}

func TestStaticServesRealAsset(t *testing.T) {
	srv := newTestServerWithUIDist(t, writeUIDist(t))

	resp, err := http.Get(srv.URL + "/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /assets/app.js status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := string(body); got != "console.log('hi')" {
		t.Fatalf("GET /assets/app.js body = %q, want the real asset, not swallowed by the fallback", got)
	}
}

func TestStaticSPAFallback(t *testing.T) {
	srv := newTestServerWithUIDist(t, writeUIDist(t))

	// "/some/unknown/route" isn't a file on disk and isn't a registered API
	// route either — it's a client-side route the SPA itself resolves — so
	// the Router must serve index.html rather than 404. (Not
	// "/sessions/abc123": that's now a real API route — GET /sessions/{id}.)
	// A browser-shaped Accept header is required to hit the fallback at all
	// (see TestAPIShapedUnregisteredPathGetsJSON404 for the non-navigation
	// case).
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/some/unknown/route", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /some/unknown/route status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := string(body); got != "<html><body>mads-ui-shell</body></html>" {
		t.Fatalf("GET /some/unknown/route body = %q, want the SPA shell (index.html)", got)
	}
}

func TestStaticDoesNotShadowAPIRoutes(t *testing.T) {
	// The static catch-all is registered on the same mux as every API
	// route — this proves the more specific method+path patterns still win
	// even with a real UI dist configured, not just when no staticHandler
	// is wired at all (the nil-defaults-to-404 case every other test here
	// already exercises implicitly).
	srv := newTestServerWithUIDist(t, writeUIDist(t))

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["service"] != "router" {
		t.Fatalf("GET /health body = %v, want the health JSON, not the UI shell", body)
	}
}

func TestAPIShapedUnregisteredPathGetsJSON404(t *testing.T) {
	// A missing/typo'd API route, called the way the UI's fetch client
	// actually calls it (Accept: application/json), must not be masked as a
	// 200 text/html SPA shell — that's the bug this whole change fixes.
	srv := newTestServerWithUIDist(t, writeUIDist(t))

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/sessions/nope/what", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /sessions/nope/what status = %d, want 404", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "not found" || body["path"] != "/sessions/nope/what" {
		t.Fatalf("GET /sessions/nope/what body = %v, want {error: not found, path: /sessions/nope/what}", body)
	}
}

func TestBrowserNavigationToDeepLinkGetsIndex(t *testing.T) {
	// A real browser navigating to a client-side SPA route (full navigation
	// Accept header, no file extension) must still get index.html, not a
	// JSON 404.
	srv := newTestServerWithUIDist(t, writeUIDist(t))

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/deep/link", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /deep/link status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := string(body); got != "<html><body>mads-ui-shell</body></html>" {
		t.Fatalf("GET /deep/link body = %q, want the SPA shell (index.html)", got)
	}
}
