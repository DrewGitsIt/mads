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
// and the delivered message lands in the subject's transcript stamped with
// the link's id and the granting session's name (docs/LINKED-SESSIONS-DESIGN.md
// decisions 1-2).
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
	if got.Attribution.LinkID != link.ID || got.Attribution.SourceName != "leader-1" {
		t.Fatalf("attribution = %+v, want {link_id: %q, source_name: leader-1}", got.Attribution, link.ID)
	}
}
