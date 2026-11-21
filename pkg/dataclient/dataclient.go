// Package dataclient is the HTTP client for the Data Service — the one way
// any other service touches persistent state (spec §15). It wraps the REST
// contract in pkg/contracts types, stamps every mutation with the caller's
// X-Mads-Actor identity, and maps the API's error shapes onto typed errors
// (ErrNotFound, ErrConflict, *contracts.ValidationError).
package dataclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// ErrNotFound maps 404s.
var ErrNotFound = errors.New("not found")

// ErrConflict maps 409s — for tasks, that's "live-leased by someone else".
var ErrConflict = errors.New("conflict")

// ConflictError is a 409 (errors.Is ErrConflict): Code and Msg are the
// answer's "code" and "error", for a caller to pass on without this
// client's request line or the Data Service's address.
type ConflictError struct {
	Method, Path string
	Code, Msg    string
	raw          string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s %s: %v: %s", e.Method, e.Path, ErrConflict, e.raw)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

// ForbiddenError is a 403: the Data Service refused the write for who made
// it (or, for a pairing code, refused the code). Code is the answer's
// "code", when it has one.
type ForbiddenError struct {
	Method, Path string
	Code, Msg    string
}

func (e *ForbiddenError) Error() string {
	return e.Method + " " + e.Path + ": status 403: " + e.Msg
}

// errorField reads one string field of a JSON error body.
func errorField(raw []byte, field string) string {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	s, _ := body[field].(string)
	return s
}

// ErrLeaseLost maps a 409 with code "lease_lost" (Renew): the lease is
// gone, expired or held under another token, so this client no longer
// holds the task and must stop writing to it. Errors carrying it also
// match ErrConflict.
var ErrLeaseLost = errors.New("lease lost")

// leaseHeader carries a task's lease token (docs/PIPELINE-LEASES-DESIGN.md
// A2) on every request about that task.
const leaseHeader = "X-Mads-Lease"

type Client struct {
	base  string
	actor string
	http  *http.Client
	// token is the install token's source (docs/LOCAL-FRONT-DOOR-DESIGN.md
	// D1/D5), sent as a bearer on every request; nil sends none.
	token func() string
	// reach logs the Data Service going unreachable and back
	// (WithUnreachableLog); nil logs nothing.
	reach *reachability

	// mu guards held: the leases this client claimed, by task id. The
	// tokens live only here, in memory: never logged, never in an error.
	mu   sync.Mutex
	held map[string]heldLease
}

type heldLease struct {
	token  string
	holder string
}

// Option configures a Client.
type Option func(*Client)

// WithToken sends tok, the install token, as a bearer on every request —
// the Data Service refuses everything but GET /health without it.
func WithToken(tok string) Option {
	return func(c *Client) {
		if tok == "" {
			c.token = nil
			return
		}
		c.token = func() string { return tok }
	}
}

// WithTokenSource is WithToken for a token that can change under a running
// process (a rotation): src is asked on every request. A *localauth.Store's
// Token method is the usual source.
func WithTokenSource(src func() string) Option {
	return func(c *Client) { c.token = src }
}

// New builds a client. actor names the mutation identity this client rides
// under (attribution, not authentication — spec §15); empty means the
// server-side default, "user". Authentication is the install token
// (WithToken / WithTokenSource).
func New(baseURL, actor string, opts ...Option) *Client {
	c := &Client{
		base:  strings.TrimSuffix(baseURL, "/"),
		actor: actor,
		http:  &http.Client{Timeout: 30 * time.Second},
		held:  map[string]heldLease{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// authorize stamps the install token on req.
func (c *Client) authorize(req *http.Request) {
	if c.token == nil {
		return
	}
	if tok := c.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// TokenSource is this client's token source (nil when it sends none), for
// a caller building a sibling client to the same Data Service.
func (c *Client) TokenSource() func() string { return c.token }

func (c *Client) leaseToken(taskID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held[taskID].token
}

func (c *Client) forgetLease(taskID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.held, taskID)
}

// pathTaskID is the task a request path is about: the id in "/tasks/{id}"
// and its sub-paths, or "" for any other path.
func pathTaskID(path string) string {
	rest, ok := strings.CutPrefix(path, "/tasks/")
	if !ok {
		return ""
	}
	seg, _, _ := strings.Cut(rest, "/")
	seg, _, _ = strings.Cut(seg, "?")
	id, err := url.PathUnescape(seg)
	if err != nil {
		return ""
	}
	return id
}

// BaseURL returns the Data Service URL this client was built with.
func (c *Client) BaseURL() string {
	return c.base
}

// Health probes Data Service reachability (used by pkg/preflight). Returns
// nil on a 2xx response, an error otherwise — do already treats a non-2xx
// status as an error via its default branch, which is exactly the
// "unreachable or unhealthy" signal this needs.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", nil, nil)
}

type meshRelayKey struct{}

// WithMeshRelay marks ctx as serving a request a mesh peer's Mesh Service
// relayed (from node, possibly ""). Every write this client makes under it
// carries contracts.MeshRelayHeader, so the Data Service never treats it as
// the host user's own (docs/MESH-AUTH-DESIGN.md Q2).
func WithMeshRelay(ctx context.Context, node string) context.Context {
	return context.WithValue(ctx, meshRelayKey{}, node)
}

// MeshRelay reports whether ctx serves a mesh-relayed request, and for
// which node.
func MeshRelay(ctx context.Context) (string, bool) {
	node, ok := ctx.Value(meshRelayKey{}).(string)
	return node, ok
}

type meshActorKey struct{}

// WithMeshActor records, on a mesh-relayed ctx (WithMeshRelay), the
// authenticated device the relaying Mesh Service named as the request's
// actor (docs/MESH-AUTH-DESIGN.md D8). Every write this client makes under
// it carries that actor — X-Mads-Actor, and the actor of an appended event
// — in place of the client's own, so the history shows which device did
// what. Without the relay mark it has no effect.
func WithMeshActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, meshActorKey{}, actor)
}

type meshDeviceKey struct{}

// WithMeshDevice records, on a mesh-relayed ctx (WithMeshRelay), the full
// fingerprint of the device the relaying Mesh Service authenticated
// (contracts.MeshDeviceHeader). Every write this client makes under it
// carries that header with the relay mark, so the Data Service can hold
// the device to what its own grant there allows. Without the relay mark it
// has no effect.
func WithMeshDevice(ctx context.Context, fingerprint string) context.Context {
	return context.WithValue(ctx, meshDeviceKey{}, fingerprint)
}

// MeshDevice is ctx's relayed device fingerprint, if any.
func MeshDevice(ctx context.Context) (string, bool) {
	if _, relayed := MeshRelay(ctx); !relayed {
		return "", false
	}
	fp, ok := ctx.Value(meshDeviceKey{}).(string)
	return fp, ok && fp != ""
}

// meshActor is ctx's relayed device actor, if any.
func meshActor(ctx context.Context) (string, bool) {
	if _, relayed := MeshRelay(ctx); !relayed {
		return "", false
	}
	a, ok := ctx.Value(meshActorKey{}).(string)
	return a, ok && a != ""
}

// do runs one request; out, when non-nil, receives the decoded 2xx body.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	return c.doIf(ctx, method, path, "", in, out)
}

