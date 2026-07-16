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

func TestServesIndexAtRoot(t *testing.T) {
	srv := httptest.NewServer(static.New(writeUIDist(t)))
	defer srv.Close()

	status, body := get(t, srv, "/")
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

	status, body := get(t, srv, "/some/unknown/route")
	if status != http.StatusOK {
		t.Fatalf("GET /some/unknown/route status = %d, want 200", status)
	}
	if body != "<html><body>mads-ui-shell</body></html>" {
		t.Fatalf("GET /some/unknown/route body = %q, want the SPA shell (index.html)", body)
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

	status, body := get(t, srv, "/")
	if status != http.StatusOK {
		t.Fatalf("GET / after build lands status = %d, want 200", status)
	}
	if body != "<html><body>mads-ui-shell</body></html>" {
		t.Fatalf("GET / after build lands body = %q, want the built index.html", body)
	}
}
