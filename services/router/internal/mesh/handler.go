package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/router/internal/safeserve"
)

// reservedNames can't be peer names: they're the Router's own /peers/...
// routes.
var reservedNames = map[string]bool{"candidates": true}

// Handler wraps the Router's API with mesh routing (D4):
//
//   - /peers, /peers/candidates, /peers/{peer}: the peer list ("devices I
//     can view"), served here.
//   - /peers/{peer}/<path>: forwarded to that peer's Mesh Service.
//   - /sessions/{id}[/...] for a session that lives on a peer: forwarded
//     there, so the conversation view needs no idea where a session runs.
//   - GET /sessions?scope=all: this node's sessions plus every visible
//     peer's, each row tagged with "host".
//   - GET /links?session_ids= naming sessions that live on peers: this
//     node's links plus each owning peer's (serveLinks).
//   - GET /events?session_id= for a session that lives on a peer, as a
//     plain JSON read (not the SSE stream): forwarded there, like
//     /sessions/{id}/..., since that session's events are in its host's
//     log. The UI's live stream for such a session is
//     /peers/{peer}/events (ui/src/router/events.ts).
//
// Everything else, and everything when no peer is registered, goes to next
// untouched.
//
// The Handler sits inside the Router's front door
// (docs/LOCAL-FRONT-DOOR-DESIGN.md): it only sees requests the door has
// admitted, with the install token already removed. Claims tells the door
// which paths are served here, so the door never mistakes one for the
// static UI (its one exempt catch-all). Only the user — the install token's
// holder — reaches a peer: a session-scoped token (an agent's orchestrate
// MCP server) is refused here even on its scoped routes, since a session on
// a peer is never its own.
func (m *Manager) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// A request a peer's Mesh Service relayed (its header present, even
		// empty) is this node's to answer alone: it is never fanned out or
		// forwarded to this node's own peers (a loop between two nodes that
		// view each other, and relaying C through A to B, D5). It also
		// carries the mark, and the device's authenticated actor, into
		// every Data Service write made for it, so the Data Service never
		// takes it for this host's user (dataclient.WithMeshRelay; Q2, D8).
		if node, relayed := r.Header[contracts.MeshRelayHeader]; relayed {
			ctx := dataclient.WithMeshRelay(r.Context(), strings.Join(node, ","))
			if actor := r.Header.Get("X-Mads-Actor"); actor != "" {
				ctx = dataclient.WithMeshActor(ctx, actor)
			}
			if fp := r.Header.Get(contracts.MeshDeviceHeader); fp != "" {
				ctx = dataclient.WithMeshDevice(ctx, fp)
			}
			r = r.WithContext(ctx)
			if isPeersPath(path) {
				// The Mesh Service never forwards these (authz: Deny); a
				// marked request that gets here anyway still can't use them.
				writeError(w, http.StatusForbidden, "relayed", "a request relayed from another device can't reach this device's peers")
				return
			}
			if path == "/sessions" && r.URL.Query().Has("scope") {
				// This node's sessions only, as without scope.
				q := r.URL.Query()
				q.Del("scope")
				r.URL.RawQuery = q.Encode()
			}
			m.serveLocal(w, r, next)
			return
		}
		switch {
		case isPeersPath(path):
			if !m.admit(w, r) {
				return
			}
			m.servePeers(w, r)
			return
		case path == "/sessions" && r.Method == http.MethodGet && r.URL.Query().Get("scope") == "all":
			if !m.admit(w, r) {
				return
			}
			m.serveAllSessions(w, r, next)
			return
		case path == "/links" && r.Method == http.MethodGet && m.HasPeers():
			if m.serveLinks(w, r, next) {
				return
			}
		}
		if id := remoteEventsSession(r); id != "" && m.HasPeers() {
			if p, ok := m.ownerOf(r.Context(), id); ok {
				if !m.admit(w, r) {
					return
				}
				m.forward(w, r, p, path)
				return
			}
		}
		if id := sessionIDOf(path); id != "" && m.HasPeers() {
			if p, ok := m.ownerOf(r.Context(), id); ok {
				if !m.admit(w, r) {
					return
				}
				m.forward(w, r, p, path)
				return
			}
		}
		m.serveLocal(w, r, next)
	})
}