// doIf is do with an If-Match precondition (ifMatch non-empty): the
// Data Service answers 409 when the row changed since.
func (c *Client) doIf(ctx context.Context, method, path, ifMatch string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a, ok := meshActor(ctx); ok {
		req.Header.Set("X-Mads-Actor", a)
	} else if c.actor != "" {
		req.Header.Set("X-Mads-Actor", c.actor)
	}
	c.authorize(req)
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	if node, relayed := MeshRelay(ctx); relayed && method != http.MethodGet && method != http.MethodHead {
		req.Header[contracts.MeshRelayHeader] = []string{node}
		if fp, ok := MeshDevice(ctx); ok {
			req.Header.Set(contracts.MeshDeviceHeader, fp)
		}
	}
	if id := pathTaskID(path); id != "" {
		if tok := c.leaseToken(id); tok != "" {
			req.Header.Set(leaseHeader, tok)
		}
	}
	start := c.reach.begin()
	resp, err := c.http.Do(req)
	c.reach.observe(ctx, method+" "+stripQuery(path), start, err)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if out == nil {
			return nil
		}
		return json.Unmarshal(raw, out)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	case resp.StatusCode == http.StatusForbidden:
		return &ForbiddenError{Method: method, Path: path, Code: errorField(raw, "code"), Msg: firstNonEmpty(errorField(raw, "error"), strings.TrimSpace(string(raw)))}
	case resp.StatusCode == http.StatusConflict:
		var body struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(raw, &body) == nil && body.Code == "lease_lost" {
			return fmt.Errorf("%s %s: %w (%w)", method, path, ErrLeaseLost, ErrConflict)
		}
		return &ConflictError{Method: method, Path: path, Code: body.Code, Msg: errorField(raw, "error"), raw: strings.TrimSpace(string(raw))}
	case resp.StatusCode == http.StatusUnprocessableEntity:
		var verr contracts.ValidationError
		if json.Unmarshal(raw, &verr) == nil && !verr.Empty() {
			return &verr
		}
		fallthrough
	default:
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
}

// --- Tasks (spec §3–§4) ---

func (c *Client) CreateTask(ctx context.Context, t *contracts.Task) error {
	return c.do(ctx, http.MethodPost, "/tasks", t, t)
}

// ImportTasks creates a batch atomically (intra-batch dependencies allowed).
func (c *Client) ImportTasks(ctx context.Context, ts []contracts.Task) ([]contracts.Task, error) {
	var wrapper struct {
		Tasks []contracts.Task `json:"tasks"`
	}
	wrapper.Tasks = ts
	err := c.do(ctx, http.MethodPost, "/tasks/import", wrapper, &wrapper)
	return wrapper.Tasks, err
}

func (c *Client) Task(ctx context.Context, id string) (contracts.Task, error) {
	var t contracts.Task
	err := c.do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(id), nil, &t)
	return t, err
}

// Tasks lists by session and optional status.
func (c *Client) Tasks(ctx context.Context, sessionID string, status contracts.TaskStatus) ([]contracts.Task, error) {
	q := url.Values{}
	if sessionID != "" {
		q.Set("session_id", sessionID)
	}
	if status != "" {
		q.Set("status", string(status))
	}
	var out []contracts.Task
	err := c.do(ctx, http.MethodGet, "/tasks?"+q.Encode(), nil, &out)
	return out, err
}

// UpdateTask is the API's full-replace PUT.
func (c *Client) UpdateTask(ctx context.Context, t *contracts.Task) error {
	return c.do(ctx, http.MethodPut, "/tasks/"+url.PathEscape(t.ID), t, t)
}

// ConfirmTask turns a draft task into a real one.
func (c *Client) ConfirmTask(ctx context.Context, taskID string) error {
	return c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/confirm", map[string]any{}, nil)
}

// DiscardTask deletes a draft task. Non-draft tasks cannot be discarded.
func (c *Client) DiscardTask(ctx context.Context, taskID string) error {
	return c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/discard", map[string]any{}, nil)
}

func (c *Client) AddNote(ctx context.Context, taskID string, n *contracts.Note) error {
	return c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/notes", n, n)
}

// ConfirmNote promotes a draft note to confirmed.
func (c *Client) ConfirmNote(ctx context.Context, taskID string, noteID int64) error {
	return c.do(ctx, http.MethodPost,
		fmt.Sprintf("/tasks/%s/notes/%d/confirm", url.PathEscape(taskID), noteID), map[string]any{}, nil)
}

// DiscardNote deletes a draft note. Non-draft notes cannot be discarded.
func (c *Client) DiscardNote(ctx context.Context, taskID string, noteID int64) error {
	return c.do(ctx, http.MethodPost,
		fmt.Sprintf("/tasks/%s/notes/%d/discard", url.PathEscape(taskID), noteID), map[string]any{}, nil)
}

// Cycles returns the session's dependency cycles — legal at rest, they block
// pipeline launch (spec §3.6).
func (c *Client) Cycles(ctx context.Context, sessionID string) ([][]string, error) {
	var wrapper struct {
		Cycles [][]string `json:"cycles"`
	}
	err := c.do(ctx, http.MethodGet, "/tasks/cycles?session_id="+url.QueryEscape(sessionID), nil, &wrapper)
	return wrapper.Cycles, err
}

// --- Leases (spec §15, the parking meter) ---

// Claim takes a free task's lease. holder is the lease holder: empty
// means this client's actor, otherwise it must be the actor or start
// with "<actor>:" (e.g. "pipeline:<runID>"). ErrConflict when the task
// has a live lease, whoever holds it (this client included: renewing is
// Renew's job). On success the client keeps the lease token and sends it
// on every later request about the task (updates, notes, Renew,
// Release), so callers never handle it.
func (c *Client) Claim(ctx context.Context, taskID, holder string, ttl time.Duration) error {
	return c.ClaimWithIdentity(ctx, taskID, holder, ttl, contracts.LeaseIdentity{})
}

// ClaimWithIdentity is Claim plus this process's lease identity (spec:
// reap-on-launch) — recorded so a future engine launch on this machine
// can prove this holder died. identity's zero value means "couldn't
// establish identity" (pkg/procid); the lease then carries none and is
// never provable, same as a pre-migration row.
func (c *Client) ClaimWithIdentity(ctx context.Context, taskID, holder string, ttl time.Duration, identity contracts.LeaseIdentity) error {
	body := map[string]any{}
	if holder != "" {
		body["holder"] = holder
	}
	if ttl > 0 {
		body["ttl_seconds"] = int(ttl.Seconds())
	}
	if identity != (contracts.LeaseIdentity{}) {
		body["identity"] = identity
	}
	var lease contracts.Lease
	if err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/claim", body, &lease); err != nil {
		return err
	}
	if lease.Token == "" {
		return fmt.Errorf("POST /tasks/%s/claim: response carried no lease token", url.PathEscape(taskID))
	}
	c.mu.Lock()
	c.held[taskID] = heldLease{token: lease.Token, holder: lease.Holder}
	c.mu.Unlock()
	return nil
}

// Renew extends this client's lease on the task by its TTL. ErrLeaseLost
// when the lease is gone, expired or not this client's (the client then
// forgets it); it never re-takes a lost lease.
func (c *Client) Renew(ctx context.Context, taskID string) error {
	err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/lease/renew", map[string]any{}, nil)
	if errors.Is(err, ErrLeaseLost) {
		c.forgetLease(taskID)
	}
	return err
}

// Release drops this client's lease on the task. Releasing a free task is
// a no-op; ErrConflict when someone else holds it. Either way the client
// forgets its token; a transport error keeps it so a retry can succeed.
func (c *Client) Release(ctx context.Context, taskID string) error {
	err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/release", map[string]any{}, nil)
	if err == nil || errors.Is(err, ErrConflict) {
		c.forgetLease(taskID)
	}
	return err
}

// ReleaseAll is the supervision call: free everything a dead holder had.
// Tokenless; any of this client's own leases under holder are forgotten.
func (c *Client) ReleaseAll(ctx context.Context, holder, reason string) error {
	err := c.do(ctx, http.MethodPost, "/leases/release-all",
		map[string]any{"holder": holder, "reason": reason}, nil)
	if err == nil {
		c.mu.Lock()
		for id, l := range c.held {
			if l.holder == holder {
				delete(c.held, id)
			}
		}
		c.mu.Unlock()
	}
	return err
}

