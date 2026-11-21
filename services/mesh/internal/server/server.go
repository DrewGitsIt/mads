// Package server is the Mesh Service's HTTP surface: the TLS listener that
// lets a paired device's Router reach this node's Router
// (docs/MESH-MODE-DESIGN.md D2; docs/MESH-AUTH-DESIGN.md D2, D3, D4, D8).
//
// The listener speaks TLS 1.3 only and requires a client certificate
// (devicekey.ServerConfig): the caller proves its device key in the
// handshake. Every request then goes through the same gate, in order:
//
//  1. Refuse anything a browser sent (Origin or any Sec-Fetch-* header).
//     Only Routers talk to the mesh, so localguard's browser threat model
//     is closed by construction rather than re-implemented.
//  2. Identify the device by its certificate key's fingerprint and look it
//     up in this host's paired devices (Options.Devices). A key that isn't
//     paired here is refused (unpaired_device), and logged as
//     device.refused.
//  3. With RequireTailnet (the default; D2's "only accept devices on my
//     tailnet"), also identify the caller with tailscaled WhoIs: a node
//     owned by this node's user, untagged, holding the mesh app
//     capability. Both factors must pass.
//  4. The effective level is the device's grant (D3), capped by the
//     capability's level when the tailnet is required. /mesh/hello needs
//     nothing more; every other route needs its class (authz.Routes) at or
//     below that level, and passes the grant's ceilings (D4).
//  5. Forward to the loopback Router as a plain program would: Host
//     localhost, no Origin, this node's own install token
//     (docs/LOCAL-FRONT-DOOR-DESIGN.md) instead of whatever the caller
//     sent, X-Mads-Mesh-Peer the device's name here (its paired row's
//     Name; the relay mark), X-Mads-Mesh-Level its effective level,
//     X-Mads-Mesh-Device its fingerprint, and X-Mads-Actor the
//     authenticated device (D8), overwriting any caller-supplied value.
//
// A forwarded request stays tied to the grant it was admitted under: when
// the device is unpaired or its grant changes, Revalidate ends it, so a
// long-lived stream (/events, /sessions/{id}/output) never outlives the
// grant that opened it.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/mesh/internal/authz"
	"github.com/drewkidwell/mads/services/mesh/internal/tailnet"
)

// MeshAPI is the mesh wire contract's version, reported by /mesh/hello. A
// viewer greys out a peer whose MeshAPI differs from its own (D3). The
// move to mTLS didn't change it: a viewer that can't speak TLS can't
// reach /mesh/hello at all. 2: the viewer's UI follows a remote session
// live through GET /events/head and since= (the live-only event bus);
// a build without that route can't be followed, so the two don't mix.
// The Router's mesh.MeshAPI must match.
const MeshAPI = 2

// PeerHeader carries the calling device's name here (its paired row's
// Name, not a tailnet node name) on every forwarded request, for the
// Router's logs, and marks it relayed (contracts.MeshRelayHeader: the
// Router never lets such a request write with this host's authority, nor
// fans it out to its own peers). A value sent by the caller is always
// overwritten.
const PeerHeader = contracts.MeshRelayHeader

// ActorHeader is the actor the Router and Data Service record (D8).
const ActorHeader = "X-Mads-Actor"

// maxInspectedBody bounds a request body read for a ceiling check (C3):
// a session create is a few KB; anything past this is refused, not
// streamed unchecked.
const maxInspectedBody = 1 << 20

// Identifier is the tailnet lookup the gate needs; *tailnet.Client in
// production, a fake in tests.
type Identifier interface {
	WhoIs(ctx context.Context, remoteAddr string) (tailnet.Caller, error)
}

// Devices is this host's paired devices by fingerprint; *paired.Directory
// in production.
type Devices interface {
	Lookup(fp string) (contracts.Peer, bool)
	// Loaded reports whether the list has been read at all. Until it has,
	// every device is refused: nobody can be told apart.
	Loaded() bool
}

// Appender writes an event to the Data Service (*dataclient.Client).
type Appender interface {
	Append(ctx context.Context, env *contracts.Envelope) error
}