// serveLocal is next, this node's own API.
func (m *Manager) serveLocal(w http.ResponseWriter, r *http.Request, next http.Handler) {
	next.ServeHTTP(w, r)
	// A local session deleted: links reads go back to asking the index
	// about its id, as ownerOf now does.
	if path := r.URL.Path; r.Method == http.MethodDelete && path == "/sessions/"+sessionIDOf(path) {
		m.forgetLocal(sessionIDOf(path))
	}
}

// Claims reports whether Handler may serve r itself rather than pass it to
// the Router's route table: the peer list and explicit peer proxy, and any
// session path (a session on a peer may have routes this build doesn't).
// The front door requires the install token on every claimed path, even
// one the route table would have left to the static UI.
func Claims(r *http.Request) bool {
	return isPeersPath(r.URL.Path) || sessionIDOf(r.URL.Path) != ""
}

func isPeersPath(path string) bool {
	return path == "/peers" || strings.HasPrefix(path, "/peers/")
}

// admit refuses a session-scoped caller: mesh routing is the user's alone.
func (m *Manager) admit(w http.ResponseWriter, r *http.Request) bool {
	if localauth.FromContext(r.Context()).Kind == localauth.Scoped {
		localauth.WriteForbidden(w, "a session-scoped token can't reach other devices")
		return false
	}
	return true
}

// sessionIDOf returns the {id} of /sessions/{id} and /sessions/{id}/...,
// or "" for anything else (including /sessions/defaults and
// /sessions/validate, which are collection routes).
func sessionIDOf(path string) string {
	rest, ok := strings.CutPrefix(path, "/sessions/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	if !strings.HasPrefix(id, "ses_") {
		return ""
	}
	return id
}

// remoteEventsSession is the session a plain JSON GET /events?session_id=
// reads (provisioning, the pipeline fold, delegate paging), or "". The SSE
// stream (Accept: text/event-stream) is always this node's own: a peer
// session's stream is read through /peers/{peer}/events, whose bus keeps
// its own cursor.
func remoteEventsSession(r *http.Request) string {
	if r.URL.Path != "/events" || r.Method != http.MethodGet || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		return ""
	}
	id := r.URL.Query().Get("session_id")
	if !strings.HasPrefix(id, "ses_") {
		return ""
	}
	return id
}

// ownerOf finds the peer that owns a session id that isn't this node's.
// Local always wins: an id the local Data Service knows is never routed
// away, whatever a peer claims.
func (m *Manager) ownerOf(ctx context.Context, id string) (contracts.Peer, bool) {
	if p, ok := m.Owner(id); ok {
		if _, err := m.data.Session(ctx, id); errors.Is(err, dataclient.ErrNotFound) {
			return p, true
		}
		return contracts.Peer{}, false
	}
	if _, err := m.data.Session(ctx, id); !errors.Is(err, dataclient.ErrNotFound) {
		return contracts.Peer{}, false // local, or the Data Service is down: let the API answer
	}
	return m.Resolve(ctx, id)
}

// --- Peer list ------------------------------------------------------------

func (m *Manager) servePeers(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/peers"), "/")
	name, sub, hasSub := strings.Cut(rest, "/")
	switch {
	case rest == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, m.Statuses())
	case rest == "" && r.Method == http.MethodPost:
		m.addPeer(w, r)
	case rest == "candidates" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, m.Candidates(r.Context()))
	case !hasSub && r.Method == http.MethodGet:
		st, ok := m.Status(name)
		if !ok {
			writeError(w, http.StatusNotFound, "peer_not_found", "no peer named "+name)
			return
		}
		writeJSON(w, http.StatusOK, st)
	case !hasSub && r.Method == http.MethodPut:
		m.updatePeer(w, r, name)
	case !hasSub && r.Method == http.MethodDelete:
		m.deletePeer(w, r, name)
	case hasSub:
		p, ok := m.peer(name)
		if !ok {
			writeError(w, http.StatusNotFound, "peer_not_found", "no peer named "+name)
			return
		}
		m.forward(w, r, p, "/"+sub)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method+" "+r.URL.Path)
	}
}

