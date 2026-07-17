package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func mkSessionAPI(t *testing.T, srv *httptest.Server, name string) contracts.Session {
	t.Helper()
	var sess contracts.Session
	resp := doJSON(t, srv, "POST", "/sessions", contracts.Session{Name: name}, &sess)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session %q: status %d", name, resp.StatusCode)
	}
	return sess
}

func TestLinkPresetResolvesToGrants(t *testing.T) {
	srv, _ := newTestServer(t)
	leader := mkSessionAPI(t, srv, "leader")
	subject := mkSessionAPI(t, srv, "subject")

	var link contracts.Link
	resp := doJSON(t, srv, "POST", "/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	}, &link)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create via preset: status %d", resp.StatusCode)
	}
	want := []contracts.Grant{contracts.GrantRead, contracts.GrantInject, contracts.GrantOrchestrate}
	if len(link.Grants) != len(want) {
		t.Fatalf("grants = %v, want %v", link.Grants, want)
	}
	for i, g := range want {
		if link.Grants[i] != g {
			t.Errorf("grants[%d] = %s, want %s", i, link.Grants[i], g)
		}
	}
}

func TestLinkHandRaiserPresetRefusedFromUserRoute(t *testing.T) {
	srv, _ := newTestServer(t)
	leader := mkSessionAPI(t, srv, "leader")
	subject := mkSessionAPI(t, srv, "subject")

	var verr contracts.ValidationError
	resp := doJSON(t, srv, "POST", "/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetHandRaiser,
	}, &verr)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("hand_raiser via user route: status %d, want 422", resp.StatusCode)
	}
	if verr.Empty() {
		t.Fatal("expected a validation error for hand_raiser preset")
	}
}

func TestLinkExplicitGrantsRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)
	leader := mkSessionAPI(t, srv, "leader")
	subject := mkSessionAPI(t, srv, "subject")

	var link contracts.Link
	resp := doJSON(t, srv, "POST", "/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Grants:            []contracts.Grant{contracts.GrantRead},
	}, &link)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}

	var got contracts.Link
	resp = doJSON(t, srv, "GET", "/links/"+link.ID, nil, &got)
	if resp.StatusCode != http.StatusOK || got.ID != link.ID {
		t.Fatalf("get: status %d, got %+v", resp.StatusCode, got)
	}

	var updated contracts.Link
	resp = doJSON(t, srv, "PUT", "/links/"+link.ID, contracts.LinkRequest{
		Grants: []contracts.Grant{contracts.GrantRead, contracts.GrantInject},
	}, &updated)
	if resp.StatusCode != http.StatusOK || len(updated.Grants) != 2 {
		t.Fatalf("update: status %d, got %+v", resp.StatusCode, updated)
	}

	var list []contracts.Link
	resp = doJSON(t, srv, "GET", "/sessions/"+subject.ID+"/links", nil, &list)
	if resp.StatusCode != http.StatusOK || len(list) != 1 {
		t.Fatalf("list: status %d, got %+v", resp.StatusCode, list)
	}

	resp = doJSON(t, srv, "DELETE", "/links/"+link.ID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}
}

func TestHandRaiserRouteRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)
	sess := mkSessionAPI(t, srv, "solo")

	var link contracts.Link
	resp := doJSON(t, srv, "POST", "/sessions/"+sess.ID+"/hand-raiser", nil, &link)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create hand-raiser: status %d", resp.StatusCode)
	}
	if link.GrantingSessionID != sess.ID || link.SubjectSessionID != sess.ID || !link.Implicit {
		t.Fatalf("hand-raiser link = %+v, want self-referential and implicit", link)
	}

	var list []contracts.Link
	resp = doJSON(t, srv, "GET", "/sessions/"+sess.ID+"/links", nil, &list)
	if resp.StatusCode != http.StatusOK || len(list) != 1 {
		t.Fatalf("list: status %d, got %+v", resp.StatusCode, list)
	}

	resp = doJSON(t, srv, "DELETE", "/sessions/"+sess.ID+"/hand-raiser", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete hand-raiser: status %d", resp.StatusCode)
	}

	resp = doJSON(t, srv, "GET", "/sessions/"+sess.ID+"/links", nil, &list)
	if resp.StatusCode != http.StatusOK || len(list) != 0 {
		t.Fatalf("list after delete: status %d, got %+v", resp.StatusCode, list)
	}
}
