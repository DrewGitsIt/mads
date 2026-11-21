// Package authz decides which Router routes a paired mesh device may
// reach, and with which grant level (docs/MESH-AUTH-DESIGN.md D3, D4).
//
// The Mesh Service presents this node's install token to the Router's front
// door (docs/LOCAL-FRONT-DOOR-DESIGN.md), so the Router trusts what it
// forwards as fully as the user's own calls: this table is the whole
// authorisation boundary for mesh traffic. It is default-deny: a route that
// isn't listed here, or that is listed as Deny, is never forwarded.
// routes_test.go fails when the Router registers a route this table doesn't
// classify, so a new route can't slip through by omission.
//
// The four grant levels (contracts.GrantLevel: view, chat, control, admin)
// are named presets over these classes: each route is classified with the
// lowest level that may use it, and a level reaches every route of its own
// class and the classes below it. The ceilings (D4: C1 full-trust chat, C2
// no shell, C3 trust ceiling) hold whatever the level; the route sets they
// apply to are below the table.
package authz

import (
	"net/http"
	"slices"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Class is the lowest grant level a route needs, or Deny.
type Class string

const (
	// View is read-only: what the session list and conversation view need,
	// plus the host-scoped reads of D3b (providers, agents, workspaces).
	View Class = "view"
	// Chat is view plus sending to, interrupting and answering permission
	// prompts for existing sessions at their existing trust (D3, Q4): a
	// misused chat key is confined to the sessions you have, at the trust
	// you chose, with every message in their transcripts.
	Chat Class = "chat"
	// Control is everything a user does with sessions: start them (any
	// folder, any trust), raise or lower trust, the agent's terminal,
	// commit/land/push, archive, restart.
	Control Class = "control"
	// Admin is control plus pairing, grants and introductions on this host
	// (D5): the four /devices routes below. The Router's /peers stays Deny
	// over the mesh.
	Admin Class = "admin"
	// Deny is never forwarded, whatever the device holds.
	Deny Class = "deny"
)

// Routes classifies every route the Router registers, by its exact ServeMux
// pattern. Keep it in the Router's registration order's spirit: grouped by
// surface, Deny entries kept so the guard test can tell "decided no" from
// "forgot".
var Routes = map[string]Class{
	// Service identity.
	"GET /health":  View,
	"GET /version": View,

	// Sessions: list, read, and the live streams.
	"GET /sessions":                                       View,
	"GET /sessions/{id}":                                  View,
	"GET /sessions/{id}/transcript":                       View,
	"GET /sessions/{id}/output":                           View,
	"GET /sessions/{id}/queue":                            View,
	"GET /sessions/{id}/permission-requests":              View,
	"GET /sessions/{id}/held-updates":                     View,
	"GET /sessions/{id}/transport":                        View,
	"GET /sessions/{id}/attachments/{aid}":                View,
	"GET /sessions/{id}/attachments/{aid}/thumbnail":      View,
	"GET /sessions/{id}/report":                           View,
	"GET /sessions/{id}/auth":                             View,
	"GET /sessions/{id}/export":                           View,
	"GET /sessions/{id}/links":                            View,
	"GET /sessions/{id}/delegates":                        View,
	"GET /sessions/{id}/delegates/{harnessId}/transcript": View,
	"GET /sessions/{id}/orchestrate/subjects":             View,
	"GET /sessions/{id}/orchestrate/summary":              View,
	"GET /sessions/{id}/orchestrate/turns":                View,
	"GET /sessions/{id}/pipeline/pickup":                  View,
	"GET /sessions/{id}/pipeline/tasks/{taskId}/output":   View,
	"GET /sessions/{id}/pipeline/tasks/{taskId}/turns":    View,
	"GET /sessions/{id}/files":                            View,
	"GET /sessions/{id}/commits":                          View,
	"GET /sessions/{id}/commits/{hash}/diff":              View,
	"GET /sessions/{id}/changes":                          View,
	"GET /sessions/{id}/changeset":                        View,
	"GET /sessions/{id}/worktree":                         View,
	"GET /sessions/{id}/commit-plan":                      View,
	"GET /sessions/{id}/push":                             View,
	"GET /sessions/{id}/move/folders":                     View,
	"GET /sessions/{id}/tool-diff":                        View,
	// A background tool's live output: the conversation view's tool rows.
	"GET /sessions/{id}/tools/{toolUseId}/output": View,
	// Repo state (docs/COMMIT-LAND-PUSH-DESIGN.md D10), scoped to one
	// session's repos: the branch facts and the Land preview.
	"GET /sessions/{id}/repos": View,
	"GET /sessions/{id}/land":  View,
	// A POST, but only a recount of the session\'s uncommitted changes
	// (cached state, no git write); the conversation view sends it on open.
	"POST /sessions/{id}/worktree/refresh": View,
	"GET /events":                          View,
	// The log's current max seq: where a viewer's live-only bus for one of
	// this host's sessions starts (ui/src/router/events.ts).
	"GET /events/head":           View,
	"GET /pipelines/{id}/output": View,
	"GET /delegates":             View,
	"GET /links/{id}":            View,
	// The links among the sessions named in ?session_ids=: what the rail's
	// link pills read.
	"GET /links": View,

	// Host-scoped reads the conversation view and launchpad make (D3b).
	"GET /providers":               View,
	"GET /providers/behaviors":     View,
	"GET /providers/{name}/models": View,
	"GET /agents":                  View,
	"GET /agents/{id}":             View,
	"GET /workspaces":              View,
	"GET /workspaces/{id}":         View,
	"GET /capabilities":            View,
	"GET /capabilities/{id}":       View,
	"GET /templates":               View,
	"GET /templates/{id}":          View,
	"GET /tasks":                   View,
	"GET /tasks/cycles":            View,
	"GET /tasks/{id}":              View,

	// Chat: driving an existing session at its existing trust. Sending is
	// subject to C1 (FullTrustChatRoutes); attachments ride with a message.
	"POST /sessions/{id}/messages":                            Chat,
	"POST /sessions/{id}/interrupt":                           Chat,
	"POST /sessions/{id}/permission-requests/{reqId}/resolve": Chat,
	"DELETE /sessions/{id}/queue/{qid}":                       Chat,
	"POST /sessions/{id}/attachments":                         Chat,
	"DELETE /sessions/{id}/attachments/{aid}":                 Chat,
	// Trims the conversation (with optional focus text); no tool runs.
	"POST /sessions/{id}/compact": Chat,

	// Control: managing a session rather than talking to it.
	"POST /sessions/{id}/rename":               Control,
	"POST /sessions/{id}/aside":                Control,
	"POST /sessions/{id}/model":                Control,
	"POST /sessions/{id}/rail-rank":            Control,
	"POST /sessions/{id}/resume":               Control,
	"POST /sessions/{id}/terminate":            Control,
	"POST /sessions/{id}/held-updates/deliver": Control,
	"POST /providers/{name}/models/refresh":    Control,
	// The agent's terminal (chat/terminal transport): raw keystrokes into
	// its TUI, outside the transcript. A TUI can run shell commands (e.g.
	// a "!" prefix) or switch its own permission mode, so this is control,
	// and the shell for C2 (ShellRoutes).
	"POST /sessions/{id}/input":     Control,
	"POST /sessions/{id}/resize":    Control,
	"POST /sessions/{id}/transport": Control,

	// Starting a session on this host (D6); its trust is subject to C3.
	"POST /sessions":                                          Control,
	"GET /sessions/defaults":                                  Control,
	"POST /sessions/validate":                                 Control,
	"GET /preflight":                                          Control,
	"POST /attachment-drafts/{id}/attachments":                Control,
	"GET /attachment-drafts/{id}/attachments/{aid}":           Control,
	"GET /attachment-drafts/{id}/attachments/{aid}/thumbnail": Control,
	"DELETE /attachment-drafts/{id}/attachments/{aid}":        Control,

	// Session lifecycle and trust: archive, raise or lower trust (C3),
	// restart the agent.
	"POST /sessions/{id}/trust":  Control,
	"POST /sessions/{id}/bounce": Control,
	"DELETE /sessions/{id}":      Control,

	// Git writes on the host: commit, draft, undo, land, push, move a file
	// into a repo, delete scratch files. The UI confirms the destructive ones.
	"POST /sessions/{id}/changeset/delete-scratch": Control,
	"POST /sessions/{id}/move":                     Control,
	"POST /sessions/{id}/push":                     Control,
	"POST /sessions/{id}/commit":                   Control,
	"POST /sessions/{id}/commit/undo":              Control,
	"POST /sessions/{id}/commit/draft":             Control,
	"POST /sessions/{id}/land":                     Control,
	// Writes the session's land commit message draft (repo state): a git
	// write like the commit draft.
	"POST /sessions/{id}/land/draft": Control,

	// Links and orchestration: cross-host links are a v1 non-goal, and the
	// orchestrate POSTs are the agents' own MCP surface. (The Router's own
	// /peers... isn't in its route table at all; it falls to "/" below,
	// Deny. Admin over the mesh is /devices, below.)
	"POST /links":                               Deny,
	"PUT /links/{id}":                           Deny,
	"DELETE /links/{id}":                        Deny,
	"POST /links/{id}/inject":                   Deny,
	"POST /links/{id}/orchestrate/message":      Deny,
	"POST /link-digests/discard":                Deny,
	"POST /link-digests/restore":                Deny,
	"POST /sessions/{id}/orchestrate/attention": Deny,
	"POST /sessions/{id}/orchestrate/mailbox":   Deny,

	// Every live session's pending permission requests at once: the
	// window-wide read this host's own UI seeds its approvals from. A
	// device reads one session's (GET /sessions/{id}/permission-requests).
	"GET /permission-requests": Deny,

	// Library and pipeline writes.
	"POST /pipelines":                         Deny,
	"POST /pipelines/{id}/cancel":             Deny,
	"POST /agents":                            Deny,
	"PUT /agents/{id}":                        Deny,
	"DELETE /agents/{id}":                     Deny,
	"POST /agents/generate":                   Deny,
	"POST /agents/describe":                   Deny,
	"POST /agents/suggest-capabilities":       Deny,
	"POST /agents/refine":                     Deny,
	"POST /capabilities":                      Deny,
	"PUT /capabilities/{id}":                  Deny,
	"DELETE /capabilities/{id}":               Deny,
	"POST /capabilities/{id}/diagnose":        Deny,
	"POST /workspaces":                        Deny,
	"PUT /workspaces/{id}":                    Deny,
	"DELETE /workspaces/{id}":                 Deny,
	"POST /templates":                         Deny,
	"PUT /templates/{id}":                     Deny,
	"DELETE /templates/{id}":                  Deny,
	"POST /templates/validate":                Deny,
	"POST /templates/validate-all":            Deny,
	"POST /tasks":                             Deny,
	"POST /tasks/import":                      Deny,
	"POST /tasks/suggest-capabilities":        Deny,
	"PUT /tasks/{id}":                         Deny,
	"POST /tasks/{id}/confirm":                Deny,
	"POST /tasks/{id}/discard":                Deny,
	"POST /tasks/{id}/retry":                  Deny,
	"POST /tasks/{id}/notes":                  Deny,
	"POST /tasks/{id}/notes/{noteId}/confirm": Deny,
	"POST /tasks/{id}/notes/{noteId}/discard": Deny,
	"POST /jobs/{id}/cancel":                  Deny,
	"GET /retros":                             Deny,
	"GET /retros/{id}":                        Deny,
	"POST /retros":                            Deny,
	"POST /retros/{id}/follow-up-session":     Deny,
	"POST /retros/{id}/dpas/{dpaId}/accept":   Deny,
	"POST /retros/{id}/dpas/{dpaId}/reject":   Deny,
	"GET /proposals":                          Deny,
	"POST /proposals/{id}/approve":            Deny,
	"POST /proposals/{id}/dismiss":            Deny,

	// The host's own settings, secrets and accounts: never over the mesh.
	"GET /config":                           Deny,
	"PUT /config":                           Deny,
	"GET /fast-model/status":                Deny,
	"GET /profiles":                         Deny,
	"POST /profiles":                        Deny,
	"PUT /profiles/{id}":                    Deny,
	"DELETE /profiles/{id}":                 Deny,
	"POST /profiles/{id}/activate":          Deny,
	"GET /credentials":                      Deny,
	"POST /credentials":                     Deny,
	"DELETE /credentials/{name}":            Deny,
	"GET /credentials/env-candidates":       Deny,
	"POST /credentials/import":              Deny,
	"GET /credentials/vault-sources":        Deny,
	"GET /credentials/vault-refs":           Deny,
	"POST /credentials/vault-refs":          Deny,
	"DELETE /credentials/vault-refs/{name}": Deny,
	"POST /accounts/signed-in":              Deny,
	"GET /imports/conversations":            Deny,
	"POST /imports/conversations":           Deny,
	"POST /update/check":                    Deny,
	"GET /registry/index":                   Deny,
	"POST /registry/install":                Deny,
	"POST /registry/update":                 Deny,
	"POST /registry/publish":                Deny,
	"GET /metrics/cost":                     Deny,
	"GET /metrics/agents":                   Deny,
	"GET /metrics/tasks":                    Deny,
	"GET /metrics/capabilities":             Deny,
	"GET /metrics/pipelines":                Deny,

	// GET /repos/state?path= reads the branch facts of whatever checkout
	// holds an arbitrary host path, not one of a session's repos: over the
	// mesh it would let a peer probe this machine's filesystem for repos.
	// The UI reads a session's repos through /sessions/{id}/repos instead.
	"GET /repos/state": Deny,
	// The front door's child-socket listing (docs/LOCAL-FRONT-DOOR-DESIGN.md
	// D9): host-local plumbing a peer has no business seeing.
	"GET /children": Deny,

	// Paired devices (docs/MESH-AUTH-DESIGN.md D5, D8). Admin: what a
	// device administering this host may do from elsewhere, each a narrow
	// route of its own (the Router's /peers CRUD stays Deny): read the
	// list, introduce a device (at most its own level, never admin),
	// change a device's level (never to admin; lowering one, admin
	// included, is fine: Q2), remove a device. The Router checks the caps
	// and the Data Service's peer registry checks them again.
	"GET /devices":            Admin,
	"POST /devices/introduce": Admin,
	"PUT /devices/{fp}/grant": Admin,
	"DELETE /devices/{fp}":    Admin,
	// The rest is this host's own: its pairing codes and the requests
	// they make (a joiner uses the Mesh Service's /mesh/pair instead),
	// joining another host, introducing to another host, removing
	// everywhere, the activity log, this device.
	"GET /devices/self":                        Deny,
	"PUT /devices/settings":                    Deny,
	"GET /devices/activity":                    Deny,
	"POST /devices/pairing-code":               Deny,
	"DELETE /devices/pairing-code/{id}":        Deny,
	"POST /devices/pair":                       Deny,
	"GET /devices/pair/{id}":                   Deny,
	"GET /devices/pair-requests":               Deny,
	"POST /devices/pair-requests/{id}/approve": Deny,
	"POST /devices/pair-requests/{id}/decline": Deny,
	"POST /devices/{fp}/introduce":             Deny,
	"POST /devices/{fp}/remove-everywhere":     Deny,

	// The static UI: a peer views through its own UI, never this one.
	"/": Deny,
}

// Classifier resolves a request to the Router route it would hit, using the
// same ServeMux matching the Router uses, so "which route is this" can't
// disagree between the two.
type Classifier struct {
	mux *http.ServeMux
}

// NewClassifier builds a Classifier over Routes.
func NewClassifier() *Classifier {
	mux := http.NewServeMux()
	for pattern := range Routes {
		mux.Handle(pattern, http.NotFoundHandler())
	}
	return &Classifier{mux: mux}
}

// Classify returns the matched Router pattern and its class. A request that
// matches no pattern (an unknown path, a wrong method, a path ServeMux would
// redirect to its clean form) is Deny with an empty pattern.
func (c *Classifier) Classify(r *http.Request) (pattern string, class Class) {
	_, pattern = c.mux.Handler(r)
	class, ok := Routes[pattern]
	if !ok {
		return pattern, Deny
	}
	return pattern, class
}

// rank orders the grant levels; GrantNone is 0 and reaches nothing.
func rank(l contracts.GrantLevel) int {
	return slices.Index(contracts.GrantLevels, l) + 1
}

// Allows reports whether a device at level may use a route of class.
func Allows(level contracts.GrantLevel, class Class) bool {
	if class == Deny || !level.Valid() {
		return false
	}
	need := contracts.GrantLevel(class)
	return need.Valid() && rank(level) >= rank(need)
}

// Min is the lower of two levels.
func Min(a, b contracts.GrantLevel) contracts.GrantLevel {
	if rank(a) <= rank(b) {
		return a
	}
	return b
}

// CapabilityLevel is the highest level a tailnet capability's access list
// names (view, chat, control or admin), GrantNone for none. It is a cap on
// the paired device's grant, never a grant of its own: the Mesh Service
// takes the lower of the two. Names it doesn't know (the 2026-11-12
// "create", a future "shell") count for nothing.
func CapabilityLevel(access []string) contracts.GrantLevel {
	best := contracts.GrantNone
	for _, a := range access {
		if l := contracts.GrantLevel(a); l.Valid() && rank(l) > rank(best) {
			best = l
		}
	}
	return best
}

// Levels is level and every level below it, least first: what /mesh/hello
// reports as "access", so a viewer that only knows "view" and "control"
// still reads a chat or admin device correctly.
func Levels(level contracts.GrantLevel) []string {
	out := []string{}
	for _, l := range contracts.GrantLevels {
		if rank(l) <= rank(level) {
			out = append(out, string(l))
		}
	}
	return out
}

// --- Ceilings (D4) ---------------------------------------------------------

// FullTrustChatRoutes are subject to C1 for a chat-level device: free text
// to a full-trust session is allowed, refused, or (later) held. A message
// makes the agent act; compact's optional focus text is free text the
// agent reads too. Interrupting or answering a prompt carries none.
var FullTrustChatRoutes = map[string]bool{
	"POST /sessions/{id}/messages": true,
	"POST /sessions/{id}/compact":  true,
}

// ShellRoutes are refused to a device with C2 (no shell), and to one with a
// C3 trust ceiling below full, which they would bypass. P2's host shells
// aren't on this branch; today the shell-like surface is the agent's
// terminal transport, raw keystrokes outside the transcript.
var ShellRoutes = map[string]bool{
	"POST /sessions/{id}/transport": true,
	"POST /sessions/{id}/input":     true,
	"POST /sessions/{id}/resize":    true,
}

// TrustSetting is how a route sets a session's trust, for C3.
type TrustSetting int

const (
	// TrustOnCreate: POST /sessions. The body's "trust"; absent, the host's
	// default for the session's provider (config, or full for a
	// full-trust-only provider).
	TrustOnCreate TrustSetting = iota + 1
	// TrustOnChange: POST /sessions/{id}/trust, the body's "trust".
	TrustOnChange
)

// TrustRoutes are every forwarded route that can start a session at, or
// move one to, a trust level: C3 checks their bodies. Routes that also
// set trust but are Deny over the mesh (PUT /config's default_trust,
// profile activation, POST /retros/{id}/follow-up-session, POST
// /pipelines, POST /imports/conversations, the orchestrate surface) never
// reach C3. Restarts (resume, bounce) keep the trust on the record, and
// answering a permission prompt is allow or deny, never a mode change.
// TestCeilingRoutes pins the list.
var TrustRoutes = map[string]TrustSetting{
	"POST /sessions":            TrustOnCreate,
	"POST /sessions/{id}/trust": TrustOnChange,
}

// trustRank orders session trust, least permissive first; 0 for unknown.
func trustRank(t contracts.SessionTrust) int {
	i := slices.Index(contracts.SessionTrusts, t)
	if i < 0 {
		return 0
	}
	return len(contracts.SessionTrusts) - i
}

// AboveCeiling reports whether trust t is above ceiling max. No ceiling
// (empty) allows everything; an unknown t is above any ceiling.
func AboveCeiling(t, max contracts.SessionTrust) bool {
	if max == "" {
		return false
	}
	return trustRank(t) == 0 || trustRank(t) > trustRank(max)
}
