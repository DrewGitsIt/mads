package api

import (
	"net/http"
)

// withCORS lets an allowed browser page — the Vite dev server's
// (localhost:5173) calling the Router (localhost:17373) — read the Router's
// answers without --disable-web-security. Which pages is localguard's list
// (allowed), not any localhost port: a dev server an agent starts is a
// localhost page too, and must not get the Router's API. Other origins get
// no CORS headers at all; localguard refuses them before they get here.
// OPTIONS is intercepted for every request because the mux below has no
// route registered for it.
func withCORS(allowed func(origin string) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Mads-Actor")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
