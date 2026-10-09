package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// POST /sessions/{id}/aside records the user's own judgement about a session.
// It is the one piece of rail state no amount of watching the session could
// derive, which is why it is on the record and not in the browser.

func createAsideSession(t *testing.T, srvURL string) string {
	t.Helper()
	resp := postJSON(t, srvURL+"/sessions", map[string]string{"name": "set me aside", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)
	return created.Session.ID
}

func setAside(t *testing.T, srvURL, id string, aside bool) contracts.Session {
	t.Helper()
	resp := postJSON(t, srvURL+"/sessions/"+id+"/aside", map[string]bool{"aside": aside})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("aside status = %d, want 200", resp.StatusCode)
	}
	return decodeJSON[contracts.Session](t, resp)
}

func TestSetSessionAsideRoundTrips(t *testing.T) {
	srv, _ := newTrustTestServer(t, []string{testChildBin})
	id := createAsideSession(t, srv.URL)

	if got := setAside(t, srv.URL, id, true); !got.IsAside() {
		t.Fatalf("after aside=true, tags = %v, want the aside tag", got.Tags)
	}
	// Read it back through the normal GET: the point of putting this on the
	// record rather than in localStorage is that it survives the client.
	resp, err := http.Get(srv.URL + "/sessions/" + id) //nolint:gosec,noctx // test helper, url is the local httptest server
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", resp.StatusCode)
	}
	if reread := decodeJSON[contracts.Session](t, resp); !reread.IsAside() {
		t.Fatalf("re-read tags = %v, want the aside tag", reread.Tags)
	}

	if got := setAside(t, srv.URL, id, false); got.IsAside() {
		t.Fatalf("after aside=false, tags = %v, want no aside tag", got.Tags)
	}
}

func TestSetSessionAsideIsIdempotent(t *testing.T) {
	// The rail's control is a toggle, so a double click must not leave two
	// copies of the tag on the record — nor must un-setting one that was never
	// set be an error.
	srv, _ := newTrustTestServer(t, []string{testChildBin})
	id := createAsideSession(t, srv.URL)

	setAside(t, srv.URL, id, true)
	got := setAside(t, srv.URL, id, true)
	if len(got.Tags) != 1 || got.Tags[0] != contracts.SessionTagAside {
		t.Fatalf("tags after two aside=true calls = %v, want exactly one aside tag", got.Tags)
	}

	setAside(t, srv.URL, id, false)
	if got := setAside(t, srv.URL, id, false); len(got.Tags) != 0 {
		t.Fatalf("tags after two aside=false calls = %v, want none", got.Tags)
	}
}

func TestSetSessionAsideLeavesOtherTagsAlone(t *testing.T) {
	// Tags are shared ground: "retro" and "imported" live here too, and a
	// set-aside must not be able to strip a session of its provenance.
	sess := contracts.Session{Tags: []string{"retro", "imported"}}

	sess.SetAside(true)
	if !sess.IsAside() || len(sess.Tags) != 3 {
		t.Fatalf("tags after SetAside(true) = %v, want retro + imported + aside", sess.Tags)
	}
	sess.SetAside(false)
	if len(sess.Tags) != 2 || sess.Tags[0] != "retro" || sess.Tags[1] != "imported" {
		t.Fatalf("tags after SetAside(false) = %v, want retro + imported back unchanged", sess.Tags)
	}
}

func TestSetSessionAsideOnMissingSessionIs404(t *testing.T) {
	srv, _ := newTrustTestServer(t, []string{testChildBin})
	resp := postJSON(t, srv.URL+"/sessions/ses_nope/aside", map[string]bool{"aside": true})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("aside on a missing session = %d, want 404", resp.StatusCode)
	}
}

// The other half of "aside": the user sending the session a message is the
// user revising the judgement, so POST /messages retires the tag. An
// unprompted turn must not — that is what the fold is for.

func TestSendingAMessageClearsAside(t *testing.T) {
	srv, _ := newTrustTestServer(t, []string{testChildBin})
	id := createAsideSession(t, srv.URL)

	if got := setAside(t, srv.URL, id, true); !got.IsAside() {
		t.Fatalf("tags = %v, want the aside tag before the send", got.Tags)
	}

	resp := postJSON(t, srv.URL+"/sessions/"+id+"/messages", map[string]string{"text": "one more thing"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send status = %d, want 202", resp.StatusCode)
	}

	reread, err := http.Get(srv.URL + "/sessions/" + id) //nolint:gosec,noctx // test helper, url is the local httptest server
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeJSON[contracts.Session](t, reread); got.IsAside() {
		t.Fatalf("tags = %v after a send, want the aside tag gone", got.Tags)
	}
}

func TestSendingAMessageToASessionNeverSetAsideChangesNothing(t *testing.T) {
	// The send path clears unconditionally rather than reading first, so the
	// common case has to be a no-op all the way down: no write, and nothing
	// on the event log to make every rail refetch its list.
	srv, data := newTrustTestServer(t, []string{testChildBin})
	id := createAsideSession(t, srv.URL)

	before, err := data.Session(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, srv.URL+"/sessions/"+id+"/messages", map[string]string{"text": "hello"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send status = %d, want 202", resp.StatusCode)
	}

	after, err := data.Session(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) || after.Tags != nil {
		t.Fatalf("send touched the record: tags %v, updated_at %v (was %v)", after.Tags, after.UpdatedAt, before.UpdatedAt)
	}
}

// TestInjectedTurnLeavesAsideAlone is the distinction the whole rule rests
// on. A linked session's push, a digest or a task-terminal notice reaches the
// child through POST /inject with an attribution attached — not through the
// composer's POST /messages — so the system starting a turn on its own does
// not retire the user's judgement. If it did, the fold would be no quieter
// than the list it exists to shorten.
func TestInjectedTurnLeavesAsideAlone(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader-aside")
	subject := mkSessionRouter(t, srv.URL, "subject-aside")

	if got := setAside(t, srv.URL, subject.ID, true); !got.IsAside() {
		t.Fatalf("tags = %v, want the subject set aside before the inject", got.Tags)
	}

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create link: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)

	injectResp := postJSON(t, srv.URL+"/links/"+link.ID+"/inject", map[string]string{"text": "fyi"})
	if injectResp.StatusCode != http.StatusAccepted {
		t.Fatalf("inject: status %d, want 202", injectResp.StatusCode)
	}

	reread, err := http.Get(srv.URL + "/sessions/" + subject.ID) //nolint:gosec,noctx // test helper, url is the local httptest server
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeJSON[contracts.Session](t, reread); !got.IsAside() {
		t.Fatalf("tags = %v after an injected turn, want the session still set aside", got.Tags)
	}
}
