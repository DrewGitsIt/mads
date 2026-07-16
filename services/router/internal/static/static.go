// Package static serves the built UI (ui/dist) at the Router's root so the
// Electron shell and the Router's API share one origin. Kept separate from
// internal/api because it has nothing to do with the JSON API contract — no
// dataclient, just file serving (plus a small JSON 404 for non-navigation
// requests that reach the fallback, see New).
package static

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// New serves distDir (typically ui/dist) at "/". A request for a real file
// under distDir gets that file; a navigation-shaped request (GET/HEAD, no
// file extension, Accept prefers text/html) for anything else falls back to
// index.html so the built SPA owns client-side routing. Everything else that
// reaches the fallback — a wrong method, a non-html Accept (e.g. a bare
// fetch()), or an extensioned path with no matching file — is a caller
// probing for a resource, not a browser navigating, and gets a JSON 404
// instead of silently 200'ing as the SPA shell (see isNavigationRequest).
//
// Whether the UI is built is checked on every request, not cached once at
// construction: scripts/app.sh starts the Router before running `npm
// --prefix ui start` (which builds ui/dist), and the UI rebuilds
// independently of the Go binary at any later point too, so a handler that
// only checked once at construction would keep serving a stale "not built"
// placeholder forever once a build actually landed. A missing/unbuilt dist
// directory never fails Router startup — a fresh checkout with no UI build
// yet must still bring up the Router for API use.
func New(distDir string) http.Handler {
	if distDir == "" {
		log.Printf("static: no UI dist path configured, serving placeholder page")
		return http.HandlerFunc(serveNotBuilt)
	}
	fileServer := http.FileServer(http.Dir(distDir))
	index := filepath.Join(distDir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info, err := os.Stat(index); err != nil || info.IsDir() {
			serveNotBuilt(w, r)
			return
		}
		// filepath.Clean strips any ".." that would otherwise escape
		// distDir before it's joined in — a rooted path (r.URL.Path always
		// starts with "/") can't climb above root.
		fsPath := filepath.Join(distDir, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(fsPath); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		if isNavigationRequest(r) {
			http.ServeFile(w, r, index)
			return
		}
		writeNotFoundJSON(w, r)
	})
}

// isNavigationRequest reports whether r looks like a browser navigating to a
// client-side SPA route, as opposed to a caller (fetch, curl, a missing/
// typo'd API route) probing for a resource. Only GET/HEAD requests for an
// extensionless path whose Accept header explicitly names text/html
// qualify — a bare fetch() (Accept defaults to "*/*", not "text/html") or a
// request for a path with an extension never does, so an absent API route
// 404s as JSON instead of silently 200'ing as the SPA shell.
func isNavigationRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if filepath.Ext(r.URL.Path) != "" {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func writeNotFoundJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found", "path": r.URL.Path})
}

const notBuiltPage = `<!doctype html>
<html>
<head><meta charset="utf-8"><title>mads</title></head>
<body style="font-family: sans-serif; padding: 3rem;">
<h1>UI not built</h1>
<p>Run <code>npm --prefix ui run build</code> (or <code>make build</code>) and reload this page — no Router restart needed.</p>
</body>
</html>`

func serveNotBuilt(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(notBuiltPage))
}