// AddPeerRequest is POST /peers: a device by MagicDNS name (the mesh URL is
// derived) or by explicit Mesh Service URL, and the fingerprint of its
// device key (`mads device` on it prints it), which every connection to it
// is pinned to. Without one, the request must name a device already paired
// here, whose pinned key is used.
type AddPeerRequest struct {
	Name        string `json:"name,omitempty"`
	DNSName     string `json:"dns_name,omitempty"`
	MeshURL     string `json:"mesh_url,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// DefaultMeshPort is the peer Mesh Service's port, the one the tailnet
// grant opens.
const DefaultMeshPort = "17380"

func (m *Manager) addPeer(w http.ResponseWriter, r *http.Request) {
	var req AddPeerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	meshURL := strings.TrimSuffix(req.MeshURL, "/")
	if meshURL == "" {
		dns := strings.TrimSuffix(req.DNSName, ".")
		if dns == "" {
			writeError(w, http.StatusUnprocessableEntity, "bad_request", "dns_name or mesh_url is required")
			return
		}
		meshURL = "http://" + dns + ":" + DefaultMeshPort
	}
	// Stored as http://host:port whatever was typed (dial.go meshHost): it
	// is always dialled with TLS.
	meshURL = strings.Replace(meshURL, "https://", "http://", 1)
	// The key to pin: the one given, else the one already pinned for this
	// name. Without a key there is nothing to verify the device against,
	// so it can't be added (no unverified connection, ever).
	var pin string
	if req.Fingerprint != "" {
		fp, err := devicekey.ParseFingerprint(req.Fingerprint)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "bad_fingerprint", err.Error())
			return
		}
		pin = string(fp)
	} else if req.Name != "" {
		if cur, err := m.data.Peer(r.Context(), req.Name); err == nil {
			pin = cur.Fingerprint
		}
	}
	if pin == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code": "peer_" + string(contracts.PeerUnpaired), "reachability": contracts.PeerUnpaired,
			"error": "can't add " + meshURL + " without its device key: run `mads device` on it, then add it with that fingerprint " +
				"(or here: mads pair --accept <fingerprint> --name <name> --view --mesh-url " + meshURL + ")",
		})
		return
	}
	// Vet before saving: a device only joins the list once its Mesh
	// Service answers, proves the pinned key, and lets this device view it.
	probe := m.ProbePeer(r.Context(), contracts.Peer{Name: req.Name, MeshURL: meshURL, Fingerprint: pin})
	if probe.State != contracts.PeerConnected {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code": "peer_" + string(probe.State), "error": "can't add " + meshURL + ": " + probe.Reason,
			"reachability": probe.State,
		})
		return
	}
	p := contracts.Peer{
		Name:        firstNonEmpty(req.Name, probe.Hello.Node),
		DNSName:     firstNonEmpty(strings.TrimSuffix(req.DNSName, "."), probe.Hello.DNSName),
		MeshURL:     meshURL,
		Fingerprint: pin,
	}
	if reservedNames[p.Name] {
		writeError(w, http.StatusUnprocessableEntity, "reserved_name", p.Name+" is reserved; pass another name")
		return
	}
	// A device already paired here under this name, but not viewed, gains
	// the viewed side on its row — only if it is that device: the TLS
	// handshake just proved the key that row pins. The name alone can come
	// from the remote's own hello, so it proves nothing.
	var merged contracts.Peer
	err := m.editPeer(r.Context(), p.Name, func(cur *contracts.Peer) (bool, error) {
		if !cur.IsPaired() || cur.IsViewed() || cur.Fingerprint != pin {
			return false, errNotEditable
		}
		yes := true
		cur.Viewed, cur.MeshURL, cur.DNSName, cur.Hidden = &yes, p.MeshURL, p.DNSName, false
		merged = *cur
		return false, nil
	})
	switch {
	case err == nil:
		p = merged
	case errors.Is(err, errNotEditable) || errors.Is(err, dataclient.ErrNotFound):
		// Built from the request alone: a new row, pinned to the key just
		// verified, granted nothing here (a clash with an existing name or
		// fingerprint is the Data Service's 422, as before).
		p.Pairing = &contracts.PeerPairing{How: contracts.PairAccept}
		err = m.data.CreatePeer(r.Context(), &p)
	}
	if err != nil {
		writeDataError(w, err)
		return
	}
	// Don't wait for the event round trip: the caller should see the new
	// peer, connected, in its very next GET /peers.
	if err := m.Reload(r.Context()); err == nil {
		m.observe(r.Context(), p.ID, probe)
	}
	st, _ := m.Status(p.ID)
	writeJSON(w, http.StatusCreated, st)
}

type userKey struct{}

// asUser marks ctx as this host's own user acting directly (the install
// token, not relayed): the writes made for it record actor "user", not
// "router" (a device's relayed writes record the device either way).
func asUser(ctx context.Context) context.Context { return context.WithValue(ctx, userKey{}, true) }

// client is the Data Service client for a write made under ctx.
func (m *Manager) client(ctx context.Context) *dataclient.Client {
	if ok, _ := ctx.Value(userKey{}).(bool); ok {
		return m.data.As("user")
	}
	return m.data
}

// errNotEditable is an editPeer edit declining the row it was given.
var errNotEditable = errors.New("not editable here")

// editPeer applies edit to the Data Service's current copy of a peer (by
// id or name) and writes it back conditionally, so a change made meanwhile
// — `mads pair` accepting or revoking it — is never overwritten from this
// Router's in-memory copy. edit returns true to delete the row instead.
// One retry on a fresh read if the row changed between read and write.
func (m *Manager) editPeer(ctx context.Context, idOrName string, edit func(*contracts.Peer) (remove bool, err error)) error {
	for attempt := 0; ; attempt++ {
		cur, err := m.data.Peer(ctx, idOrName)
		if err != nil {
			return err
		}
		remove, err := edit(&cur)
		if err != nil {
			return err
		}
		if remove {
			err = m.client(ctx).DeletePeerIfUnchanged(ctx, cur)
		} else {
			err = m.client(ctx).UpdatePeerIfUnchanged(ctx, &cur)
		}
		if !errors.Is(err, dataclient.ErrConflict) || attempt > 0 {
			return err
		}
	}
}

func (m *Manager) updatePeer(w http.ResponseWriter, r *http.Request, name string) {
	known, ok := m.peer(name)
	if !ok {
		writeError(w, http.StatusNotFound, "peer_not_found", "no peer named "+name)
		return
	}
	// Only the presentational fields are editable; moving a peer to
	// another URL is remove + add, so it gets re-vetted.
	var patch struct {
		Name   *string `json:"name"`
		Hidden *bool   `json:"hidden"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if patch.Name != nil && reservedNames[*patch.Name] {
		writeError(w, http.StatusUnprocessableEntity, "reserved_name", *patch.Name+" is reserved")
		return
	}
	if err := m.editPeer(r.Context(), known.ID, func(cur *contracts.Peer) (bool, error) {
		if patch.Name != nil {
			cur.Name = *patch.Name
		}
		if patch.Hidden != nil {
			cur.Hidden = *patch.Hidden
		}
		return false, nil
	}); err != nil {
		writeDataError(w, err)
		return
	}
	_ = m.Reload(r.Context())
	st, _ := m.Status(known.ID)
	writeJSON(w, http.StatusOK, st)
}