// Options configures a Server.
type Options struct {
	Self tailnet.Self
	// Tailnet identifies callers by WhoIs; used only with RequireTailnet.
	Tailnet Identifier
	// RequireTailnet also requires tailnet identity as a second factor
	// (D2): same user, untagged, holding tailnet.MeshCap, whose level then
	// caps the grant's. The value to start with: SetRequireTailnet changes
	// it live (this host's "Only accept devices on my tailnet" setting).
	RequireTailnet bool
	// Devices is who may connect, and at what grant.
	Devices Devices
	// Audit receives device.refused (D8); nil only logs.
	Audit     Appender
	RouterURL *url.URL // the local Router, e.g. http://localhost:17373
	// Token is this node's install token, sent as the bearer on every
	// forwarded request: the Router's front door refuses anything else.
	// localauth.FileSource in production, so a rotation is followed
	// without a restart. Nil sends none (the Router then answers 401).
	Token func() string
	// Transport is the forwarder's base transport (nil: the default).
	Transport http.RoundTripper
	Commit    string // this build's VCS revision, for /mesh/hello
	FS        *FS    // /mesh/fs/list; nil disables it
	// Device is this node's device fingerprint (pkg/devicekey), for
	// /mesh/hello. The same key the TLS handshake proved.
	Device devicekey.Fingerprint
	// Platform is what /mesh/hello reports as this host's platform
	// (runtime.GOOS, or an override such as a phone's "ios").
	Platform string
	// HostsSessions is whether this host has mads sessions for other
	// devices to view; /mesh/hello reports it.
	HostsSessions bool
	// Pairing is where pairing codes are redeemed and requests read (the
	// Data Service, *dataclient.Client); nil turns /mesh/pair off.
	Pairing Pairing
	// Name is this host's mads name (the Router's -mesh-name): how pairing
	// answers name it. Empty: the tailnet host name.
	Name string
	// WhoIsTTL caches a caller's identity per IP. Short, so a policy change
	// in the admin console takes effect within seconds.
	WhoIsTTL time.Duration
	Now      func() time.Time
}

// Server is the gate plus the forwarder.
type Server struct {
	opts       Options
	classifier *authz.Classifier
	proxy      *httputil.ReverseProxy
	// router reads from the local Router for a ceiling check (a session's
	// trust, the host's default trust), with this node's token.
	router  *http.Client
	refused *refusals
	// pairLimits rate-limit /mesh/pair: per address and overall.
	pairLimits *pairLimits

	mu    sync.Mutex
	cache map[string]cachedCaller
	// tailnetLogged debounces tailnet refusal log lines, by address and
	// code.
	tailnetLogged map[string]time.Time
	// active is every forwarded request in flight, with the grant it was
	// admitted under (Revalidate).
	active map[*activeReq]struct{}
	// requireTailnet is Options.RequireTailnet as it stands now
	// (SetRequireTailnet).
	requireTailnet atomic.Bool
}

type cachedCaller struct {
	caller  tailnet.Caller
	expires time.Time
}

// New builds a Server.
func New(opts Options) *Server {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	transport := &localauth.Transport{Base: opts.Transport, Source: opts.Token}
	s := &Server{
		opts: opts, classifier: authz.NewClassifier(), cache: map[string]cachedCaller{}, tailnetLogged: map[string]time.Time{},
		active:     map[*activeReq]struct{}{},
		router:     &http.Client{Transport: transport, Timeout: 10 * time.Second},
		refused:    newRefusals(opts.Audit, opts.Now),
		pairLimits: newPairLimits(opts.Now),
	}
	s.requireTailnet.Store(opts.RequireTailnet)
	target := opts.RouterURL
	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// SetURL keeps the inbound Host; the Router's localguard wants
			// its own loopback name.
			pr.Out.Host = target.Host
			// What a browser would send, and what would make localguard
			// treat this as a page. Step 1 already refused these; strip
			// anyway so the forwarder never depends on the gate's order.
			for name := range pr.Out.Header {
				if strings.HasPrefix(http.CanonicalHeaderKey(name), "Sec-Fetch-") {
					pr.Out.Header.Del(name)
				}
			}
			pr.Out.Header.Del("Origin")
			pr.Out.Header.Del("Referer")
			pr.Out.Header.Del("Cookie")
			// A caller's credential is never passed through: the bearer
			// the Router sees is this node's own, added by the transport.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Proxy-Authorization")
			stamp(pr.Out.Header, callerFromContext(pr.In.Context()))
		},
		Transport: transport,
		// SSE (/events, /sessions/{id}/output) must reach the peer per event.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return // the peer went away mid-stream, or Revalidate ended it
			}
			log.Printf("mesh: forward %s %s for %s: %v", r.Method, r.URL.Path, callerFromContext(r.Context()).label(), err)
			writeError(w, http.StatusBadGateway, "router_unreachable", "this node's Router isn't answering")
		},
	}
	return s
}

// caller is an admitted device.
type caller struct {
	peer  contracts.Peer
	fp    devicekey.Fingerprint
	level contracts.GrantLevel // effective: the grant, capped by the tailnet
	node  string               // tailnet node name, with RequireTailnet
	// tailnet: the tailnet factor was checked at admission (it was
	// required then).
	tailnet bool
}

// actor is the authenticated identity the Router records (D8): the
// device's name here and its fingerprint's short form, which survives a
// rename.
func (c caller) actor() string { return "device:" + c.peer.Name + "/" + c.fp.Short() }

func (c caller) label() string {
	if c.peer.Name != "" {
		return c.peer.Name + " (" + c.fp.Short() + ")"
	}
	return c.fp.Short()
}

