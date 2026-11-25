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

// NewDoor is the Router's front door: the install token on every route but
// ExemptRoutes, a scoped token on ScopedRoutes only. classify is Classify:
// requests are classified with the mux's own pattern lookup, so an API
// route can never pass for the static UI.
func NewDoor(classify func(r *http.Request) string, tokens *localauth.Store, scoped *localauth.Registry) *localauth.Door {
	return localauth.NewDoor(localauth.DoorOptions{
		Name:         "router",
		Store:        tokens,
		Scoped:       scoped,
		Classify:     classify,
		Exempt:       ExemptRoutes,
		ScopedRoutes: ScopedRoutes,
	})
}

// LayerPattern is what Classify reports for a path a Layer claims that the
// route table has no route for: not exempt, not scoped, so the door wants
// the install token.
const LayerPattern = "layer"

// Layer is a handler between the front door and the route table — mesh
// routing (docs/MESH-MODE-DESIGN.md D4). It only ever sees requests the
// door has admitted.
type Layer struct {
	Wrap func(next http.Handler) http.Handler
	// Claims reports the paths Wrap may serve itself. One the route table
	// would leave to the static UI ("/", exempt) is classified LayerPattern
	// instead, so the door doesn't let it through untokened.
	Claims func(r *http.Request) bool
}

// Classify is the door's classifier: the pattern mux would serve r with,
// except that a path layer claims is never the static UI.
func Classify(mux *http.ServeMux, layer *Layer) func(r *http.Request) string {
	return func(r *http.Request) string {
		_, p := mux.Handler(r)
		if layer != nil && layer.Claims != nil && (p == "/" || p == "") && layer.Claims(r) {
			return LayerPattern
		}
		return p
	}
}

// NewHandler is New behind door: the Router's production stack, minus
// localguard (main.go puts that outermost). Order, outermost first: door,
// CORS, layer, route table. layer (may be nil) therefore only sees
// authenticated requests, and its own answers get the same CORS headers as
// the API's.
func NewHandler(opts Options, door func(classify func(r *http.Request) string) *localauth.Door, layer *Layer) http.Handler {
	allowed := opts.OriginAllowed
	if allowed == nil {
		allowed = func(string) bool { return false }
	}
	mux := NewMux(opts)
	var inner http.Handler = mux
	if layer != nil && layer.Wrap != nil {
		inner = layer.Wrap(mux)
	}
	classify := Classify(mux, layer)
	// no-store outside the layer, so its own answers (/peers/...) get it too.
	return door(classify).Wrap(withCORS(allowed, withNoStore(classify, inner)))
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