func (m *Manager) deletePeer(w http.ResponseWriter, r *http.Request, name string) {
	known, ok := m.peer(name)
	if !ok {
		writeError(w, http.StatusNotFound, "peer_not_found", "no peer named "+name)
		return
	}
	// Removing a device from this list stops viewing it. A device that is
	// also paired keeps its pairing (unpairing is `mads pair --revoke`), so
	// its row stays, no longer viewed. Decided on the Data Service's
	// current row, not this Router's copy: a pairing made a moment ago
	// must not be deleted with it.
	err := m.editPeer(r.Context(), known.ID, func(cur *contracts.Peer) (bool, error) {
		if !cur.IsPaired() {
			return true, nil
		}
		no := false
		cur.Viewed = &no
		return false, nil
	})
	if errors.Is(err, dataclient.ErrNotFound) {
		err = nil // already gone
	}
	if err != nil {
		writeDataError(w, err)
		return
	}
	_ = m.Reload(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// --- scope=all ------------------------------------------------------------

// serveAllSessions merges this node's GET /sessions with every visible
// peer's. A peer that can't be read right now contributes its last-known
// rows marked unreachable, so its sessions go stale rather than vanish.
func (m *Manager) serveAllSessions(w http.ResponseWriter, r *http.Request, next http.Handler) {
	q := r.URL.Query()
	q.Del("scope")
	rawQuery := q.Encode()

	local := r.Clone(r.Context())
	local.URL.RawQuery = rawQuery
	local.RequestURI = ""
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, local)
	if rec.Code != http.StatusOK {
		copyRecorded(w, rec)
		return
	}
	var localRows []json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &localRows); err != nil {
		copyRecorded(w, rec)
		return
	}
	out := tagRows(localRows, contracts.SessionHost{Name: m.opts.SelfName, Self: true, Reachable: true})

	m.mu.Lock()
	var visible []contracts.Peer
	for _, st := range m.peers {
		if !st.peer.Hidden {
			visible = append(visible, st.peer)
		}
	}
	m.mu.Unlock()

	results := make([][]json.RawMessage, len(visible))
	var wg sync.WaitGroup
	for i, p := range visible {
		wg.Add(1)
		go func() {
			defer wg.Done()
			host := contracts.SessionHost{Name: p.Name, PeerID: p.ID, Reachable: true}
			rows, err := m.peerSessions(r.Context(), p, rawQuery)
			if err != nil {
				host.Reachable = false
				rows = m.lastSessions(p.ID)
			}
			results[i] = tagRows(rows, host)
		}()
	}
	wg.Wait()
	for _, rows := range results {
		out = append(out, rows...)
	}
	writeJSON(w, http.StatusOK, out)
}