// Leases lists every live lease held by exactly this client's own actor.
func (c *Client) Leases(ctx context.Context) ([]contracts.Lease, error) {
	var leases []contracts.Lease
	err := c.do(ctx, http.MethodGet, "/leases", nil, &leases)
	return leases, err
}

// LeasesOfKind lists every live lease whose holder is kind or starts with
// "<kind>:" — the reap-on-launch candidate set across runs. kind must be
// this client's actor.
func (c *Client) LeasesOfKind(ctx context.Context, kind string) ([]contracts.Lease, error) {
	var leases []contracts.Lease
	err := c.do(ctx, http.MethodGet, "/leases?kind="+url.QueryEscape(kind), nil, &leases)
	return leases, err
}

// As is a client for the same Data Service under another actor, sharing
// this one's HTTP client. It holds no leases. The Router uses it to read the
// leases of a kind it doesn't own (LeasesOfKind requires the caller's
// actor); attribution, not authentication (spec §15).
func (c *Client) As(actor string) *Client {
	return &Client{base: c.base, actor: actor, http: c.http, token: c.token, reach: c.reach, held: map[string]heldLease{}}
}

// ReclaimLease is the proof-or-nothing transition (reap-on-launch): if
// the lease's holder is of this client's actor's kind and still matches
// identity exactly, it's deleted. The task's status is untouched (an
// in_progress task becomes Interrupted). reclaimed is false when the
// lease moved on since the caller established proof (a safe no-op, not
// an error).
func (c *Client) ReclaimLease(ctx context.Context, taskID string, identity contracts.LeaseIdentity, evidence string) (bool, error) {
	var out struct {
		Reclaimed bool `json:"reclaimed"`
	}
	err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/reclaim",
		map[string]any{"identity": identity, "evidence": evidence}, &out)
	return out.Reclaimed, err
}

// --- Sessions (spec §1) ---

func (c *Client) CreateSession(ctx context.Context, s *contracts.Session) error {
	return c.do(ctx, http.MethodPost, "/sessions", s, s)
}

func (c *Client) UpdateSession(ctx context.Context, s *contracts.Session) error {
	return c.do(ctx, http.MethodPut, "/sessions/"+url.PathEscape(s.ID), s, s)
}

// SetSessionActivity flips a session's Activity field in one atomic round
// trip, unlike Session-then-UpdateSession: that pair is exposed to a
// lost-update race with any other concurrent writer of the same session
// record (see pkg/pipelinerun's activateSession).
func (c *Client) SetSessionActivity(ctx context.Context, id string, activity contracts.SessionActivity) error {
	return c.do(ctx, http.MethodPut, "/sessions/"+url.PathEscape(id)+"/activity",
		map[string]any{"activity": activity}, nil)
}

// SetSessionAside sets or clears the user's "set aside" judgement in one
// atomic round trip, and no-ops when the record already says that — so the
// composer-send path can clear it without reading the session first
// (store.Sessions.SetAside).
func (c *Client) SetSessionAside(ctx context.Context, id string, aside bool) error {
	return c.do(ctx, http.MethodPut, "/sessions/"+url.PathEscape(id)+"/aside",
		map[string]any{"aside": aside}, nil)
}

// SetSessionRailRank records the user's drag position for a session in the
// rail's active list, in one atomic round trip (store.Sessions.SetRailRank).
func (c *Client) SetSessionRailRank(ctx context.Context, id string, rank float64) error {
	return c.do(ctx, http.MethodPut, "/sessions/"+url.PathEscape(id)+"/rail-rank",
		map[string]any{"rank": rank}, nil)
}

// SetSessionAgentState mirrors one main-agent transition onto the session
// record AND appends its session.agent_state event, in one atomic round trip
// — the pair has to land together or a cold reader and a live stream can
// report different things about the same turn (store.Sessions.SetAgentState).
func (c *Client) SetSessionAgentState(ctx context.Context, id string, state contracts.AgentState) error {
	return c.do(ctx, http.MethodPut, "/sessions/"+url.PathEscape(id)+"/agent-state", state, nil)
}

// PutDelegate creates or replaces one delegate record (keyed by session and
// harness id) and announces a real change as session.delegate_changed, in one
// atomic round trip; an unchanged record is a silent no-op, so a retry is
// always safe. Returns the stored record.
func (c *Client) PutDelegate(ctx context.Context, d contracts.Delegate) (contracts.Delegate, error) {
	var out contracts.Delegate
	err := c.do(ctx, http.MethodPut,
		"/sessions/"+url.PathEscape(d.SessionID)+"/delegates/"+url.PathEscape(d.HarnessID), d, &out)
	return out, err
}

// SessionDelegates lists one session's delegates, oldest first.
func (c *Client) SessionDelegates(ctx context.Context, sessionID string) ([]contracts.Delegate, error) {
	var out []contracts.Delegate
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/delegates", nil, &out)
	return out, err
}

// Delegates lists every session's delegates, only those in state when it is
// non-empty.
func (c *Client) Delegates(ctx context.Context, state contracts.DelegateState) ([]contracts.Delegate, error) {
	path := "/delegates"
	if state != "" {
		path += "?state=" + url.QueryEscape(string(state))
	}
	var out []contracts.Delegate
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// DelegatesEndedAfter lists every running delegate plus every finished one
// that ended at or after t.
func (c *Client) DelegatesEndedAfter(ctx context.Context, t time.Time) ([]contracts.Delegate, error) {
	var out []contracts.Delegate
	err := c.do(ctx, http.MethodGet, "/delegates?ended_after="+url.QueryEscape(t.UTC().Format(time.RFC3339Nano)), nil, &out)
	return out, err
}

// RecordSessionTurnEnd stamps LastTurnAt — and LastUsage, when the provider
// reported any — in one atomic round trip, unlike the Session-then-
// UpdateSession pair it replaces: that one could drop a field written
// concurrently by the same runner (its own agent state, its provider ref).
func (c *Client) RecordSessionTurnEnd(ctx context.Context, id string, usage *contracts.Usage, at time.Time) error {
	return c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(id)+"/turn-end",
		map[string]any{"usage": usage, "at": at}, nil)
}

// AddSessionUsage folds one turn's usage into the session's running totals
// atomically (contracts.UsageIncrement).
func (c *Client) AddSessionUsage(ctx context.Context, id string, inc contracts.UsageIncrement) error {
	return c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(id)+"/usage", inc, nil)
}

// SetSessionBilling records the harness's latest billing mode and/or plan
// rate limits; an empty mode or nil limits leaves that half alone.
func (c *Client) SetSessionBilling(ctx context.Context, id string, patch contracts.SessionBillingPatch) error {
	return c.do(ctx, http.MethodPut, "/sessions/"+url.PathEscape(id)+"/billing", patch, nil)
}

// AccountAuthFailed reports a send refused for auth on f.Account
// (docs/AUTH-SIGNIN-DESIGN.md); the Data Service announces
// account.auth_changed if that signs the login out.
func (c *Client) AccountAuthFailed(ctx context.Context, f contracts.AccountAuthFailed) error {
	return c.do(ctx, http.MethodPut, "/accounts/auth/failed", f, nil)
}

// AccountAuthOK reports ok.Account working: a sign-in finished or a turn got
// a real answer. A no-op unless the login is signed out.
func (c *Client) AccountAuthOK(ctx context.Context, ok contracts.AccountAuthOK) error {
	return c.do(ctx, http.MethodPut, "/accounts/auth/ok", ok, nil)
}