// stamp sets the headers that carry the authenticated device to the
// Router, overwriting anything the caller sent under those names.
func stamp(h http.Header, c caller) {
	h[PeerHeader] = []string{c.peer.Name}
	h.Set(ActorHeader, c.actor())
	h.Set(contracts.MeshLevelHeader, string(c.level))
	h.Set(contracts.MeshDeviceHeader, string(c.fp))
}

// ServeHTTP is the gate.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if reason := browserReason(r); reason != "" {
		s.refuse(w, r, "", http.StatusForbidden, "browser_request", reason)
		return
	}
	if reason := uncleanPath(r); reason != "" {
		// The route table matches the cleaned path, the Router serves the
		// path as sent: refuse anything where the two could differ.
		s.refuse(w, r, "", http.StatusBadRequest, "bad_path", reason)
		return
	}
	if r.TLS == nil {
		s.refuse(w, r, "", http.StatusForbidden, "no_client_certificate", "the mesh listener takes TLS with a device certificate only")
		return
	}
	fp, err := devicekey.PeerFingerprint(*r.TLS)
	if err != nil {
		s.refuse(w, r, "", http.StatusForbidden, "no_client_certificate", err.Error())
		return
	}
	// The tailnet factor first: a caller that isn't one of this user's
	// tailnet devices is turned away before its key is looked at, so it
	// can't spend the audit log's budget for unpaired keys.
	var tc tailnet.Caller
	requireTailnet := s.requireTailnet.Load()
	if requireTailnet {
		var ok bool
		if tc, ok = s.tailnetGate(w, r, fp.Short()); !ok {
			return
		}
	}
	if !s.opts.Devices.Loaded() {
		s.refuse(w, r, fp.Short(), http.StatusServiceUnavailable, "devices_unavailable",
			"this host hasn't read its paired devices yet (is its Data Service up?)")
		return
	}
	if r.URL.Path == "/mesh/pair" || strings.HasPrefix(r.URL.Path, "/mesh/pair/") {
		// The one surface an unpaired key may reach: presenting a pairing
		// code, and asking how its request went (D6). Still behind the
		// tailnet factor above, when it's required.
		s.handlePair(w, r, fp, tc)
		return
	}
	dev, ok := s.opts.Devices.Lookup(string(fp))
	if !ok {
		// Nothing more for this connection: an unpaired key gets one answer
		// per handshake, not a kept-alive channel to retry on.
		w.Header().Set("Connection", "close")
		s.refuseDevice(w, r, caller{fp: fp}, "", http.StatusForbidden, "unpaired_device",
			"this host hasn't paired device "+fp.Short()+"; to let it in, run on this host: mads pair --accept "+fp.Grouped())
		return
	}
	c := caller{peer: dev, fp: fp, level: dev.Level()}
	if requireTailnet {
		c.node, c.tailnet = tc.NodeName, true
		// The capability is only the second factor's gate; the grant
		// decides the level. Should the tailnet policy name a lower level
		// than the grant, the lower one holds: neither side can raise what
		// the other allows.
		c.level = authz.Min(c.level, authz.CapabilityLevel(tc.Access))
	}
	// A paired device through both factors, granted something here: its
	// connection stays open. One granted nothing gets its answer (hello
	// says so) and the connection's deadline still runs.
	if c.level != contracts.GrantNone {
		admitConn(r.Context())
	}
	r = r.WithContext(withCaller(r.Context(), c))

	switch {
	case r.URL.Path == "/mesh/hello":
		s.handleHello(w, r, c)
		return
	case r.URL.Path == "/mesh/fs/list":
		if !authz.Allows(c.level, authz.Control) {
			s.refuseDevice(w, r, c, "/mesh/fs/list", http.StatusForbidden, "no_access", `browsing folders needs "control" here`)
			return
		}
		s.handleFSList(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/mesh/"):
		writeError(w, http.StatusNotFound, "not_found", "no such mesh route")
		return
	}

	pattern, class := s.classifier.Classify(r)
	if class == authz.Deny {
		// The same for every device, whatever its grant: not an attempt
		// above its level, so not audited.
		s.refuse(w, r, c.label(), http.StatusForbidden, "not_exposed", "this route isn't available over the mesh")
		return
	}
	if c.level == contracts.GrantNone {
		s.refuseDevice(w, r, c, pattern, http.StatusForbidden, "no_access", "this host grants device "+c.label()+" nothing")
		return
	}
	if !authz.Allows(c.level, class) {
		s.refuseDevice(w, r, c, pattern, http.StatusForbidden, "no_access",
			pattern+` needs "`+string(class)+`" here; this device has "`+string(c.level)+`"`)
		return
	}
	if !s.ceilings(w, r, c, pattern) {
		return
	}
	s.forward(w, r, c)
}

