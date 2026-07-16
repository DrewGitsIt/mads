package api

import (
	"net/http"
	"regexp"
)

// localhostOrigin matches http(s)://localhost or 127.0.0.1 at any port —
// the Vite dev server's origin, whatever port it happens to be on.
var localhostOrigin = regexp.MustCompile(`^https?://(localhost|127\.0\.0\.1)(:[0-9]+)?$`)

// withCORS lets a browser page served from the Vite dev server
// (localhost:5173) call the Router (localhost:8080) without
// --disable-web-security. Non-localhost origins get no CORS headers at all —
// this is not a wildcard policy. OPTIONS is intercepted for every request
// (matching or not) because the mux below has no route registered for it;
// a non-matching origin still gets a bare 204 with no ACAO header, which is
// what causes the browser to block the real request.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if localhostOrigin.MatchString(origin) {
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