// AccountAuths lists every login that has failed auth, signed out or not.
func (c *Client) AccountAuths(ctx context.Context) ([]contracts.AccountAuth, error) {
	var out []contracts.AccountAuth
	err := c.do(ctx, http.MethodGet, "/accounts/auth", nil, &out)
	return out, err
}

// SetSessionWorktree applies a scoped write of the session's isolation
// state (worktree folder/branch/base, D4 counts) in one atomic round trip;
// nil fields are left alone. Like RecordSessionTurnEnd, it can't drop a field
// another writer landed between a read and a write.
func (c *Client) SetSessionWorktree(ctx context.Context, id string, patch contracts.SessionWorktreePatch) error {
	return c.do(ctx, http.MethodPatch, "/sessions/"+url.PathEscape(id)+"/worktree", patch, nil)
}

// SetSessionNotes adds and removes notes owed to the session's agent
// (Session.PendingNotes).
func (c *Client) SetSessionNotes(ctx context.Context, id string, patch contracts.SessionNotesPatch) error {
	return c.do(ctx, http.MethodPatch, "/sessions/"+url.PathEscape(id)+"/notes", patch, nil)
}

func (c *Client) DeleteSession(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(id), nil, nil)
}

func (c *Client) Session(ctx context.Context, id string) (contracts.Session, error) {
	var s contracts.Session
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(id), nil, &s)
	return s, err
}

// Sessions lists sessions, optionally filtered by activity ("active" /
// "inactive"); empty lists every session.
func (c *Client) Sessions(ctx context.Context, activity string) ([]contracts.Session, error) {
	path := "/sessions"
	if activity != "" {
		path += "?activity=" + url.QueryEscape(activity)
	}
	var out []contracts.Session
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// SessionActiveLease reports whether any task under sessionID currently
// has a live write-lease — the Router boot sweep's guard against
// reconciling a session that's genuinely active under an engine it has no
// other way to see (e.g. a CLI `mads run`).
func (c *Client) SessionActiveLease(ctx context.Context, sessionID string) (bool, error) {
	var out struct {
		Active bool `json:"active"`
	}
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/lease-active", nil, &out)
	return out.Active, err
}

// --- Session file tracking & commit linkage (spec §5) ---

// RecordFileChange reports one file the session touched. fc.SessionID
// selects the URL path; the Data Service computes Attributed itself
// (contracts.AttributedChange) — callers must not duplicate that logic.
func (c *Client) RecordFileChange(ctx context.Context, fc *contracts.FileChange) error {
	return c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(fc.SessionID)+"/files", fc, fc)
}

// RecordShellFileChanges reports the files one scan found the session's
// shell commands changed, in one request; the Data Service skips any another
// session's tools already claimed in the same window.
func (c *Client) RecordShellFileChanges(ctx context.Context, sessionID string, changes []contracts.ShellFileChange) (contracts.ShellFileChangesResult, error) {
	var out contracts.ShellFileChangesResult
	err := c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/files/shell",
		contracts.ShellFileChanges{Changes: changes}, &out)
	return out, err
}

// FileChanges lists every file change recorded for sessionID.
func (c *Client) FileChanges(ctx context.Context, sessionID string) ([]contracts.FileChange, error) {
	var list []contracts.FileChange
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/files", nil, &list)
	return list, err
}

// PathTouchers names the session other than exclude that last changed each
// of paths through its own tools; paths nobody else changed are absent.
func (c *Client) PathTouchers(ctx context.Context, paths []string, exclude string) ([]contracts.PathToucher, error) {
	var list []contracts.PathToucher
	if len(paths) == 0 {
		return list, nil
	}
	err := c.do(ctx, http.MethodPost, "/file-changes/touchers",
		contracts.PathTouchersRequest{Paths: paths, ExcludeSession: exclude}, &list)
	return list, err
}

// CommitReport is what the post-commit hook (pkg/githook) sends: the raw
// commit plus whatever the hook itself could read out of the commit
// message. Task-linkage resolution (trailer → session context → heuristic →
// orphan) happens server-side, not here.
type CommitReport struct {
	contracts.Commit
	TrailerTaskID    string   `json:"trailer_task_id,omitempty"`
	HeuristicTaskIDs []string `json:"heuristic_task_ids,omitempty"`
	// Source, Files and Drafted describe a commit mads made itself
	// (contracts.CommitSource*: how many files a Commit took, and whether
	// the Fast Model wrote its message). The Data Service echoes them on
	// commit.created, so the conversation draws a card for exactly these
	// and not for the agent's own commits.
	Source  string `json:"source,omitempty"`
	Files   int    `json:"files,omitempty"`
	Drafted bool   `json:"drafted,omitempty"`
}

// RecordCommit reports one commit for linkage resolution. rep.SessionID
// selects the URL path. Returns the stored contracts.Commit, including the
// LinkedVia/Orphan fields the Data Service resolved.
func (c *Client) RecordCommit(ctx context.Context, rep *CommitReport) (contracts.Commit, error) {
	var out contracts.Commit
	err := c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(rep.SessionID)+"/commits", rep, &out)
	return out, err
}

// DeleteCommit removes one of sessionID's commit records (an undone
// Commit); the Data Service appends commit.undone. ErrNotFound when the
// session never recorded hash.
func (c *Client) DeleteCommit(ctx context.Context, sessionID, hash string) error {
	return c.do(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(sessionID)+"/commits/"+url.PathEscape(hash), nil, nil)
}

// Commits lists every commit recorded for sessionID, with linkage.
func (c *Client) Commits(ctx context.Context, sessionID string) ([]contracts.Commit, error) {
	var list []contracts.Commit
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/commits", nil, &list)
	return list, err
}

// --- Retros (spec §7) ---

func (c *Client) CreateRetro(ctx context.Context, r *contracts.Retro) error {
	return c.do(ctx, http.MethodPost, "/retros", r, r)
}

func (c *Client) Retro(ctx context.Context, id string) (contracts.Retro, error) {
	var r contracts.Retro
	err := c.do(ctx, http.MethodGet, "/retros/"+url.PathEscape(id), nil, &r)
	return r, err
}

// Retros lists every retro, newest first.
func (c *Client) Retros(ctx context.Context) ([]contracts.Retro, error) {
	var out []contracts.Retro
	err := c.do(ctx, http.MethodGet, "/retros", nil, &out)
	return out, err
}

func (c *Client) AcceptDPA(ctx context.Context, retroID, dpaID string) error {
	return c.do(ctx, http.MethodPost, "/retros/"+url.PathEscape(retroID)+"/dpas/"+url.PathEscape(dpaID)+"/accept", map[string]any{}, nil)
}

func (c *Client) RejectDPA(ctx context.Context, retroID, dpaID string) error {
	return c.do(ctx, http.MethodPost, "/retros/"+url.PathEscape(retroID)+"/dpas/"+url.PathEscape(dpaID)+"/reject", map[string]any{}, nil)
}

func (c *Client) EditDPA(ctx context.Context, retroID, dpaID string, edit contracts.DPAEdit) (contracts.DPA, error) {
	var out contracts.DPA
	err := c.do(ctx, http.MethodPatch, "/retros/"+url.PathEscape(retroID)+"/dpas/"+url.PathEscape(dpaID), edit, &out)
	return out, err
}

// SetRetroFollowUpSession links a retro to the follow-up session created from
// its accepted DPAs.
func (c *Client) SetRetroFollowUpSession(ctx context.Context, retroID, sessionID string) error {
	return c.do(ctx, http.MethodPost, "/retros/"+url.PathEscape(retroID)+"/follow-up", map[string]any{"session_id": sessionID}, nil)
}

// --- Synthesis proposals (spec §4) ---