// tailnetGate is the second factor (D2): WhoIs, same user, untagged, and
// the mesh capability.
func (s *Server) tailnetGate(w http.ResponseWriter, r *http.Request, label string) (tailnet.Caller, bool) {
	tc, err := s.whois(r.Context(), r.RemoteAddr)
	code, msg, definite := s.tailnetVerdict(tc, err)
	switch {
	case code == "":
		return tc, true
	case !definite:
		s.logTailnetRefusal(r.RemoteAddr, code, "whois %s: %v", r.RemoteAddr, err)
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusServiceUnavailable, code, msg)
	default:
		// Debounced per address: a node retrying in a loop writes a line a
		// minute, not one per request.
		s.logTailnetRefusal(r.RemoteAddr, code, "refused %s %s from %s: %s", clip(r.Method), clip(r.URL.Path), label, msg)
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusForbidden, code, msg)
	}
	return tc, false
}

// closeIfUnadmitted ends a refused request's connection unless it already
// carries an admitted device: a caller that isn't let in gets one answer
// per connection, not a kept-alive channel to retry on (HTTP/2: GOAWAY).
func closeIfUnadmitted(w http.ResponseWriter, r *http.Request) {
	if !connAdmitted(r.Context()) {
		w.Header().Set("Connection", "close")
	}
}

// --- Ceilings (D4) -----------------------------------------------------------

// ceilings applies the device's grant ceilings to the route, reading the
// request body where the decision is in it (C3), and reports whether the
// request may go on. A body it read is put back for the forwarder.
func (s *Server) ceilings(w http.ResponseWriter, r *http.Request, c caller, pattern string) bool {
	var ceil contracts.PeerCeilings
	if c.peer.Grant != nil {
		ceil = c.peer.Grant.Ceilings
	}
	// C1: a chat device's free text to a full-trust session.
	if authz.FullTrustChatRoutes[pattern] && c.level == contracts.GrantChat {
		switch ceil.FullTrustChat {
		case contracts.FullTrustChatRefused, contracts.FullTrustChatHeld:
			if pattern == "POST /sessions/{id}/compact" {
				// Compact carries free text only in its optional focus
				// ("instructions"); a plain compact is no message.
				raw, ok := readInspected(w, r)
				if !ok {
					return false
				}
				var body struct {
					Instructions string `json:"instructions"`
				}
				// Decoded as the session child decodes it: the first JSON
				// value, keys in any case. A body it can't decode runs
				// nothing there.
				_ = json.NewDecoder(bytes.NewReader(raw)).Decode(&body)
				if body.Instructions == "" {
					break
				}
			}
			trust, err := s.sessionTrust(r.Context(), sessionIDOf(r.URL), c)
			if err != nil {
				log.Printf("mesh: C1 check for %s: %v", c.label(), err)
				writeError(w, http.StatusServiceUnavailable, "ceiling_check_failed", "couldn't read the session's trust to apply this device's ceiling")
				return false
			}
			if trust != contracts.TrustFull {
				break
			}
			if ceil.FullTrustChat == contracts.FullTrustChatRefused {
				s.refuseDevice(w, r, c, pattern, http.StatusForbidden, "full_trust_chat_refused",
					"this device may not message full-trust sessions here")
				return false
			}
			// "held" (wait until a control device approves) is deferred
			// (docs/MESH-AUTH-DESIGN.md D4): there is no approval flow, so
			// a held message is refused, and says why.
			s.refuseDevice(w, r, c, pattern, http.StatusForbidden, "held_unsupported",
				"messages from this device to full-trust sessions are held for approval, which this host can't do yet; refused")
			return false
		}
	}
	// C2: no shell for this device. A trust ceiling (C3) below full
	// implies it: keystrokes into the agent's TUI can run commands or
	// change its permission mode whatever trust the session was started at.
	if authz.ShellRoutes[pattern] {
		switch {
		case ceil.NoShell:
			s.refuseDevice(w, r, c, pattern, http.StatusForbidden, "no_shell", "this device may not use the agent's terminal here")
			return false
		case ceil.MaxTrust != "" && ceil.MaxTrust != contracts.TrustFull:
			s.refuseDevice(w, r, c, pattern, http.StatusForbidden, "no_shell",
				"this device may not use the agent's terminal here: its trust ceiling ("+string(ceil.MaxTrust)+") would mean nothing there")
			return false
		}
	}
	// C3: no session started or moved above the trust ceiling.
	if setting, ok := authz.TrustRoutes[pattern]; ok && ceil.MaxTrust != "" {
		return s.trustCeiling(w, r, c, pattern, setting, ceil.MaxTrust)
	}
	return true
}

