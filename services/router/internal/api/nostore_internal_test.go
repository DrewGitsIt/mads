package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withNoStore classifies with the mesh layer in view: a path only the layer
// serves is API (no-store), the static UI keeps its own caching, and a
// header the answer already carries (a handler's, or a peer's on a
// forwarded answer, added the way ReverseProxy copies them) wins.
func TestWithNoStoreAndTheMeshLayer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte("ui"))
	})
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []string{})
	})
	layer := &Layer{
		Claims: func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/peers/") },
		Wrap: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/peers/list":
					writeJSON(w, http.StatusOK, []string{})
				case "/peers/p/attachments/a":
					w.Header().Add("Cache-Control", "private, max-age=31536000, immutable")
					_, _ = w.Write([]byte("bytes"))
				default:
					next.ServeHTTP(w, r)
				}
			})
		},
	}
	h := withNoStore(Classify(mux, layer), layer.Wrap(mux))
	for path, want := range map[string]string{
		"/sessions":              "no-store",
		"/peers/list":            "no-store",
		"/peers/p/attachments/a": "private, max-age=31536000, immutable",
		"/":                      "no-cache",
		"/assets/app.js":         "no-cache",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Values("Cache-Control"); len(got) != 1 || got[0] != want {
			t.Errorf("GET %s Cache-Control = %q, want just %q", path, got, want)
		}
	}
}