// CreateProposal stores a synthesis proposal and returns it with its
// assigned ID and timestamps.
func (c *Client) CreateProposal(ctx context.Context, p *contracts.Proposal) error {
	return c.do(ctx, http.MethodPost, "/proposals", p, p)
}

// Proposal returns one proposal.
func (c *Client) Proposal(ctx context.Context, id string) (contracts.Proposal, error) {
	var p contracts.Proposal
	err := c.do(ctx, http.MethodGet, "/proposals/"+url.PathEscape(id), nil, &p)
	return p, err
}

// Proposals lists a session's proposals, oldest first.
func (c *Client) Proposals(ctx context.Context, sessionID string) ([]contracts.Proposal, error) {
	var out []contracts.Proposal
	err := c.do(ctx, http.MethodGet, "/proposals?session_id="+url.QueryEscape(sessionID), nil, &out)
	return out, err
}

// ApproveProposal applies a proposed change through the task APIs (actor
// "synthesizer") and returns the approved proposal.
func (c *Client) ApproveProposal(ctx context.Context, id string) (contracts.Proposal, error) {
	var p contracts.Proposal
	err := c.do(ctx, http.MethodPost, "/proposals/"+url.PathEscape(id)+"/approve", map[string]any{}, &p)
	return p, err
}

// DismissProposal marks a proposal dismissed without applying any change.
func (c *Client) DismissProposal(ctx context.Context, id string) (contracts.Proposal, error) {
	var p contracts.Proposal
	err := c.do(ctx, http.MethodPost, "/proposals/"+url.PathEscape(id)+"/dismiss", map[string]any{}, &p)
	return p, err
}

// --- Registries (spec §9, §12, §17) ---

func (c *Client) Agent(ctx context.Context, idOrName string) (contracts.Agent, error) {
	var a contracts.Agent
	err := c.do(ctx, http.MethodGet, "/agents/"+url.PathEscape(idOrName), nil, &a)
	return a, err
}

// Agents lists the full agent registry — the candidate pool for auto-select
// (pkg/agentselect, spec §9).
func (c *Client) Agents(ctx context.Context) ([]contracts.Agent, error) {
	var out []contracts.Agent
	err := c.do(ctx, http.MethodGet, "/agents", nil, &out)
	return out, err
}

// CreateAgent adds an agent to the registry.
func (c *Client) CreateAgent(ctx context.Context, a *contracts.Agent) error {
	return c.do(ctx, http.MethodPost, "/agents", a, a)
}

// UpdateAgent saves changes to an existing agent.
func (c *Client) UpdateAgent(ctx context.Context, a *contracts.Agent) error {
	return c.do(ctx, http.MethodPut, "/agents/"+url.PathEscape(a.ID), a, a)
}

// DeleteAgent removes an agent from the registry.
func (c *Client) DeleteAgent(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/agents/"+url.PathEscape(id), nil, nil)
}

func (c *Client) Capability(ctx context.Context, idOrName string) (contracts.Capability, error) {
	var out contracts.Capability
	err := c.do(ctx, http.MethodGet, "/capabilities/"+url.PathEscape(idOrName), nil, &out)
	return out, err
}

// Capabilities lists the full capability registry — the declaration source
// for the capability runtime's MCP lifecycle and skill injection (spec §12).
func (c *Client) Capabilities(ctx context.Context) ([]contracts.Capability, error) {
	var out []contracts.Capability
	err := c.do(ctx, http.MethodGet, "/capabilities", nil, &out)
	return out, err
}

// CreateCapability adds a capability to the registry.
func (c *Client) CreateCapability(ctx context.Context, capability *contracts.Capability) error {
	return c.do(ctx, http.MethodPost, "/capabilities", capability, capability)
}

// UpdateCapability saves changes to an existing capability.
func (c *Client) UpdateCapability(ctx context.Context, capability *contracts.Capability) error {
	return c.do(ctx, http.MethodPut, "/capabilities/"+url.PathEscape(capability.ID), capability, capability)
}

// DeleteCapability removes a capability from the registry.
func (c *Client) DeleteCapability(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/capabilities/"+url.PathEscape(id), nil, nil)
}

// SetCapabilityHealth records a health check result (spec §12.6): reason
// must never carry a credential value, only capability/env-var names and
// static diagnostic text (spec §13) — this travels as a wire type, so a
// credential value here would violate the boundary.
func (c *Client) SetCapabilityHealth(ctx context.Context, idOrName string, healthy bool, reason string) (contracts.Capability, error) {
	var out contracts.Capability
	body := map[string]any{"healthy": healthy, "reason": reason}
	err := c.do(ctx, http.MethodPost, "/capabilities/"+url.PathEscape(idOrName)+"/health", body, &out)
	return out, err
}

// DiagnoseCapability runs a one-off health check plus troubleshooting
// suggestions (spec §12.7) — a heavier, on-demand sibling of
// SetCapabilityHealth that doesn't persist anything.
func (c *Client) DiagnoseCapability(ctx context.Context, idOrName string) (contracts.CapabilityHealth, []string, error) {
	var out struct {
		Healthy     bool     `json:"healthy"`
		Reason      string   `json:"reason"`
		Suggestions []string `json:"suggestions"`
	}
	err := c.do(ctx, http.MethodPost, "/capabilities/"+url.PathEscape(idOrName)+"/diagnose", nil, &out)
	return contracts.CapabilityHealth{Healthy: out.Healthy, Reason: out.Reason}, out.Suggestions, err
}

func (c *Client) Workspace(ctx context.Context, idOrName string) (contracts.Workspace, error) {
	var out contracts.Workspace
	err := c.do(ctx, http.MethodGet, "/workspaces/"+url.PathEscape(idOrName), nil, &out)
	return out, err
}

// Workspaces lists the full workspace registry (spec §17).
func (c *Client) Workspaces(ctx context.Context) ([]contracts.Workspace, error) {
	var out []contracts.Workspace
	err := c.do(ctx, http.MethodGet, "/workspaces", nil, &out)
	return out, err
}

// CreateWorkspace adds a workspace to the registry.
func (c *Client) CreateWorkspace(ctx context.Context, w *contracts.Workspace) error {
	return c.do(ctx, http.MethodPost, "/workspaces", w, w)
}

// --- Config & Profiles (MADS-UX §3.7 Settings) ---

// Config fetches the global application configuration.
func (c *Client) Config(ctx context.Context) (contracts.Config, error) {
	var out contracts.Config
	err := c.do(ctx, http.MethodGet, "/config", nil, &out)
	return out, err
}

// UpdateConfig replaces the global configuration, validated server-side.
func (c *Client) UpdateConfig(ctx context.Context, cfg *contracts.Config) error {
	return c.do(ctx, http.MethodPut, "/config", cfg, cfg)
}

// Profiles lists every settings profile (MADS-UX §10.5).
func (c *Client) Profiles(ctx context.Context) ([]contracts.Profile, error) {
	var out []contracts.Profile
	err := c.do(ctx, http.MethodGet, "/profiles", nil, &out)
	return out, err
}

// CreateProfile adds a profile.
func (c *Client) CreateProfile(ctx context.Context, p *contracts.Profile) error {
	return c.do(ctx, http.MethodPost, "/profiles", p, p)
}

// UpdateProfile replaces a profile's fields.
func (c *Client) UpdateProfile(ctx context.Context, p *contracts.Profile) error {
	return c.do(ctx, http.MethodPut, "/profiles/"+url.PathEscape(p.ID), p, p)
}

// DeleteProfile removes a profile. System profiles refuse.
func (c *Client) DeleteProfile(ctx context.Context, idOrName string) error {
	return c.do(ctx, http.MethodDelete, "/profiles/"+url.PathEscape(idOrName), nil, nil)
}

