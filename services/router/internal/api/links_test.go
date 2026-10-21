package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

func mkSessionRouter(t *testing.T, baseURL, name string) contracts.Session {
	t.Helper()
	resp := postJSON(t, baseURL+"/sessions", map[string]string{"name": name, "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session %q: status %d", name, resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	return created.Session
}

func setLinkedSessionsFlag(t *testing.T, baseURL string, on bool) {
	t.Helper()
	getResp, err := http.Get(baseURL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeJSON[contracts.Config](t, getResp)
	cfg.FeatureFlags.LinkedSessions = on
	putResp := putJSON(t, baseURL+"/config", cfg)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("set linked-sessions flag: status %d", putResp.StatusCode)
	}
}

func TestLinkRoutes404WhenFlagOff(t *testing.T) {
	srv := newTestServer(t)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	cases := []struct {
		method, path string
		body         any
	}{
		{"POST", "/links", contracts.LinkRequest{GrantingSessionID: leader.ID, SubjectSessionID: subject.ID, Preset: contracts.PresetMonitor}},
		{"GET", "/links/lnk_nope", nil},
		{"PUT", "/links/lnk_nope", contracts.LinkRequest{Grants: []contracts.Grant{contracts.GrantRead}}},
		{"DELETE", "/links/lnk_nope", nil},
		{"GET", "/sessions/" + subject.ID + "/links", nil},
	}
	for _, c := range cases {
		var resp *http.Response
		switch c.method {
		case "POST":
			resp = postJSON(t, srv.URL+c.path, c.body)
		case "PUT":
			resp = putJSON(t, srv.URL+c.path, c.body)
		case "GET":
			r, err := http.Get(srv.URL + c.path)
			if err != nil {
				t.Fatal(err)
			}
			resp = r
		case "DELETE":
			req, err := http.NewRequest("DELETE", srv.URL+c.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			r, err := http.DefaultClient.Do(req) //nolint:noctx // test helper, url is local httptest server
			if err != nil {
				t.Fatal(err)
			}
			resp = r
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s with flag off: status %d, want 404", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestLinkRoutesProxiedWhenFlagOn(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetMonitor,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)
	if len(link.Grants) != 1 || link.Grants[0] != contracts.GrantRead {
		t.Fatalf("grants = %v, want [read]", link.Grants)
	}

	getResp, err := http.Get(srv.URL + "/links/" + link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get: status %d", getResp.StatusCode)
	}

	updateResp := putJSON(t, srv.URL+"/links/"+link.ID, contracts.LinkRequest{
		Grants: []contracts.Grant{contracts.GrantRead, contracts.GrantInject},
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d", updateResp.StatusCode)
	}
	updated := decodeJSON[contracts.Link](t, updateResp)
	if len(updated.Grants) != 2 {
		t.Fatalf("updated grants = %v, want 2 entries", updated.Grants)
	}

	listResp, err := http.Get(srv.URL + "/sessions/" + subject.ID + "/links")
	if err != nil {
		t.Fatal(err)
	}
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d", listResp.StatusCode)
	}
	list := decodeJSON[[]contracts.Link](t, listResp)
	if len(list) != 1 {
		t.Fatalf("list = %v, want 1 link", list)
	}

	delReq, err := http.NewRequest("DELETE", srv.URL+"/links/"+link.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	delResp, err := http.DefaultClient.Do(delReq) //nolint:noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", delResp.StatusCode)
	}
}

// waitForTranscriptLenRouter polls GET /sessions/{id}/transcript until it
// reaches n messages — used to confirm a real session's provider-native ref
// has been announced (the fake provider announces once the first turn
// begins, mirroring RefAnnounceOnConnect's default timing) before a test
// depends on that ref being resumable.
func waitForTranscriptLenRouter(t *testing.T, baseURL, sessionID string, n int) []contracts.TranscriptMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(baseURL + "/sessions/" + sessionID + "/transcript")
		if err != nil {
			t.Fatal(err)
		}
		msgs := decodeJSON[[]contracts.TranscriptMessage](t, resp)
		if len(msgs) >= n {
			return msgs
		}
		if time.Now().After(deadline) {
			t.Fatalf("transcript len = %d, want >= %d", len(msgs), n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitOrchestrateStatusRouter polls GET /links/{id} until OrchestrateStatus
// matches want or the deadline expires — the router's notify is
// fire-and-forget (services/router/internal/api/links.go
// notifyOrchestrateRefresh), so its effect on the link record is
// necessarily asynchronous from this test's point of view.
func waitOrchestrateStatusRouter(t *testing.T, baseURL, linkID string, want contracts.OrchestrateStatus) contracts.Link {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(baseURL + "/links/" + linkID)
		if err != nil {
			t.Fatal(err)
		}
		link := decodeJSON[contracts.Link](t, resp)
		if link.OrchestrateStatus == want {
			return link
		}
		if time.Now().After(deadline) {
			t.Fatalf("link %s OrchestrateStatus = %q, want %q", linkID, link.OrchestrateStatus, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestLinkCreateNotifiesRunningLeaderMaterializesOrchestrate is the
// acceptance test's live-materialization half at the Router layer: creating
// a Leader-preset link onto an already-running session (a real Session
// Service child, not testchild) must reach the child's own
// POST /orchestrate/refresh (notifyOrchestrateRefresh) and flip the link's
// OrchestrateStatus from pending_restart to active — without a restart.
func TestLinkCreateNotifiesRunningLeaderMaterializesOrchestrate(t *testing.T) {
	srv, _ := newTestServerWithRealSession(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	// Drive one turn so the leader's provider-native ref is announced
	// (swapTransport's resumable-ref precondition, see
	// TestRefreshOrchestrateMaterializesLive) before the link exists.
	mresp := postJSON(t, srv.URL+"/sessions/"+leader.ID+"/messages", map[string]string{"text": "echo hi"})
	if mresp.StatusCode != http.StatusAccepted {
		t.Fatalf("send message status = %d, want 202", mresp.StatusCode)
	}
	waitForTranscriptLenRouter(t, srv.URL, leader.ID, 2)

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)
	if link.OrchestrateStatus != contracts.OrchestratePending {
		t.Fatalf("OrchestrateStatus on create = %q, want %q", link.OrchestrateStatus, contracts.OrchestratePending)
	}

	waitOrchestrateStatusRouter(t, srv.URL, link.ID, contracts.OrchestrateActive)
}

// TestLinkUpdateNotifiesOnGrantAddition proves handleUpdateLink's notify
// condition fires for a grant newly added by an edit (not just at create
// time): a Monitor-preset link upgraded to Leader must also materialize
// live, exercising the "old.HasGrant || link.HasGrant" branch's newly-added
// half.
func TestLinkUpdateNotifiesOnGrantAddition(t *testing.T) {
	srv, _ := newTestServerWithRealSession(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	mresp := postJSON(t, srv.URL+"/sessions/"+leader.ID+"/messages", map[string]string{"text": "echo hi"})
	if mresp.StatusCode != http.StatusAccepted {
		t.Fatalf("send message status = %d, want 202", mresp.StatusCode)
	}
	waitForTranscriptLenRouter(t, srv.URL, leader.ID, 2)

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetMonitor,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)
	if link.OrchestrateStatus != "" {
		t.Fatalf("OrchestrateStatus on monitor create = %q, want empty", link.OrchestrateStatus)
	}

	updateResp := putJSON(t, srv.URL+"/links/"+link.ID, contracts.LinkRequest{
		Grants: []contracts.Grant{contracts.GrantRead, contracts.GrantInject, contracts.GrantOrchestrate},
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d", updateResp.StatusCode)
	}
	updated := decodeJSON[contracts.Link](t, updateResp)
	if updated.OrchestrateStatus != contracts.OrchestratePending {
		t.Fatalf("OrchestrateStatus right after update = %q, want %q", updated.OrchestrateStatus, contracts.OrchestratePending)
	}

	waitOrchestrateStatusRouter(t, srv.URL, link.ID, contracts.OrchestrateActive)
}

// TestLinkCreateMergeUpgradeMaterializesOrchestrate: POST /links with the
// Leader preset for a pair that already has a Monitor link upgrades that
// link in place (same id) instead of adding a parallel one, and the upgrade
// still reaches the running leader's /orchestrate/refresh and goes active.
func TestLinkCreateMergeUpgradeMaterializesOrchestrate(t *testing.T) {
	srv, _ := newTestServerWithRealSession(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	mresp := postJSON(t, srv.URL+"/sessions/"+leader.ID+"/messages", map[string]string{"text": "echo hi"})
	if mresp.StatusCode != http.StatusAccepted {
		t.Fatalf("send message status = %d, want 202", mresp.StatusCode)
	}
	waitForTranscriptLenRouter(t, srv.URL, leader.ID, 2)

	monitorResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID, Preset: contracts.PresetMonitor,
	})
	if monitorResp.StatusCode != http.StatusCreated {
		t.Fatalf("monitor create: status %d", monitorResp.StatusCode)
	}
	monitor := decodeJSON[contracts.Link](t, monitorResp)

	leaderResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID, Preset: contracts.PresetLeader,
	})
	if leaderResp.StatusCode != http.StatusCreated {
		t.Fatalf("leader create over monitor: status %d", leaderResp.StatusCode)
	}
	upgraded := decodeJSON[contracts.Link](t, leaderResp)
	if upgraded.ID != monitor.ID {
		t.Fatalf("upgrade returned link %s, want the existing %s", upgraded.ID, monitor.ID)
	}
	if !upgraded.HasGrant(contracts.GrantOrchestrate) {
		t.Fatalf("upgraded grants = %v, want orchestrate", upgraded.Grants)
	}
	waitOrchestrateStatusRouter(t, srv.URL, monitor.ID, contracts.OrchestrateActive)

	resp, err := http.Get(srv.URL + "/sessions/" + leader.ID + "/links")
	if err != nil {
		t.Fatal(err)
	}
	if links := decodeJSON[[]contracts.Link](t, resp); len(links) != 1 {
		t.Fatalf("leader has %d links after the upgrade, want 1: %+v", len(links), links)
	}
}

// TestLinkUpdateMonitorOnlyEditNeverNotifies proves a monitor-only edit
// (orchestrate absent both before and after) never triggers a refresh
// attempt — the link's OrchestrateStatus must stay empty throughout, not
// just immediately after the update.
func TestLinkUpdateMonitorOnlyEditNeverNotifies(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetMonitor,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)

	updateResp := putJSON(t, srv.URL+"/links/"+link.ID, contracts.LinkRequest{
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceManualOnly,
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d", updateResp.StatusCode)
	}
	updated := decodeJSON[contracts.Link](t, updateResp)
	if updated.OrchestrateStatus != "" {
		t.Fatalf("OrchestrateStatus after monitor-only edit = %q, want empty", updated.OrchestrateStatus)
	}
}

// TestLinkDeleteThenRecreateOrchestrateStillMaterializes is the teardown
// regression check: deleting a materialized Leader link must leave the
// granting session's runner able to cleanly materialize a fresh one
// afterward — proof the teardown cycle (RefreshOrchestrate's swapTransport
// back to no MCP servers, capability.go) doesn't leave the process wedged.
func TestLinkDeleteThenRecreateOrchestrateStillMaterializes(t *testing.T) {
	srv, _ := newTestServerWithRealSession(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subjectA := mkSessionRouter(t, srv.URL, "subject-a")
	subjectB := mkSessionRouter(t, srv.URL, "subject-b")

	mresp := postJSON(t, srv.URL+"/sessions/"+leader.ID+"/messages", map[string]string{"text": "echo hi"})
	if mresp.StatusCode != http.StatusAccepted {
		t.Fatalf("send message status = %d, want 202", mresp.StatusCode)
	}
	waitForTranscriptLenRouter(t, srv.URL, leader.ID, 2)

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subjectA.ID,
		Preset:            contracts.PresetLeader,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)
	waitOrchestrateStatusRouter(t, srv.URL, link.ID, contracts.OrchestrateActive)

	delReq, err := http.NewRequest("DELETE", srv.URL+"/links/"+link.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	delResp, err := http.DefaultClient.Do(delReq) //nolint:noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", delResp.StatusCode)
	}

	createResp2 := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subjectB.ID,
		Preset:            contracts.PresetLeader,
	})
	if createResp2.StatusCode != http.StatusCreated {
		t.Fatalf("recreate: status %d", createResp2.StatusCode)
	}
	link2 := decodeJSON[contracts.Link](t, createResp2)
	waitOrchestrateStatusRouter(t, srv.URL, link2.ID, contracts.OrchestrateActive)
}

// TestLinkInjectRefusedWithoutGrant proves a link that doesn't carry the
// inject grant (a Monitor preset, {read} only) can't be used to inject.
func TestLinkInjectRefusedWithoutGrant(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetMonitor,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)

	resp := postJSON(t, srv.URL+"/links/"+link.ID+"/inject", map[string]string{"text": "hello"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("inject without grant: status %d, want 403", resp.StatusCode)
	}
}

// TestLinkInject404WhenSubjectNotRunning proves inject 404s when the
// subject session has no running child — even though the link itself and
// its grant are valid.
func TestLinkInject404WhenSubjectNotRunning(t *testing.T) {
	srv, base := newTestServerWithData(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")

	// Created directly against the Data Service, bypassing the Router's
	// POST /sessions (which spawns a child) — this session exists but has
	// no running process.
	bootstrap := dataclient.New(base, "test-bootstrap")
	subject := contracts.Session{Name: "subject", Provider: "fake"}
	if err := bootstrap.CreateSession(context.Background(), &subject); err != nil {
		t.Fatalf("create subject session: %v", err)
	}

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)

	resp := postJSON(t, srv.URL+"/links/"+link.ID+"/inject", map[string]string{"text": "hello"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("inject with subject not running: status %d, want 404", resp.StatusCode)
	}
}

// TestLinkInjectDeliversWithAttribution is the end-to-end happy path: a
// Leader-preset link injects a message into a real running subject child,
// and the delivered message lands in the subject's transcript labelled with
// the granting session's name and relation, read back from the wrapper
// (docs/LINKED-SESSIONS-DESIGN.md decisions 1-2, "Message wrapper").
func TestLinkInjectDeliversWithAttribution(t *testing.T) {
	srv, _ := newTestServerWithRealSession(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader-1")
	subject := mkSessionRouter(t, srv.URL, "subject-1")

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)

	injectResp := postJSON(t, srv.URL+"/links/"+link.ID+"/inject", map[string]string{"text": "hello"})
	if injectResp.StatusCode != http.StatusAccepted {
		t.Fatalf("inject: status %d, want 202", injectResp.StatusCode)
	}

	var msgs []contracts.TranscriptMessage
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tresp, err := http.Get(srv.URL + "/sessions/" + subject.ID + "/transcript")
		if err != nil {
			t.Fatal(err)
		}
		msgs = decodeJSON[[]contracts.TranscriptMessage](t, tresp)
		if len(msgs) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(msgs) == 0 {
		t.Fatal("timed out waiting for injected message in subject transcript")
	}
	got := msgs[0]
	if got.Role != "user" || got.Text != "hello" {
		t.Fatalf("msgs[0] = %+v, want {role: user, text: hello}", got)
	}
	if got.Attribution == nil {
		t.Fatalf("msgs[0] = %+v, attribution = nil, want set", got)
	}
	// Labelled from the wrapper the runner put on the wire: who and how
	// they relate, no link id.
	if want := (contracts.Attribution{SourceName: "leader-1", Kind: "message", Relation: "your leader"}); *got.Attribution != want {
		t.Fatalf("attribution = %+v, want %+v", got.Attribution, want)
	}
}
