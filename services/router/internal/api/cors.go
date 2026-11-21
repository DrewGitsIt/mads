package api

import (
	"net/http"
)

// withCORS lets an allowed browser page — the Vite dev server's
// (localhost:5173, when the Router is started with -allowed-origins) calling
// the Router (localhost:17373) — read the Router's answers without
// --disable-web-security. Which pages is localguard's list
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
			// /export's filename: the UI fetches the file and names the save from it.
			w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withNoStore marks every API answer Cache-Control: no-store unless its
// handler says otherwise (the SSE streams' no-cache, an attachment's
// immutable, a peer's own header on an answer the mesh layer forwards). The
// Router's JSON is live state: a browser HTTP cache entry for it is never
// right, and Chromium queues a second identical GET behind one still in
// flight while it decides whether it may reuse that answer (two views
// asking for one session's /changeset waited on each other). The static UI
// (the "/" catch-all, as classify names it) keeps its own caching; a path
// only the mesh layer serves is API like the rest.
//
// The default goes on as the answer's header is written, so whatever set
// one first, the handler or a proxied response's headers, wins.
func withNoStore(classify func(r *http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if classify(r) == "/" {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&noStoreWriter{ResponseWriter: w}, r)
	})
}

// noStoreWriter adds Cache-Control: no-store when the header is written
// without one.
type noStoreWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *noStoreWriter) WriteHeader(code int) {
	if !w.wrote {
		w.wrote = true
		if w.Header().Get("Cache-Control") == "" {
			w.Header().Set("Cache-Control", "no-store")
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *noStoreWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush keeps SSE streaming through the wrapper.
func (w *noStoreWriter) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection underneath.
func (w *noStoreWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