// ActivateProfile merges a profile's settings onto the global Config,
// returning the merged result.
func (c *Client) ActivateProfile(ctx context.Context, idOrName string) (contracts.Config, error) {
	var out contracts.Config
	err := c.do(ctx, http.MethodPost, "/profiles/"+url.PathEscape(idOrName)+"/activate", map[string]any{}, &out)
	return out, err
}

// UpdateWorkspace saves changes to an existing workspace.
func (c *Client) UpdateWorkspace(ctx context.Context, w *contracts.Workspace) error {
	return c.do(ctx, http.MethodPut, "/workspaces/"+url.PathEscape(w.ID), w, w)
}

// DeleteWorkspace removes a workspace from the registry.
func (c *Client) DeleteWorkspace(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/workspaces/"+url.PathEscape(id), nil, nil)
}

// --- Mesh peers (docs/MESH-MODE-DESIGN.md D3) ---

// Peers lists the mesh peer list: the devices this node views and the
// paired-only devices (Viewed false) it doesn't.
func (c *Client) Peers(ctx context.Context) ([]contracts.Peer, error) {
	var out []contracts.Peer
	err := c.do(ctx, http.MethodGet, "/peers", nil, &out)
	return out, err
}

// Peer fetches one peer by id or name.
func (c *Client) Peer(ctx context.Context, idOrName string) (contracts.Peer, error) {
	var out contracts.Peer
	err := c.do(ctx, http.MethodGet, "/peers/"+url.PathEscape(idOrName), nil, &out)
	return out, err
}

// CreatePeer adds a device to the mesh peer list.
func (c *Client) CreatePeer(ctx context.Context, p *contracts.Peer) error {
	return c.do(ctx, http.MethodPost, "/peers", p, p)
}

// UpdatePeer saves changes to a peer (rename, hide).
func (c *Client) UpdatePeer(ctx context.Context, p *contracts.Peer) error {
	return c.do(ctx, http.MethodPut, "/peers/"+url.PathEscape(p.ID), p, p)
}

// DeletePeer removes a device from the peer list.
func (c *Client) DeletePeer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/peers/"+url.PathEscape(id), nil, nil)
}

// UpdatePeerIfUnchanged is UpdatePeer only if the row is still as read
// (p.UpdatedAt); otherwise ErrConflict and nothing written. A read, edit,
// write-back uses it so a change made meanwhile (a pairing, a revoke)
// isn't overwritten.
func (c *Client) UpdatePeerIfUnchanged(ctx context.Context, p *contracts.Peer) error {
	return c.doIf(ctx, http.MethodPut, "/peers/"+url.PathEscape(p.ID), p.UpdatedAt.Format(time.RFC3339Nano), p, p)
}

// DeletePeerIfUnchanged is DeletePeer only if the row is still as read.
func (c *Client) DeletePeerIfUnchanged(ctx context.Context, p contracts.Peer) error {
	return c.doIf(ctx, http.MethodDelete, "/peers/"+url.PathEscape(p.ID), p.UpdatedAt.Format(time.RFC3339Nano), nil, nil)
}

// --- Pairing by code (docs/MESH-AUTH-DESIGN.md D6) ---

// CreatePairingCode registers a new pairing code by its secret's hash
// (paircode.Hash); the secret never reaches the Data Service.
func (c *Client) CreatePairingCode(ctx context.Context, secretHash string) (contracts.PairingCode, error) {
	var out contracts.PairingCode
	err := c.do(ctx, http.MethodPost, "/pairing/codes", contracts.PairingCodeCreate{SecretHash: secretHash}, &out)
	return out, err
}

// DeletePairingCode retires an unused code.
func (c *Client) DeletePairingCode(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/pairing/codes/"+url.PathEscape(id), nil, nil)
}

// RedeemPairingCode consumes a code for the device that presented its
// secret, opening a request for the host to answer. A bad code is a
// *ForbiddenError with Code "pairing_refused".
func (c *Client) RedeemPairingCode(ctx context.Context, in contracts.PairRedeem) (contracts.PairRequest, error) {
	var out contracts.PairRequest
	err := c.do(ctx, http.MethodPost, "/pairing/redeem", in, &out)
	return out, err
}

// PairRequests lists open and recently answered pairing requests.
func (c *Client) PairRequests(ctx context.Context) ([]contracts.PairRequest, error) {
	var out []contracts.PairRequest
	err := c.do(ctx, http.MethodGet, "/pairing/requests", nil, &out)
	return out, err
}

// PairRequest reads one pairing request.
func (c *Client) PairRequest(ctx context.Context, id string) (contracts.PairRequest, error) {
	var out contracts.PairRequest
	err := c.do(ctx, http.MethodGet, "/pairing/requests/"+url.PathEscape(id), nil, &out)
	return out, err
}

// ApprovePairRequest pins the requesting device at d.Level.
func (c *Client) ApprovePairRequest(ctx context.Context, id string, d contracts.PairDecision) (contracts.PairRequest, error) {
	var out contracts.PairRequest
	err := c.do(ctx, http.MethodPost, "/pairing/requests/"+url.PathEscape(id)+"/approve", d, &out)
	return out, err
}

// DeclinePairRequest declines a pairing request.
func (c *Client) DeclinePairRequest(ctx context.Context, id string) (contracts.PairRequest, error) {
	var out contracts.PairRequest
	err := c.do(ctx, http.MethodPost, "/pairing/requests/"+url.PathEscape(id)+"/decline", struct{}{}, &out)
	return out, err
}

