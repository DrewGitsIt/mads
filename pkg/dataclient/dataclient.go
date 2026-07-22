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
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// ErrNotFound maps 404s.
var ErrNotFound = errors.New("not found")

// ErrConflict maps 409s — for tasks, that's "live-leased by someone else".
var ErrConflict = errors.New("conflict")

type Client struct {
	base  string
	actor string
	http  *http.Client
}

// New builds a client. actor names the mutation identity this client rides
// under (attribution, not authentication — spec §15); empty means the
// server-side default, "user".
func New(baseURL, actor string) *Client {
	return &Client{
		base:  strings.TrimSuffix(baseURL, "/"),
		actor: actor,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
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

// do runs one request; out, when non-nil, receives the decoded 2xx body.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
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
	if c.actor != "" {
		req.Header.Set("X-Mads-Actor", c.actor)
	}
	resp, err := c.http.Do(req)
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
	case resp.StatusCode == http.StatusConflict:
		return fmt.Errorf("%s %s: %w: %s", method, path, ErrConflict, strings.TrimSpace(string(raw)))
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

// Claim takes or renews the task's lease for this client's actor.
// ErrConflict when someone else holds it.
func (c *Client) Claim(ctx context.Context, taskID string, ttl time.Duration) error {
	body := map[string]any{}
	if ttl > 0 {
		body["ttl_seconds"] = int(ttl.Seconds())
	}
	return c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/claim", body, nil)
}

func (c *Client) Release(ctx context.Context, taskID string) error {
	return c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/release", map[string]any{}, nil)
}

// ReleaseAll is the supervision call: free everything a dead holder had.
func (c *Client) ReleaseAll(ctx context.Context, holder, reason string) error {
	return c.do(ctx, http.MethodPost, "/leases/release-all",
		map[string]any{"holder": holder, "reason": reason}, nil)
}

// ClaimWithIdentity is Claim plus this process's lease identity (spec:
// reap-on-launch) — recorded so a future engine launch on this machine
// can prove this holder died. identity's zero value means "couldn't
// establish identity" (pkg/procid); the lease then carries none and is
// never provable, same as a pre-migration row.
func (c *Client) ClaimWithIdentity(ctx context.Context, taskID string, ttl time.Duration, identity contracts.LeaseIdentity) error {
	body := map[string]any{}
	if ttl > 0 {
		body["ttl_seconds"] = int(ttl.Seconds())
	}
	if identity != (contracts.LeaseIdentity{}) {
		body["identity"] = identity
	}
	return c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskID)+"/claim", body, nil)
}

// Leases lists every live lease held by this client's own actor — the
// reap-on-launch candidate set.
func (c *Client) Leases(ctx context.Context) ([]contracts.Lease, error) {
	var leases []contracts.Lease
	err := c.do(ctx, http.MethodGet, "/leases", nil, &leases)
	return leases, err
}

// ReclaimLease is the proof-or-nothing transition (reap-on-launch): if
// the lease still matches identity exactly, it's released and the task
// requeued in_progress -> ready with evidence on the event. reclaimed is
// false when the lease moved on since the caller established proof (a
// safe no-op, not an error).
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

// FileChanges lists every file change recorded for sessionID.
func (c *Client) FileChanges(ctx context.Context, sessionID string) ([]contracts.FileChange, error) {
	var list []contracts.FileChange
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/files", nil, &list)
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
}

// RecordCommit reports one commit for linkage resolution. rep.SessionID
// selects the URL path. Returns the stored contracts.Commit, including the
// LinkedVia/Orphan fields the Data Service resolved.
func (c *Client) RecordCommit(ctx context.Context, rep *CommitReport) (contracts.Commit, error) {
	var out contracts.Commit
	err := c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(rep.SessionID)+"/commits", rep, &out)
	return out, err
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

// --- Links (docs/LINKED-SESSIONS-DESIGN.md) ---

// CreateLink creates a link from a preset or an explicit grant set — the
// server resolves req into the stored grant set (and refuses the
// system-only hand_raiser preset).
func (c *Client) CreateLink(ctx context.Context, req contracts.LinkRequest) (contracts.Link, error) {
	var out contracts.Link
	err := c.do(ctx, http.MethodPost, "/links", req, &out)
	return out, err
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

// LinksForSession lists links where sessionID is on either side (granting
// or subject).
func (c *Client) LinksForSession(ctx context.Context, sessionID string) ([]contracts.Link, error) {
	var out []contracts.Link
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID)+"/links", nil, &out)
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
	if env.Actor == "" {
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
	resp, err := (&http.Client{}).Do(req) // no timeout: the stream lives until ctx ends
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