// tagRows adds "host" to each session row without decoding the rest, so
// fields this build doesn't know about (a newer peer's) pass through.
func tagRows(rows []json.RawMessage, host contracts.SessionHost) []json.RawMessage {
	hostJSON, _ := json.Marshal(host)
	out := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(row, &obj); err != nil {
			continue
		}
		obj["host"] = hostJSON
		b, err := json.Marshal(obj)
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

func copyRecorded(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, vs := range rec.Header() {
		w.Header()[k] = vs
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

// --- Forwarding -----------------------------------------------------------

// forward sends r to peer p's Mesh Service at path, streaming the answer
// over p's pinned TLS connection.
//
// The UI's request is a browser request (Origin, Sec-Fetch-*); the Mesh
// Service refuses those by design, so they're stripped here. That's safe
// because this Router's localguard and front door have already vetted the
// page. No credential is ever sent: the front door has already removed the
// install token, and Authorization is dropped here again so this machine's
// token can never reach a peer whatever the layering. The peer's Mesh
// Service authenticates this device by its certificate, then uses its own
// node's token for its own Router.
func (m *Manager) forward(w http.ResponseWriter, r *http.Request, p contracts.Peer, path string) {
	c, err := m.conn(p)
	if err != nil {
		state, reason := dialFailure(p, err)
		writeError(w, http.StatusBadGateway, "peer_"+string(state), p.Name+": "+reason)
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "https"
			pr.Out.URL.Host = c.host
			pr.Out.URL.Path = path
			pr.Out.URL.RawPath = ""
			pr.Out.Host = c.host
			for name := range pr.Out.Header {
				if strings.HasPrefix(name, "Sec-Fetch-") {
					pr.Out.Header.Del(name)
				}
			}
			pr.Out.Header.Del("Origin")
			pr.Out.Header.Del("Referer")
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Proxy-Authorization")
		},
		Transport:      c.rt,
		ModifyResponse: sanitizePeerResponse(p.Name),
		FlushInterval:  -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			state, reason := dialFailure(p, err)
			writeError(w, http.StatusBadGateway, "peer_"+string(state), p.Name+" isn't answering: "+reason)
		},
	}
	proxy.ServeHTTP(&peerWriter{ResponseWriter: w}, r)
}

// peerWriter is the ResponseWriter a peer's answer is relayed through.
// ReverseProxy passes an informational (1xx) answer straight on from its
// client trace, before ModifyResponse ever sees it, copying the peer's
// headers into the writer's map and then clearing the map: a peer's 103
// Early Hints could carry Link preloads or Set-Cookie to the browser, and
// the clear would erase the headers this node's own layers set
// (localguard's frame protections). So the proxy writes headers into a
// map of its own: a 1xx is dropped with whatever it held, and only the
// final answer's headers (already sanitized) are added to the real ones.
type peerWriter struct {
	http.ResponseWriter
	scratch http.Header
	wrote   bool
}

func (w *peerWriter) Header() http.Header {
	if w.wrote {
		return w.ResponseWriter.Header()
	}
	if w.scratch == nil {
		w.scratch = http.Header{}
	}
	return w.scratch
}

func (w *peerWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	if status < 200 {
		clear(w.scratch) // an informational answer is never relayed
		return
	}
	w.wrote = true
	h := w.ResponseWriter.Header()
	for k, vs := range w.scratch {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *peerWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush and Unwrap keep streaming working (FlushInterval -1 flushes
// through http.ResponseController).
func (w *peerWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *peerWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// relayedHeaders are the only response headers a peer's answer keeps on its
// way to this node's UI: what describes the body, its caching and its
// freshness. The
// rest is a peer speaking for this node's origin — cookies (Set-Cookie),
// navigation (Location, Refresh, Link), CORS, site data, security policy —
// and none of it is the peer's to set.
var relayedHeaders = map[string]bool{
	"Content-Type": true, "Content-Length": true, "Content-Encoding": true,
	"Content-Disposition": true, "Content-Range": true, "Accept-Ranges": true,
	"Cache-Control": true, "Etag": true, "Last-Modified": true, "Expires": true,
	"Date": true, "Vary": true, "Retry-After": true, "X-Accel-Buffering": true,
	// GET /sessions/{id}/repos's freshness (docs/GIT-STATE-DESIGN.md): its
	// body is a bare array, so these say whether it is still computing.
	"X-Mads-Updating": true, "X-Mads-Computed-At": true,
}

// sanitizePeerResponse is ModifyResponse for every answer relayed from
// peer: the implicit /sessions/{id}/... forwarding, /peers/{peer}/..., and
// the streams and downloads among them.
//   - A redirect (or a protocol switch) is never passed on: the browser
//     would follow it on this node's origin with this node's credentials,
//     to any local route. It becomes 502 peer_redirect.
//   - Only relayedHeaders are kept.
//   - The body can't run here (safeserve.Apply): nosniff, a sandboxing
//     CSP, and any type outside safeserve.InlineTypes downloaded as an
//     attachment.
func sanitizePeerResponse(peer string) func(*http.Response) error {
	return func(resp *http.Response) error {
		if (resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified) || resp.StatusCode == http.StatusSwitchingProtocols {
			_ = resp.Body.Close()
			body, _ := json.Marshal(map[string]string{"code": "peer_redirect", "error": peer + " answered with a redirect (" + resp.Status + "), which isn't followed or passed on"})
			resp.StatusCode, resp.Status = http.StatusBadGateway, "502 Bad Gateway"
			resp.Header = http.Header{"Content-Type": {"application/json"}}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			resp.TransferEncoding = nil
		}
		for k := range resp.Header {
			if !relayedHeaders[http.CanonicalHeaderKey(k)] {
				delete(resp.Header, k)
			}
		}
		safeserve.Apply(resp.Header, resp.StatusCode)
		// Trailers are a peer's headers too, set after the body where no
		// filter above sees them: none are relayed.
		resp.Trailer = nil
		return nil
	}
}

// writeDataError answers a /peers route with a Data Service error's own
// code and message (peer_not_found for a missing row). See dataErrorAs.
func writeDataError(w http.ResponseWriter, err error) {
	dataErrorAs(w, err, "peer_not_found")
}

// writeDeviceDataError is writeDataError for the /devices and pairing
// routes, whose missing row is a device: device_not_found.
func writeDeviceDataError(w http.ResponseWriter, err error) {
	dataErrorAs(w, err, "device_not_found")
}

// dataErrorAs answers with a Data Service error's own code and message,
// never the client's request line or the Data Service's address: an answer
// may go to another device. notFound is the code for a missing row.
func dataErrorAs(w http.ResponseWriter, err error, notFound string) {
	var v *contracts.ValidationError
	var f *dataclient.ForbiddenError
	var c *dataclient.ConflictError
	switch {
	case errors.As(err, &v):
		writeJSON(w, http.StatusUnprocessableEntity, v)
	case errors.As(err, &f):
		writeError(w, http.StatusForbidden, firstNonEmpty(f.Code, "forbidden"), f.Msg)
	case errors.As(err, &c):
		writeError(w, http.StatusConflict, firstNonEmpty(c.Code, "conflict"), firstNonEmpty(c.Msg, "it changed meanwhile; reload and retry"))
	case errors.Is(err, dataclient.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "it changed meanwhile; reload and retry")
	case errors.Is(err, dataclient.ErrNotFound):
		writeError(w, http.StatusNotFound, notFound, "not found")
	default:
		log.Printf("mesh: data service: %v", err)
		writeError(w, http.StatusBadGateway, "data_unavailable", "this device's Data Service isn't answering")
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
