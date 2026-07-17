package api_test

import (
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
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
