package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
)

func TestGetSession(t *testing.T) {
	srv := newTestServer(t)
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, postJSON(t, srv.URL+"/sessions", map[string]string{"name": "get-me", "provider": "fake"}))

	resp, err := http.Get(srv.URL + "/sessions/" + created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[contracts.Session](t, resp)
	if got.ID != created.Session.ID || got.Name != created.Session.Name {
		t.Errorf("got = %+v, want %+v", got, created.Session)
	}
}

func TestGetSessionNotFound(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/sessions/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, resp)
	if body["error"] == "" {
		t.Errorf("expected non-empty error field, got %+v", body)
	}
}

// All-empty create (no provider/model/agent/isolation/trust) must resolve
// through Config defaults before spawning, and persist the resolved values —
// not just echo them back (spec §10).
func TestCreateSessionResolvesConfigDefaults(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "defaults-only"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
		Addr    string            `json:"addr"`
	}](t, resp)

	if created.Session.Provider != "fake" {
		t.Errorf("provider = %q, want %q", created.Session.Provider, "fake")
	}
	if created.Session.Model != "fake-fast" {
		t.Errorf("model = %q, want %q (fake's Default:true model)", created.Session.Model, "fake-fast")
	}
	if created.Session.Isolation != contracts.IsolationWorktree {
		t.Errorf("isolation = %q, want %q", created.Session.Isolation, contracts.IsolationWorktree)
	}
	if created.Session.Trust != contracts.TrustLimited {
		t.Errorf("trust = %q, want %q", created.Session.Trust, contracts.TrustLimited)
	}

	// Persisted, not just echoed — fetch it back independently.
	getResp, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	list := decodeJSON[[]contracts.Session](t, getResp)
	var found *contracts.Session
	for i := range list {
		if list[i].ID == created.Session.ID {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("created session %s not found in GET /sessions", created.Session.ID)
	}
	if found.Provider != "fake" || found.Model != "fake-fast" || found.Isolation != contracts.IsolationWorktree || found.Trust != contracts.TrustLimited {
		t.Errorf("persisted session = %+v, want resolved defaults", found)
	}
}

// Workspace defaults override Config; an explicit session field overrides
// the workspace default (spec §10's full chain).
func TestCreateSessionWorkspaceDefaultsOverrideConfig(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")

	wsResp := postJSON(t, srv.URL+"/workspaces", contracts.Workspace{
		Name: "ws-with-defaults",
		Defaults: contracts.WorkspaceDefaults{
			Model:     "fake-smart",
			Isolation: contracts.IsolationCurrentDir,
		},
	})
	if wsResp.StatusCode != http.StatusCreated {
		t.Fatalf("create workspace status = %d, want 201", wsResp.StatusCode)
	}
	ws := decodeJSON[contracts.Workspace](t, wsResp)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{
		"name": "ws-defaults", "workspace": ws.ID, "provider": "fake",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	if created.Session.Model != "fake-smart" {
		t.Errorf("model = %q, want workspace default %q", created.Session.Model, "fake-smart")
	}
	if created.Session.Isolation != contracts.IsolationCurrentDir {
		t.Errorf("isolation = %q, want workspace default %q", created.Session.Isolation, contracts.IsolationCurrentDir)
	}

	// Explicit session field wins over the workspace default.
	resp2 := postJSON(t, srv.URL+"/sessions", map[string]string{
		"name": "ws-defaults-explicit-model", "workspace": ws.ID, "provider": "fake", "model": "fake-fast",
	})
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp2.StatusCode)
	}
	created2 := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp2)
	if created2.Session.Model != "fake-fast" {
		t.Errorf("model = %q, want explicit %q to win over workspace default", created2.Session.Model, "fake-fast")
	}
}