// trustCeiling is C3 for a route whose body sets trust. The body is
// decoded the way the Router decodes it (encoding/json, first value, case-
// insensitive keys), so the trust checked here is the trust the Router
// will apply. A create naming no trust gets the host's default for it —
// the same resolution the Router would make — written into the body, so
// what's checked is what's used.
// readInspected reads r's body for a ceiling check (at most
// maxInspectedBody) and puts it back for the forwarder.
func readInspected(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxInspectedBody+1))
	_ = r.Body.Close()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "couldn't read the request body")
		return nil, false
	}
	if len(raw) > maxInspectedBody {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
		return nil, false
	}
	setBody(r, raw)
	return raw, true
}

func setBody(r *http.Request, raw []byte) {
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	r.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	r.TransferEncoding = nil
}

func (s *Server) trustCeiling(w http.ResponseWriter, r *http.Request, c caller, pattern string, setting authz.TrustSetting, ceiling contracts.SessionTrust) bool {
	raw, ok := readInspected(w, r)
	if !ok {
		return false
	}
	var err error
	var body struct {
		Trust     contracts.SessionTrust `json:"trust"`
		Provider  string                 `json:"provider"`
		Workspace string                 `json:"workspace"`
		Dir       string                 `json:"dir"`
	}
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return false
	}
	trust := body.Trust
	if trust == "" && setting == authz.TrustOnCreate {
		if trust, err = s.defaultTrust(r.Context(), body.Provider, body.Workspace, body.Dir, c); err != nil {
			log.Printf("mesh: C3 default trust for %s: %v", c.label(), err)
			writeError(w, http.StatusServiceUnavailable, "ceiling_check_failed", "couldn't read this host's default trust to apply this device's ceiling")
			return false
		}
		if raw, err = withTrust(raw, trust); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
			return false
		}
	}
	if authz.AboveCeiling(trust, ceiling) {
		what := "raise a session"
		if setting == authz.TrustOnCreate {
			what = "start a session"
		}
		s.refuseDevice(w, r, c, pattern, http.StatusForbidden, "trust_ceiling",
			"this device may not "+what+" above "+string(ceiling)+" trust here (asked for "+strconv.Quote(string(trust))+")")
		return false
	}
	setBody(r, raw)
	return true
}

// withTrust is raw (a JSON object) with its trust set to t: every key the
// Router would read as "trust" (any case) is replaced by one.
func withTrust(raw []byte, t contracts.SessionTrust) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&obj); err != nil {
		return nil, err
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	for k := range obj {
		if strings.EqualFold(k, "trust") {
			delete(obj, k)
		}
	}
	v, _ := json.Marshal(t)
	obj["trust"] = v
	return json.Marshal(obj)
}

// sessionIDOf is the {id} of /sessions/{id}/..., read from the escaped
// path and unescaped, as the Router's ServeMux reads a path value.
func sessionIDOf(u *url.URL) string {
	rest, _ := strings.CutPrefix(u.EscapedPath(), "/sessions/")
	seg, _, _ := strings.Cut(rest, "/")
	id, err := url.PathUnescape(seg)
	if err != nil {
		return ""
	}
	return id
}

// uncleanPath says why r's path isn't one the route table and the Router
// would read alike, or "" when it is: it must be in clean form (no "."
// or ".." segments, no doubled slashes; a trailing slash is kept as sent)
// and carry no encoded slash, which ServeMux and a path-value reader can
// split differently.
func uncleanPath(r *http.Request) string {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/") {
		return "the path must be absolute"
	}
	clean := path.Clean(p)
	if strings.HasSuffix(p, "/") && clean != "/" {
		clean += "/"
	}
	if clean != p {
		return "the path must be in clean form"
	}
	if strings.Contains(strings.ToLower(r.URL.RawPath), "%2f") {
		return "the path may not contain an encoded slash"
	}
	return ""
}

// sessionTrust is a session's trust as recorded (unset reads as limited,
// the Router's builtin default).
func (s *Server) sessionTrust(ctx context.Context, id string, c caller) (contracts.SessionTrust, error) {
	var sess struct {
		Trust contracts.SessionTrust `json:"trust"`
	}
	if err := s.routerGet(ctx, "/sessions/"+url.PathEscape(id), c, &sess); err != nil {
		return "", err
	}
	if sess.Trust == "" {
		return contracts.TrustLimited, nil
	}
	return sess.Trust, nil
}

// defaultTrust is the trust the Router would give a new session with these
// fields and none of its own (GET /sessions/defaults: the same resolution
// POST /sessions makes).
func (s *Server) defaultTrust(ctx context.Context, provider, workspace, dir string, c caller) (contracts.SessionTrust, error) {
	q := url.Values{}
	for k, v := range map[string]string{"provider": provider, "workspace": workspace, "dir": dir} {
		if v != "" {
			q.Set(k, v)
		}
	}
	var d contracts.SessionDefaults
	if err := s.routerGet(ctx, "/sessions/defaults?"+q.Encode(), c, &d); err != nil {
		return "", err
	}
	if !contracts.SessionTrust(d.Trust.Value).Valid() {
		return "", errors.New("the Router resolved no trust")
	}
	return contracts.SessionTrust(d.Trust.Value), nil
}

