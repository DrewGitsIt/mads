package api_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// grantNotices returns the leader transcript's "mads"-attributed grant
// notices.
func grantNotices(t *testing.T, baseURL, sessionID string) []contracts.TranscriptMessage {
	t.Helper()
	resp, err := http.Get(baseURL + "/sessions/" + sessionID + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	var out []contracts.TranscriptMessage
	for _, m := range decodeJSON[[]contracts.TranscriptMessage](t, resp) {
		if m.Role == "user" && m.Attribution != nil && m.Attribution.SourceName == "mads" {
			out = append(out, m)
		}
	}
	return out
}

// Granting orchestrate to a session whose child is live injects one notice
// naming the subject, with shell commands that run the mads binary by
// absolute path, and the grant still goes active afterwards. Re-creating the
// same leader link (an idempotent merge) sends no second notice.
func TestOrchestrateGrantNoticeForLiveLeader(t *testing.T) {
	srv, _ := newTestServerWithRealSession(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "payments refactor")

	mresp := postJSON(t, srv.URL+"/sessions/"+leader.ID+"/messages", map[string]string{"text": "echo hi"})
	if mresp.StatusCode != http.StatusAccepted {
		t.Fatalf("send message status = %d, want 202", mresp.StatusCode)
	}
	waitForTranscriptLenRouter(t, srv.URL, leader.ID, 2)

	req := contracts.LinkRequest{GrantingSessionID: leader.ID, SubjectSessionID: subject.ID, Preset: contracts.PresetLeader}
	createResp := postJSON(t, srv.URL+"/links", req)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)

	var notices []contracts.TranscriptMessage
	deadline := time.Now().Add(5 * time.Second)
	for len(notices) == 0 && time.Now().Before(deadline) {
		notices = grantNotices(t, srv.URL, leader.ID)
		time.Sleep(50 * time.Millisecond)
	}
	if len(notices) != 1 {
		t.Fatalf("grant notices = %d, want 1", len(notices))
	}
	text := notices[0].Text
	for _, want := range []string{`"payments refactor"`, subject.ID, "orchestrate send", "-session " + leader.ID, "-subject " + subject.ID} {
		if !strings.Contains(text, want) {
			t.Errorf("notice missing %q:\n%s", want, text)
		}
	}
	cmd := strings.Fields(text[strings.Index(text, "  ")+2:])[0]
	if !filepath.IsAbs(cmd) {
		t.Errorf("notice runs %q, want an absolute path to mads", cmd)
	}
	if notices[0].Attribution.LinkID != link.ID || !notices[0].Attribution.DigestAttributed {
		t.Errorf("attribution = %+v, want link %s, digest-attributed", notices[0].Attribution, link.ID)
	}

	waitOrchestrateStatusRouter(t, srv.URL, link.ID, contracts.OrchestrateActive)

	if again := postJSON(t, srv.URL+"/links", req); again.StatusCode != http.StatusCreated {
		t.Fatalf("re-create: status %d", again.StatusCode)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(grantNotices(t, srv.URL, leader.ID)); n != 1 {
		t.Errorf("grant notices after an identical re-create = %d, want still 1", n)
	}
}

// With no live child for the leader there's no one to tell; the tools mount
// when the session next starts.
func TestOrchestrateGrantNoNoticeWithoutLiveChild(t *testing.T) {
	srv, base := newTestServerWithRealSession(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leaderID := createSessionDirect(t, base, contracts.Session{Name: "dormant leader", Provider: "fake"})
	subject := mkSessionRouter(t, srv.URL, "subject")

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leaderID, SubjectSessionID: subject.ID, Preset: contracts.PresetLeader,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}
	time.Sleep(300 * time.Millisecond)
	resp, err := http.Get(srv.URL + "/sessions/" + leaderID + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range decodeJSON[[]contracts.TranscriptMessage](t, resp) {
		if m.Attribution != nil && m.Attribution.SourceName == "mads" {
			t.Fatalf("dormant leader got a grant notice: %+v", m)
		}
	}
}
