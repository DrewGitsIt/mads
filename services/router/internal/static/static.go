// Package static serves the built UI (ui/dist) at the Router's root so the
// Electron shell and the Router's API share one origin. Kept separate from
// internal/api because it has nothing to do with the JSON API contract — no
// dataclient, no JSON, just files.
package static

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
)

// New serves distDir (typically ui/dist) at "/", falling back to index.html
// for any request path that isn't a real file under distDir — the built SPA
// owns client-side routing for everything past that.
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
		path := filepath.Join(distDir, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			http.ServeFile(w, r, index)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
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