// routerGet reads pathAndQuery from the local Router for a ceiling check.
// It carries the relay mark, as the forwarded request will, so the Router
// answers from this host only.
func (s *Server) routerGet(ctx context.Context, pathAndQuery string, c caller, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(s.opts.RouterURL.String(), "/")+pathAndQuery, nil)
	if err != nil {
		return err
	}
	req.Host = s.opts.RouterURL.Host
	stamp(req.Header, c)
	resp, err := s.router.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return errors.New("GET " + strings.SplitN(pathAndQuery, "?", 2)[0] + ": " + resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// --- Forwarding and revocation -------------------------------------------------

// activeReq is one forwarded request in flight.
type activeReq struct {
	fp     string
	remote string // for the tailnet factor's re-check
	grant  grantKey
	level  contracts.GrantLevel // effective, as admitted
	// tailnet: admitted with the tailnet factor checked.
	tailnet bool
	label   string
	what    string
	cancel  context.CancelFunc
}

// grantKey is what a request was admitted under: the device's stored level
// and ceilings.
type grantKey struct {
	level    contracts.GrantLevel
	ceilings contracts.PeerCeilings
}

func grantOf(p contracts.Peer) grantKey {
	if p.Grant == nil {
		return grantKey{}
	}
	return grantKey{p.Grant.Level, p.Grant.Ceilings}
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, c caller) {
	ctx, cancel := context.WithCancel(r.Context())
	a := &activeReq{fp: string(c.fp), remote: r.RemoteAddr, grant: grantOf(c.peer), level: c.level, tailnet: c.tailnet, label: c.label(), what: r.Method + " " + clip(r.URL.Path), cancel: cancel}
	s.mu.Lock()
	s.active[a] = struct{}{}
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.active, a)
		s.mu.Unlock()
	}()
	// A change landing between admission and registration above is caught
	// here rather than missed.
	if !s.stillGranted(a) {
		writeError(w, http.StatusForbidden, "grant_changed", "this device's grant changed; retry")
		return
	}
	// The same for "Only accept devices on my tailnet" turned on meanwhile:
	// SetRequireTailnet's re-check may have run before this request was in
	// s.active, so one admitted without the tailnet factor gets it now.
	if !a.tailnet && s.requireTailnet.Load() {
		if why := s.stillOnTailnet(a); why != "" {
			log.Printf("mesh: refusing %s for %s: the tailnet is required now, and %s", a.what, a.label, why)
			writeError(w, http.StatusForbidden, "grant_changed", "this host now only accepts devices on its tailnet; retry")
			return
		}
	}
	s.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// stillGranted reports whether a's device still holds the pairing and
// grant a was admitted under, by the paired directory alone: no network,
// so a revocation never waits on tailscaled.
func (s *Server) stillGranted(a *activeReq) bool {
	p, ok := s.opts.Devices.Lookup(a.fp)
	return ok && grantOf(p) == a.grant
}

// stillOnTailnet re-checks a's tailnet factor: "" when it still passes at
// the effective level a was admitted at, else why not. A transient WhoIs
// failure is no answer: whois keeps the last good one for a grace period,
// and only past it does the failure end the request.
func (s *Server) stillOnTailnet(a *activeReq) string {
	p, ok := s.opts.Devices.Lookup(a.fp)
	if !ok {
		return "" // the directory check's business
	}
	tc, err := s.whois(context.Background(), a.remote)
	if code, _, _ := s.tailnetVerdict(tc, err); code != "" {
		return "its tailnet identity check failed (" + code + ")"
	}
	if authz.Min(p.Level(), authz.CapabilityLevel(tc.Access)) != a.level {
		return "its tailnet capability changed"
	}
	return ""
}

func (s *Server) activeReqs() []*activeReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := make([]*activeReq, 0, len(s.active))
	for a := range s.active {
		active = append(active, a)
	}
	return active
}

// Revalidate ends every forwarded request whose device has since been
// unpaired, or whose grant (level or ceilings) has changed: the device
// reconnects and is admitted afresh under what it holds now. The paired
// directory calls it after every reload; it reads only the directory, so
// it is immediate and never stalls the directory's loop.
func (s *Server) Revalidate() {
	for _, a := range s.activeReqs() {
		if !s.stillGranted(a) {
			log.Printf("mesh: ending %s for %s: its pairing or grant changed", a.what, a.label)
			a.cancel()
		}
	}
}