// DeviceActivity is the device activity log, newest first: device.*
// events and events by a device actor. fingerprint "" is every device;
// before 0 starts at the newest; limit 0 is the service's cap.
func (c *Client) DeviceActivity(ctx context.Context, fingerprint string, before int64, limit int) ([]contracts.Envelope, error) {
	q := url.Values{}
	if fingerprint != "" {
		q.Set("device", fingerprint)
	}
	if before > 0 {
		q.Set("before", fmt.Sprint(before))
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	var out []contracts.Envelope
	err := c.do(ctx, http.MethodGet, "/devices/activity?"+q.Encode(), nil, &out)
	return out, err
}

// DeviceLastUsed is each device actor's newest event.
func (c *Client) DeviceLastUsed(ctx context.Context) ([]contracts.DeviceLastUsed, error) {
	var out []contracts.DeviceLastUsed
	err := c.do(ctx, http.MethodGet, "/devices/last-used", nil, &out)
	return out, err
}

// MeshSettings is this host's mesh settings (GET /devices/settings): the
// zero value when none were ever set.
func (c *Client) MeshSettings(ctx context.Context) (contracts.MeshSettings, error) {
	var out contracts.MeshSettings
	err := c.do(ctx, http.MethodGet, "/devices/settings", nil, &out)
	return out, err
}

// SetMeshSettings records this host's mesh settings (PUT
// /devices/settings). Only this host's own user may: a relayed write is
// refused.
func (c *Client) SetMeshSettings(ctx context.Context, s contracts.MeshSettings) (contracts.MeshSettings, error) {
	var out contracts.MeshSettings
	err := c.do(ctx, http.MethodPut, "/devices/settings", s, &out)
	return out, err
}

// --- Templates (spec §6) ---

// Template fetches by id or name; used_by is dropped — callers wanting
// binding info talk to the endpoint directly.
func (c *Client) Template(ctx context.Context, idOrName string) (contracts.Template, error) {
	var wrapper struct {
		Template contracts.Template `json:"template"`
	}
	err := c.do(ctx, http.MethodGet, "/templates/"+url.PathEscape(idOrName), nil, &wrapper)
	return wrapper.Template, err
}

// Templates lists the full template registry.
func (c *Client) Templates(ctx context.Context) ([]contracts.Template, error) {
	var out []contracts.Template
	err := c.do(ctx, http.MethodGet, "/templates", nil, &out)
	return out, err
}

// TemplateDetail fetches a template plus the workspace names bound to it
// (M9's "used by mads-core, mads-ui" card copy).
func (c *Client) TemplateDetail(ctx context.Context, idOrName string) (contracts.Template, []string, error) {
	var wrapper struct {
		Template contracts.Template `json:"template"`
		UsedBy   []string           `json:"used_by"`
	}
	err := c.do(ctx, http.MethodGet, "/templates/"+url.PathEscape(idOrName), nil, &wrapper)
	return wrapper.Template, wrapper.UsedBy, err
}

// CreateTemplate adds a template to the registry.
func (c *Client) CreateTemplate(ctx context.Context, t *contracts.Template) error {
	return c.do(ctx, http.MethodPost, "/templates", t, t)
}

// UpdateTemplate saves changes to an existing template.
func (c *Client) UpdateTemplate(ctx context.Context, t *contracts.Template) error {
	return c.do(ctx, http.MethodPut, "/templates/"+url.PathEscape(t.ID), t, t)
}

// DeleteTemplate removes a template from the registry.
func (c *Client) DeleteTemplate(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/templates/"+url.PathEscape(id), nil, nil)
}

// ValidateTemplate runs the launch-equivalent checks against source without
// storing anything (M9's editor Validate button). Returns nil when valid;
// on failure, do()'s generic 422 handling already decodes the body into
// *contracts.ValidationError.
func (c *Client) ValidateTemplate(ctx context.Context, source string) error {
	return c.do(ctx, http.MethodPost, "/templates/validate", map[string]string{"source": source}, nil)
}

// TemplateValidation is one row of the Data Service's validate-all report
// (services/data/internal/api/templates.go's handleValidateAll). Mirrored
// here rather than shared via pkg/contracts because it's a response DTO for
// this one report, not a stored record like contracts.Template.
type TemplateValidation struct {
	ID     string                 `json:"id"`
	Name   string                 `json:"name"`
	Valid  bool                   `json:"valid"`
	Errors []contracts.FieldError `json:"errors"`
}

// ValidateAllTemplates re-validates every stored template — the discovery
// surface for pkg/template's now-strict grammar (a template saved under the
// old, lax one can be invalid today even though nothing new can be saved
// invalid). Always succeeds at the HTTP level even when some templates are
// invalid: that's data in the response, not a request failure.
func (c *Client) ValidateAllTemplates(ctx context.Context) ([]TemplateValidation, error) {
	var wrapper struct {
		Results []TemplateValidation `json:"results"`
	}
	err := c.do(ctx, http.MethodPost, "/templates/validate-all", nil, &wrapper)
	return wrapper.Results, err
}

// --- Links (docs/LINKED-SESSIONS-DESIGN.md) ---

// CreateLink creates a link from a preset or an explicit grant set — the
// server resolves req into the stored grant set (and refuses the
// system-only hand_raiser preset).
func (c *Client) CreateLink(ctx context.Context, req contracts.LinkRequest) (contracts.Link, error) {
	var out contracts.Link
	err := c.do(ctx, http.MethodPost, "/links", req, &out)
	return out, err
}

// ValidateLinks dry-runs staged links (one endpoint of each is
// contracts.NewSessionPlaceholder) through the Data Service's link rules
// without creating anything. Returns the field errors found, nil when the
// links would be accepted; a missing counterpart session is ErrNotFound.
func (c *Client) ValidateLinks(ctx context.Context, reqs []contracts.LinkRequest) (*contracts.ValidationError, error) {
	var out contracts.ValidationError
	if err := c.do(ctx, http.MethodPost, "/links/validate", map[string]any{"links": reqs}, &out); err != nil {
		return nil, err
	}
	if out.Empty() {
		return nil, nil
	}
	return &out, nil
}

// Link fetches one link by id.
func (c *Client) Link(ctx context.Context, id string) (contracts.Link, error) {
	var out contracts.Link
	err := c.do(ctx, http.MethodGet, "/links/"+url.PathEscape(id), nil, &out)
	return out, err
}

// UpdateLink changes an existing link's grant set. GrantingSessionID and
// SubjectSessionID in req are ignored server-side — they're immutable after
// creation.
func (c *Client) UpdateLink(ctx context.Context, id string, req contracts.LinkRequest) (contracts.Link, error) {
	var out contracts.Link
	err := c.do(ctx, http.MethodPut, "/links/"+url.PathEscape(id), req, &out)
	return out, err
}

// DeleteLink removes a link.
func (c *Client) DeleteLink(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/links/"+url.PathEscape(id), nil, nil)
}

// SetLinkOrchestrateStatus records whether a link's orchestrate grant has
// materialized as live tools yet. Called by the granting session's own
// runner (services/session/internal/runner) after Start-time materialization
// or a RefreshOrchestrate cycle — never by the UI.
//
// errMsg is the reason for OrchestrateFailed; pass "" for any other status.
func (c *Client) SetLinkOrchestrateStatus(ctx context.Context, id string, status contracts.OrchestrateStatus, errMsg string) (contracts.Link, error) {
	var out contracts.Link
	body := struct {
		Status contracts.OrchestrateStatus `json:"status"`
		Error  string                      `json:"error,omitempty"`
	}{Status: status, Error: errMsg}
	err := c.do(ctx, http.MethodPut, "/links/"+url.PathEscape(id)+"/orchestrate-status", body, &out)
	return out, err
}

// LinksForSession lists links where sessionID is on either side (granting
// or subject).
func (c *Client) LinksForSession(ctx context.Context, sessionID string) ([]contracts.Link, error) {
	var out []contracts.Link
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/links", nil, &out)
	return out, err
}

// LinksForSessions lists links with any of sessionIDs on either side, each
// link once, ordered by creation — the bulk form of LinksForSession. The
// Data Service refuses more than contracts.MaxLinkSessionIDs ids; callers
// chunk. An empty sessionIDs returns an empty list without a request.
func (c *Client) LinksForSessions(ctx context.Context, sessionIDs []string) ([]contracts.Link, error) {
	if len(sessionIDs) == 0 {
		return []contracts.Link{}, nil
	}
	q := url.Values{"session_ids": {strings.Join(sessionIDs, ",")}}
	var out []contracts.Link
	err := c.do(ctx, http.MethodGet, "/links?"+q.Encode(), nil, &out)
	return out, err
}

// AppendLinkDigest queues one compressed digest on an on_signal link's
// mailbox (docs/LINKED-SESSIONS-DESIGN.md decision 4). Capped server-side;
// oldest entries drop first.
func (c *Client) AppendLinkDigest(ctx context.Context, id string, d contracts.LinkDigest) error {
	return c.do(ctx, http.MethodPost, "/links/"+url.PathEscape(id)+"/digests", d, nil)
}

// DrainLinkDigests atomically returns and clears a link's mailbox, oldest
// first. An unknown link drains empty.
func (c *Client) DrainLinkDigests(ctx context.Context, id string) ([]contracts.LinkDigest, error) {
	var out []contracts.LinkDigest
	err := c.do(ctx, http.MethodPost, "/links/"+url.PathEscape(id)+"/digests/drain", nil, &out)
	return out, err
}

// DrainDeferredLinkDigests atomically returns and clears the pushes deferred
// on a link because its granting session was asleep, oldest first. On_signal
// backlog is left in place (DrainLinkDigests drains only that).
func (c *Client) DrainDeferredLinkDigests(ctx context.Context, id string) ([]contracts.LinkDigest, error) {
	var out []contracts.LinkDigest
	err := c.do(ctx, http.MethodPost, "/links/"+url.PathEscape(id)+"/digests/drain?deferred=1", nil, &out)
	return out, err
}

// HeldUpdates lists the deferred pushes waiting for sessionID (and the ones
// discarded since its last drain), oldest first.
func (c *Client) HeldUpdates(ctx context.Context, sessionID string) (contracts.HeldUpdates, error) {
	var out contracts.HeldUpdates
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/held-updates", nil, &out)
	return out, err
}

// DiscardLinkDigests soft-deletes held updates by id; RestoreLinkDigests
// undoes it. Both return how many entries changed.
func (c *Client) DiscardLinkDigests(ctx context.Context, ids []int64) (int, error) {
	return c.setLinkDigestsDiscarded(ctx, "/link-digests/discard", ids)
}

// RestoreLinkDigests: see DiscardLinkDigests.
func (c *Client) RestoreLinkDigests(ctx context.Context, ids []int64) (int, error) {
	return c.setLinkDigestsDiscarded(ctx, "/link-digests/restore", ids)
}

func (c *Client) setLinkDigestsDiscarded(ctx context.Context, path string, ids []int64) (int, error) {
	if ids == nil {
		ids = []int64{}
	}
	var out contracts.LinkDigestsChanged
	err := c.do(ctx, http.MethodPost, path, contracts.LinkDigestIDs{IDs: ids}, &out)
	return out.Changed, err
}

// DeferredLinkCounts returns, keyed by granting session, how many deferred
// pushes wait for it.
func (c *Client) DeferredLinkCounts(ctx context.Context) (map[string]int, error) {
	var out map[string]int
	err := c.do(ctx, http.MethodGet, "/links/deferred-counts", nil, &out)
	return out, err
}

// CreateHandRaiser creates (or returns the existing) implicit self-referential
// hand-raiser link for sessionID (docs/LINKED-SESSIONS-DESIGN.md decision 6).
// System-only: meant to be called by the Router's Supervisor.
func (c *Client) CreateHandRaiser(ctx context.Context, sessionID string) (contracts.Link, error) {
	var out contracts.Link
	err := c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/hand-raiser", nil, &out)
	return out, err
}

// RemoveHandRaiser deletes sessionID's implicit hand-raiser link, if any.
// No-op, successful if none exists.
func (c *Client) RemoveHandRaiser(ctx context.Context, sessionID string) error {
	return c.do(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(sessionID)+"/hand-raiser", nil, nil)
}

// --- Events (spec §8, §15) ---

// Append posts one standalone event (envelope minus seq/timestamp); the
// client's actor fills the envelope unless the caller set one.
func (c *Client) Append(ctx context.Context, env *contracts.Envelope) error {
	if a, ok := meshActor(ctx); ok {
		env.Actor = a // a relayed device's action is that device's (D8)
	} else if env.Actor == "" {
		env.Actor = c.actor
	}
	return c.do(ctx, http.MethodPost, "/events", env, env)
}

// Stream opens a filtered SSE subscription (history from the cursor, then
// live pushes). Envelopes arrive on the returned channel until ctx ends or
// the connection drops; the channel then closes. Callers resume by passing
// the last envelope's Seq as since — reconnection policy is theirs.
func (c *Client) Stream(ctx context.Context, since int64, sessionID string, types []string) (<-chan contracts.Envelope, error) {
	q := url.Values{}
	q.Set("since", fmt.Sprint(since))
	if sessionID != "" {
		q.Set("session_id", sessionID)
	}
	if len(types) > 0 {
		q.Set("types", strings.Join(types, ","))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/events?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	c.authorize(req)
	start := c.reach.begin()
	resp, err := (&http.Client{}).Do(req) // no timeout: the stream lives until ctx ends
	c.reach.observe(ctx, "GET /events (stream)", start, err)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("stream: status %d", resp.StatusCode)
	}
	ch := make(chan contracts.Envelope, 64)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var env contracts.Envelope
			if json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &env) != nil {
				continue
			}
			select {
			case ch <- env:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// LatestSeq is the seq of the newest event matching sessionID and types
// (0 if none): where a reader that has just loaded current state starts
// following. It pages through GET /events, which caps each batch, so the
// answer is the true latest, not the last of the first batch.
func (c *Client) LatestSeq(ctx context.Context, sessionID string, types []string) (int64, error) {
	var since int64
	for {
		evs, err := c.Events(ctx, since, sessionID, types)
		if err != nil {
			return since, err
		}
		if len(evs) == 0 {
			return since, nil
		}
		since = evs[len(evs)-1].Seq
	}
}

// Events fetches one filtered batch after the cursor (capped server-side;
// page with since = last seq).
func (c *Client) Events(ctx context.Context, since int64, sessionID string, types []string) ([]contracts.Envelope, error) {
	q := url.Values{}
	q.Set("since", fmt.Sprint(since))
	if sessionID != "" {
		q.Set("session_id", sessionID)
	}
	if len(types) > 0 {
		q.Set("types", strings.Join(types, ","))
	}
	var out []contracts.Envelope
	err := c.do(ctx, http.MethodGet, "/events?"+q.Encode(), nil, &out)
	return out, err
}

// EventsHead is GET /events/head: the log's current max seq (0 when empty).
func (c *Client) EventsHead(ctx context.Context) (int64, error) {
	var out struct {
		Seq int64 `json:"seq"`
	}
	err := c.do(ctx, http.MethodGet, "/events/head", nil, &out)
	return out.Seq, err
}

// --- Metrics (spec §8) ---

// MetricsCost fetches usage.recorded events sliced by day/model, session, and
// pipeline run. sessionID scopes to one session; empty means every session.
func (c *Client) MetricsCost(ctx context.Context, sessionID string) (contracts.CostMetrics, error) {
	q := url.Values{}
	if sessionID != "" {
		q.Set("session_id", sessionID)
	}
	var out contracts.CostMetrics
	err := c.do(ctx, http.MethodGet, "/metrics/cost?"+q.Encode(), nil, &out)
	return out, err
}

// MetricsAgentLeaderboard fetches each agent's usage, success rate, and
// retries, system-wide.
func (c *Client) MetricsAgentLeaderboard(ctx context.Context) ([]contracts.AgentLeaderboardEntry, error) {
	var out []contracts.AgentLeaderboardEntry
	err := c.do(ctx, http.MethodGet, "/metrics/agents", nil, &out)
	return out, err
}

// MetricsTaskOutcomes fetches the confirmed-task status distribution.
// sessionID scopes to one session; empty means every session.
func (c *Client) MetricsTaskOutcomes(ctx context.Context, sessionID string) (contracts.TaskOutcomes, error) {
	q := url.Values{}
	if sessionID != "" {
		q.Set("session_id", sessionID)
	}
	var out contracts.TaskOutcomes
	err := c.do(ctx, http.MethodGet, "/metrics/tasks?"+q.Encode(), nil, &out)
	return out, err
}

// MetricsCapabilityUsage fetches every registered capability's invocation
// count, including never-used ones.
func (c *Client) MetricsCapabilityUsage(ctx context.Context) ([]contracts.CapabilityUsage, error) {
	var out []contracts.CapabilityUsage
	err := c.do(ctx, http.MethodGet, "/metrics/capabilities", nil, &out)
	return out, err
}

// MetricsPipelineRuns fetches reconstructed pipeline run history. sessionID
// scopes to one session; empty means every session.
func (c *Client) MetricsPipelineRuns(ctx context.Context, sessionID string) ([]contracts.PipelineRun, error) {
	q := url.Values{}
	if sessionID != "" {
		q.Set("session_id", sessionID)
	}
	var out []contracts.PipelineRun
	err := c.do(ctx, http.MethodGet, "/metrics/pipelines?"+q.Encode(), nil, &out)
	return out, err
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