// A session created with no explicit workspace pill selection, but a
// Config.DefaultWorkspace configured, must persist that resolved workspace
// onto the session itself — not just use it to derive the other defaults —
// otherwise the record ends up with workspace=null despite one having
// clearly been in play (the Launchpad-session workspace=null bug).
func TestCreateSessionWithNoExplicitWorkspacePersistsConfigDefault(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")

	wsResp := postJSON(t, srv.URL+"/workspaces", contracts.Workspace{Name: "default-ws"})
	if wsResp.StatusCode != http.StatusCreated {
		t.Fatalf("create workspace status = %d, want 201", wsResp.StatusCode)
	}
	ws := decodeJSON[contracts.Workspace](t, wsResp)

	getResp, err := http.Get(srv.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeJSON[contracts.Config](t, getResp)
	cfg.DefaultWorkspace = ws.ID
	if putResp := putJSON(t, srv.URL+"/config", cfg); putResp.StatusCode != http.StatusOK {
		t.Fatalf("set default workspace: status %d", putResp.StatusCode)
	}

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "no-explicit-ws", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	if created.Session.Workspace != ws.ID {
		t.Errorf("workspace = %q, want config default %q persisted onto the session", created.Session.Workspace, ws.ID)
	}
}

// Terminate on a session orphaned by a prior Router restart (Activity says
// active, nothing tracked — Supervisor state is in-memory only) must
// reconcile Activity to inactive, unblocking Resume end-to-end: Resume's
// existing 409 guards were the reason it looked broken on an orphan, not a
// bug in Resume itself.
func TestTerminateOrphanThenResumeEndToEnd(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "router")

	sess := contracts.Session{Name: "orphaned via api", Provider: "fake"}
	if err := data.CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Never spawned through the Router — simulates a session left active by
	// a prior process that no longer tracks this session's child.

	resp := postJSON(t, srv.URL+"/sessions/"+sess.ID+"/terminate", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("terminate orphan status = %d, want 204", resp.StatusCode)
	}

	got, err := http.Get(srv.URL + "/sessions/" + sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.StatusCode != http.StatusOK {
		t.Fatalf("get session after terminate status = %d, want 200", got.StatusCode)
	}
	gotSess := decodeJSON[contracts.Session](t, got)
	if gotSess.Activity != contracts.SessionInactive {
		t.Fatalf("session activity after terminating an orphan = %q, want inactive", gotSess.Activity)
	}

	resp = postJSON(t, srv.URL+"/sessions/"+sess.ID+"/resume", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume status = %d, want 200", resp.StatusCode)
	}
	resumed := decodeJSON[struct {
		Addr string `json:"addr"`
	}](t, resp)
	if resumed.Addr == "" {
		t.Fatal("resume after orphan reconciliation: no addr discovered from spawned child")
	}
}

// An unknown provider must be rejected before any session record or child
// process is created — a 4xx naming the provider, never a spawned-then-
// crashed 502.
func TestCreateSessionUnknownProviderNoOrphan(t *testing.T) {
	srv := newTestServer(t)

	before, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	beforeList := decodeJSON[[]contracts.Session](t, before)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "bad-provider", "provider": "nonexistent"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, resp)
	if got := body["error"]; !strings.Contains(got, `unknown provider "nonexistent"`) {
		t.Fatalf("error = %q, want it to name the provider", got)
	}

	after, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	afterList := decodeJSON[[]contracts.Session](t, after)
	if len(afterList) != len(beforeList) {
		t.Fatalf("session count changed %d -> %d: unknown-provider create left a record behind", len(beforeList), len(afterList))
	}
}

// Interactive session-start's agent="auto" (owner decision, 2026-07-30) must
// resolve to a concrete, registered agent — never persist the literal
// "auto" — by routing through the same agentselect.BuildRequest +
// autostaff.SelectOrGenerate machinery pipeline agent:auto stages use. Here
// a shipped system agent (autonomous_executor quadrant) survives the
// filters, so no generation is needed.
func TestCreateSessionAgentAutoResolvesToRegisteredAgent(t *testing.T) {
	srv := newTestServer(t)
	setDefaultProvider(t, srv.URL, "fake")

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{
		"name": "auto-agent", "provider": "fake", "agent": "auto",
		"opening_prompt": "fix the login bug",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	if created.Session.Agent == "" || created.Session.Agent == "auto" {
		t.Fatalf("agent = %q, want a concrete registered agent name", created.Session.Agent)
	}

	agentResp, err := http.Get(srv.URL + "/agents/" + created.Session.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if agentResp.StatusCode != http.StatusOK {
		t.Fatalf("resolved agent %q not found: status %d", created.Session.Agent, agentResp.StatusCode)
	}

	// Persisted, not just echoed.
	getResp, err := http.Get(srv.URL + "/sessions/" + created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted := decodeJSON[contracts.Session](t, getResp)
	if persisted.Agent != created.Session.Agent {
		t.Errorf("persisted agent = %q, want %q", persisted.Agent, created.Session.Agent)
	}
}

// No shipped agent survives the filters for this quadrant (mirrors
// engine_test.go's TestAutoGeneratesAgentWhenNoCandidate), so agent="auto"
// must fall through to on-demand generation — the exact fallback pipeline
// agent:auto stages already relied on, now shared via pkg/autostaff.
func TestCreateSessionAgentAutoGeneratesWhenNoCandidate(t *testing.T) {
	srv, _ := newTestServerWithScenario(t, fake.Scenario{Name: "session-auto-generate", Rules: []fake.Rule{{
		Match: "Generate an agent definition for this task",
		Reply: `{"name":"session-investigator","description":"Generated for this session",` +
			`"prompt":"You investigate things.","quadrant":"investigator","tags":[],` +
			`"capabilities":{"tools":[],"mcp_servers":[],"skills":[]},"permissions":{}}`,
	}}})
	setDefaultProvider(t, srv.URL, "fake")

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{
		"name": "auto-agent-generated", "provider": "fake", "agent": "auto",
		"opening_prompt": "investigate something",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	if created.Session.Agent != "session-investigator" {
		t.Errorf("agent = %q, want generated session-investigator", created.Session.Agent)
	}

	agentResp, err := http.Get(srv.URL + "/agents/session-investigator")
	if err != nil {
		t.Fatal(err)
	}
	if agentResp.StatusCode != http.StatusOK {
		t.Fatalf("generated agent was never registered: status %d", agentResp.StatusCode)
	}
}