// RevalidateTailnet ends every forwarded request whose tailnet factor no
// longer passes at the level it was admitted at: a capability lowered or
// removed in the tailnet policy, a node gone from the tailnet. The tailnet
// sends no events, so RevalidateEvery runs this on a clock.
func (s *Server) RevalidateTailnet() {
	if !s.requireTailnet.Load() {
		return
	}
	for _, a := range s.activeReqs() {
		if why := s.stillOnTailnet(a); why != "" {
			log.Printf("mesh: ending %s for %s: %s", a.what, a.label, why)
			a.cancel()
		}
	}
}

// RequireTailnet reports whether the tailnet factor is required now.
func (s *Server) RequireTailnet() bool { return s.requireTailnet.Load() }

// SetRequireTailnet turns the tailnet factor on or off from the next
// connection and request on (this host's "Only accept devices on my
// tailnet", followed live from the Data Service). Turning it on also ends
// every forwarded request in flight that wouldn't pass it now, as a
// lowered tailnet capability does; turning it off ends nothing (what's
// open was admitted under the stricter rule).
func (s *Server) SetRequireTailnet(on bool) {
	if s.requireTailnet.Swap(on) == on {
		return
	}
	if on {
		log.Printf("mesh: only accepting devices on this tailnet (tailnet identity required, besides a paired device key)")
		s.RevalidateTailnet()
	} else {
		log.Printf("mesh: accepting paired devices from any network (tailnet identity no longer required)")
	}
}

// RevalidateEvery runs RevalidateTailnet every d until ctx ends.
func (s *Server) RevalidateEvery(ctx context.Context, d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RevalidateTailnet()
		}
	}
}

// --- Hello ---------------------------------------------------------------------

func (s *Server) handleHello(w http.ResponseWriter, _ *http.Request, c caller) {
	writeJSON(w, http.StatusOK, Hello{
		Node:          s.opts.Self.HostName,
		DNSName:       s.opts.Self.DNSName,
		MadsCommit:    s.opts.Commit,
		MeshAPI:       MeshAPI,
		Access:        authz.Levels(c.level),
		Device:        string(s.opts.Device),
		Platform:      s.opts.Platform,
		HostsSessions: s.opts.HostsSessions,
		Grant:         string(c.peer.Level()),
		TailnetCapped: authz.Min(c.level, c.peer.Level()) != c.peer.Level(),
	})
}

// Hello is GET /mesh/hello: who this node is, and what the caller may do
// here. It is served only to paired devices, over the verified TLS
// connection.
type Hello struct {
	Node       string `json:"node"`
	DNSName    string `json:"dns_name"`
	MadsCommit string `json:"mads_commit"`
	MeshAPI    int    `json:"mesh_api"`
	// Access is the caller's effective level and every level below it
	// (view, chat, control, admin), so ["view"] for a view-only device and
	// ["view","chat","control"] for control. Empty: paired, granted nothing.
	Access []string `json:"access"`
	// Device is this node's device fingerprint (docs/MESH-AUTH-DESIGN.md
	// D1): the key its TLS certificate carries.
	Device string `json:"device,omitempty"`
	// Platform is this host's platform (runtime.GOOS, or an override for a
	// phone): a viewer derives the device's kind from it.
	Platform string `json:"platform,omitempty"`
	// HostsSessions is whether this host has mads sessions to view.
	HostsSessions bool `json:"hosts_sessions"`
	// Grant is the caller's grant here before the tailnet capability caps
	// it ("" none); TailnetCapped says the capability (the tailnet
	// policy's access list) holds it below that, e.g. an admin grant under
	// a policy of ["control"] acts as control.
	Grant         string `json:"grant,omitempty"`
	TailnetCapped bool   `json:"tailnet_capped,omitempty"`
}

// whoisTimeout bounds one LocalAPI call: a hung tailscaled can't hold a
// request, a connection's first read, or a re-check for long.
var whoisTimeout = 2 * time.Second

// whois is the tailnet identity behind remoteAddr, cached per IP for
// WhoIsTTL. ErrNotFound is a definite answer (not a tailnet node). Any
// other failure is transient: the last good answer is used for up to
// three TTLs past its expiry (a tailscaled restart, a slow LocalAPI), and
// only then does the failure stand.
func (s *Server) whois(ctx context.Context, remoteAddr string) (tailnet.Caller, error) {
	ip := hostOf(remoteAddr)
	now := s.opts.Now()
	s.mu.Lock()
	c, cached := s.cache[ip]
	s.mu.Unlock()
	if cached && now.Before(c.expires) {
		return c.caller, nil
	}

	ctx, cancel := context.WithTimeout(ctx, whoisTimeout)
	defer cancel()
	caller, err := s.opts.Tailnet.WhoIs(ctx, remoteAddr)
	switch {
	case err == nil:
		if s.opts.WhoIsTTL > 0 {
			s.mu.Lock()
			s.cache[ip] = cachedCaller{caller: caller, expires: now.Add(s.opts.WhoIsTTL)}
			s.mu.Unlock()
		}
		return caller, nil
	case errors.Is(err, tailnet.ErrNotFound):
		s.mu.Lock()
		delete(s.cache, ip)
		s.mu.Unlock()
		return tailnet.Caller{}, err
	case cached && now.Before(c.expires.Add(3*s.opts.WhoIsTTL)):
		return c.caller, nil
	}
	return tailnet.Caller{}, err
}

