package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/localauth"
)

// ExemptRoutes are the Router patterns served without the install token
// (docs/LOCAL-FRONT-DOOR-DESIGN.md D3): the static UI (public code, no
// data) and GET /health (service and status only, unless the caller holds
// the token).
var ExemptRoutes = []string{"/", "GET /health"}

// ScopedRoutes are the only patterns a session-scoped token
// (MADS_ROUTER_TOKEN, D4) reaches: its own session's orchestrate routes,
// and a message over a link it granted. Each handler is wrapped in
// ownSession or ownLink, which hold the token to its own session.
var ScopedRoutes = []string{
	"GET /sessions/{id}/orchestrate/subjects",
	"GET /sessions/{id}/orchestrate/summary",
	"GET /sessions/{id}/orchestrate/turns",
	"POST /sessions/{id}/orchestrate/attention",
	"POST /sessions/{id}/orchestrate/mailbox",
	"POST /links/{id}/orchestrate/message",
}

// NewDoor is the Router's front door over mux: the install token on every
// route but ExemptRoutes, a scoped token on ScopedRoutes only. Requests are
// classified with mux's own pattern lookup, so an API route can never pass
// for the static UI.
func NewDoor(mux *http.ServeMux, tokens *localauth.Store, scoped *localauth.Registry) *localauth.Door {
	return localauth.NewDoor(localauth.DoorOptions{
		Name:         "router",
		Store:        tokens,
		Scoped:       scoped,
		Classify:     func(r *http.Request) string { _, p := mux.Handler(r); return p },
		Exempt:       ExemptRoutes,
		ScopedRoutes: ScopedRoutes,
	})
}

// NewHandler is New behind door: the Router's production stack, minus
// localguard (main.go puts that outermost).
func NewHandler(opts Options, door func(mux *http.ServeMux) *localauth.Door) http.Handler {
	allowed := opts.OriginAllowed
	if allowed == nil {
		allowed = func(string) bool { return false }
	}
	mux := NewMux(opts)
	return door(mux).Wrap(withCORS(allowed, mux))
}

// ownSession holds a scoped caller to its own session: the path's {id}
// must be the token's session. Install-token callers (the user) pass.
func (s *server) ownSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := localauth.FromContext(r.Context())
		if p.Kind == localauth.Scoped && r.PathValue("id") != p.SessionID {
			localauth.WriteForbidden(w, "this token belongs to another session")
			return
		}
		next(w, r)
	}
}

// ownLink holds a scoped caller to links its session granted.
func (s *server) ownLink(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := localauth.FromContext(r.Context())
		if p.Kind == localauth.Scoped {
			link, err := s.data.Link(r.Context(), r.PathValue("id"))
			if err != nil {
				writeError(w, err)
				return
			}
			if link.GrantingSessionID != p.SessionID {
				localauth.WriteForbidden(w, "this token's session did not grant that link")
				return
			}
		}
		next(w, r)
	}
}
