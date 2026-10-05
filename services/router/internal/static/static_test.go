package static_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/services/router/internal/static"
)

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

func get(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// getWithAccept is like get but sets an explicit Accept header, needed to
// exercise the navigation-shaped gate: only a request that prefers
// text/html falls back to index.html.
func getWithAccept(t *testing.T, srv *httptest.Server, path, accept string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", accept)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestServesIndexAtRoot(t *testing.T) {
	srv := httptest.NewServer(static.New(writeUIDist(t)))
	defer srv.Close()

	status, body := getWithAccept(t, srv, "/", "text/html")
	if status != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", status)
	}
	if body != "<html><body>mads-ui-shell</body></html>" {
		t.Fatalf("GET / body = %q, want the built index.html", body)
	}
}

// TestServesIndexAtRootWithoutHTMLAccept covers curl/health-check-shaped
// requests (default Accept "*/*", the same header a plain "curl
// localhost:17373/" sends): "/" must 200 with the built index.html the same
// as a browser navigation, not fall through to isNavigationRequest's
// Accept-sniffing 404 meant for disambiguating unknown routes.
func TestServesIndexAtRootWithoutHTMLAccept(t *testing.T) {
	srv := httptest.NewServer(static.New(writeUIDist(t)))
	defer srv.Close()

	status, body := getWithAccept(t, srv, "/", "*/*")
	if status != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", status)
	}
	if body != "<html><body>mads-ui-shell</body></html>" {
		t.Fatalf("GET / body = %q, want the built index.html", body)
	}
}

func TestServesRealAssetVerbatim(t *testing.T) {
	srv := httptest.NewServer(static.New(writeUIDist(t)))
	defer srv.Close()

	status, body := get(t, srv, "/assets/app.js")
	if status != http.StatusOK {
		t.Fatalf("GET /assets/app.js status = %d, want 200", status)
	}
	if body != "console.log('hi')" {
		t.Fatalf("GET /assets/app.js body = %q, want the real asset, not swallowed by the fallback", body)
	}
}

func TestUnknownPathFallsBackToIndex(t *testing.T) {
	srv := httptest.NewServer(static.New(writeUIDist(t)))
	defer srv.Close()

	status, body := getWithAccept(t, srv, "/some/unknown/route", "text/html")
	if status != http.StatusOK {
		t.Fatalf("GET /some/unknown/route status = %d, want 200", status)
	}
	if body != "<html><body>mads-ui-shell</body></html>" {
		t.Fatalf("GET /some/unknown/route body = %q, want the SPA shell (index.html)", body)
	}
}

func TestNonHTMLAcceptGetsJSON404(t *testing.T) {
	srv := httptest.NewServer(static.New(writeUIDist(t)))
	defer srv.Close()

	status, body := getWithAccept(t, srv, "/sessions/nope/what", "application/json")
	if status != http.StatusNotFound {
		t.Fatalf("GET /sessions/nope/what status = %d, want 404", status)
	}
	want := `{"error":"not found","path":"/sessions/nope/what"}` + "\n"
	if body != want {
		t.Fatalf("GET /sessions/nope/what body = %q, want %q", body, want)
	}
}

func TestBareFetchDefaultAcceptGetsJSON404(t *testing.T) {
	srv := httptest.NewServer(static.New(writeUIDist(t)))
	defer srv.Close()

	status, body := getWithAccept(t, srv, "/some/unknown/route", "*/*")
	if status != http.StatusNotFound {
		t.Fatalf("GET /some/unknown/route status = %d, want 404", status)
	}
	want := `{"error":"not found","path":"/some/unknown/route"}` + "\n"
	if body != want {
		t.Fatalf("GET /some/unknown/route body = %q, want %q", body, want)
	}
}

func TestExtensionedPathWithHTMLAcceptStillGetsJSON404(t *testing.T) {
	srv := httptest.NewServer(static.New(writeUIDist(t)))
	defer srv.Close()

	status, body := getWithAccept(t, srv, "/assets/missing.js", "text/html")
	if status != http.StatusNotFound {
		t.Fatalf("GET /assets/missing.js status = %d, want 404", status)
	}
	want := `{"error":"not found","path":"/assets/missing.js"}` + "\n"
	if body != want {
		t.Fatalf("GET /assets/missing.js body = %q, want %q", body, want)
	}
}

func TestPostToUnknownPathGetsJSON404(t *testing.T) {
	srv := httptest.NewServer(static.New(writeUIDist(t)))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/some/unknown/route", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /some/unknown/route status = %d, want 404", resp.StatusCode)
	}
	want := `{"error":"not found","path":"/some/unknown/route"}` + "\n"
	if string(body) != want {
		t.Fatalf("POST /some/unknown/route body = %q, want %q", body, want)
	}
}

func TestPlaceholderWhenDistMissing(t *testing.T) {
	srv := httptest.NewServer(static.New(filepath.Join(t.TempDir(), "does-not-exist")))
	defer srv.Close()

	status, body := get(t, srv, "/")
	if status != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200 (placeholder, not a failure)", status)
	}
	if !strings.Contains(body, "UI not built") {
		t.Fatalf("GET / body = %q, want a placeholder page mentioning \"UI not built\"", body)
	}
}

func TestPlaceholderWhenDistPathEmpty(t *testing.T) {
	srv := httptest.NewServer(static.New(""))
	defer srv.Close()

	status, body := get(t, srv, "/")
	if status != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", status)
	}
	if !strings.Contains(body, "UI not built") {
		t.Fatalf("GET / body = %q, want a placeholder page mentioning \"UI not built\"", body)
	}
}

func TestPicksUpBuildThatLandsAfterConstruction(t *testing.T) {
	// scripts/app.sh starts the Router (and constructs static.New) before
	// running `npm --prefix ui start` (which builds ui/dist) — New must
	// start serving the real UI as soon as the build lands, not require a
	// Router restart.
	dir := t.TempDir()
	srv := httptest.NewServer(static.New(dir))
	defer srv.Close()

	if status, body := get(t, srv, "/"); status != http.StatusOK || !strings.Contains(body, "UI not built") {
		t.Fatalf("GET / before build lands = %d %q, want the placeholder page", status, body)
	}

	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><body>mads-ui-shell</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, body := getWithAccept(t, srv, "/", "text/html")
	if status != http.StatusOK {
		t.Fatalf("GET / after build lands status = %d, want 200", status)
	}
	if body != "<html><body>mads-ui-shell</body></html>" {
		t.Fatalf("GET / after build lands body = %q, want the built index.html", body)
	}
}

// A rebuilt UI must reach a window that has already loaded an older one: the
// shell (index.html), wherever it is served from, is always rechecked, while
// the content-hashed bundles under assets/ are safe to keep forever.
func TestCacheControl(t *testing.T) {
	srv := httptest.NewServer(static.New(writeUIDist(t)))
	defer srv.Close()

	cases := []struct {
		path, accept, want string
	}{
		{"/", "", "no-cache"},
		{"/sessions/ses_1", "text/html", "no-cache"},
		{"/index.html", "", "no-cache"},
		{"/assets/app.js", "", "public, max-age=31536000, immutable"},
	}
	for _, c := range cases {
		req, err := http.NewRequest(http.MethodGet, srv.URL+c.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if c.accept != "" {
			req.Header.Set("Accept", c.accept)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("Cache-Control"); got != c.want {
			t.Errorf("GET %s Cache-Control = %q, want %q", c.path, got, c.want)
		}
	}
}