// tailnetVerdict is the second factor's answer on a WhoIs result: "" when
// the caller passes, else a refusal code and message. definite is false
// only for a transient failure (no verdict yet).
func (s *Server) tailnetVerdict(tc tailnet.Caller, err error) (code, msg string, definite bool) {
	switch {
	case errors.Is(err, tailnet.ErrNotFound):
		return "not_tailnet", "caller is not a tailnet peer", true
	case err != nil:
		return "whois_failed", "couldn't identify the caller with tailscaled", false
	case tc.UserID != s.opts.Self.UserID:
		return "other_user", "mesh peers must belong to this node's owner", true
	case len(tc.Tags) > 0:
		return "tagged_node", "tagged nodes can't join the mesh (v1)", true
	case !tc.HasCap:
		return "no_capability", "the tailnet policy grants this device no " + tailnet.MeshCap, true
	}
	return "", "", true
}

// admitAddr is the tailnet gate at a connection's first read, before the
// TLS handshake: false closes the connection. Only a definite refusal
// closes it; on a transient failure the connection goes on, and its
// requests get the gate's own answer.
func (s *Server) admitAddr(remoteAddr string) bool {
	tc, err := s.whois(context.Background(), remoteAddr)
	code, msg, definite := s.tailnetVerdict(tc, err)
	if code == "" || !definite {
		return true
	}
	s.logTailnetRefusal(remoteAddr, code, "connection from %s closed before its handshake: %s", remoteAddr, msg)
	return false
}

// tailnetLogEvery is how often a tailnet refusal is logged per address
// and code: a node retrying in a loop writes one line a minute.
const tailnetLogEvery = time.Minute

func (s *Server) logTailnetRefusal(remoteAddr, code, format string, args ...any) {
	key := hostOf(remoteAddr) + " " + code
	now := s.opts.Now()
	s.mu.Lock()
	last, seen := s.tailnetLogged[key]
	if seen && now.Sub(last) < tailnetLogEvery {
		s.mu.Unlock()
		return
	}
	if len(s.tailnetLogged) >= refusalMemory {
		s.tailnetLogged = map[string]time.Time{}
	}
	s.tailnetLogged[key] = now
	s.mu.Unlock()
	log.Printf("mesh: "+format, args...)
}

func (s *Server) refuse(w http.ResponseWriter, r *http.Request, peer string, status int, code, msg string) {
	if peer == "" {
		peer = r.RemoteAddr
	}
	log.Printf("mesh: refused %s %s from %s: %s", clip(r.Method), clip(r.URL.Path), peer, msg)
	closeIfUnadmitted(w, r)
	writeError(w, status, code, msg)
}

// maxLogged bounds a caller-chosen string (a path, a method) in a log line
// or an event: a request can't make either as large as itself.
const maxLogged = 256

func clip(s string) string {
	if len(s) <= maxLogged {
		return s
	}
	return s[:maxLogged] + "…"
}

// refuseDevice is refuse for a device turned away by its key, level or
// ceilings: also recorded as device.refused (debounced, rate-capped).
func (s *Server) refuseDevice(w http.ResponseWriter, r *http.Request, c caller, route string, status int, code, msg string) {
	if route == "" {
		route = r.URL.Path
	}
	ref := contracts.DeviceRefusal{
		Fingerprint: string(c.fp), PeerID: c.peer.ID, Name: c.peer.Name, Level: c.level,
		Code: code, Method: clip(r.Method), Route: clip(route), Remote: r.RemoteAddr,
	}
	if s.refused.record(ref) {
		log.Printf("mesh: refused %s %s from %s: %s", clip(r.Method), clip(r.URL.Path), c.label(), msg)
	}
	closeIfUnadmitted(w, r)
	writeError(w, status, code, msg)
}

func browserReason(r *http.Request) string {
	if _, ok := r.Header["Origin"]; ok {
		return "browser requests aren't accepted on the mesh listener (Origin present)"
	}
	for name := range r.Header {
		if strings.HasPrefix(name, "Sec-Fetch-") {
			return "browser requests aren't accepted on the mesh listener (" + name + " present)"
		}
	}
	return ""
}

type callerKey struct{}

func withCaller(ctx context.Context, c caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

func callerFromContext(ctx context.Context) caller {
	c, _ := ctx.Value(callerKey{}).(caller)
	return c
}

// apiError matches the Router's error shape closely enough for a viewer to
// show it: a stable code plus a sentence.
type apiError struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Code: code, Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
