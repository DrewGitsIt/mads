package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// TestOrchestrateRoutes404WhenFlagOff proves the three orchestrate routes are
// gated by the linked-sessions flag exactly like every other linked-sessions
// route (requireLinkedSessions).
func TestOrchestrateRoutes404WhenFlagOff(t *testing.T) {
	srv := newTestServer(t)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	resp, err := http.Get(srv.URL + "/sessions/" + leader.ID + "/orchestrate/subjects")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET subjects with flag off: status %d, want 404", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/sessions/" + leader.ID + "/orchestrate/summary?for=" + subject.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET summary with flag off: status %d, want 404", resp.StatusCode)
	}

	resp = postJSON(t, srv.URL+"/links/lnk_nope/orchestrate/message", map[string]string{"text": "hi"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST message with flag off: status %d, want 404", resp.StatusCode)
	}
}

// TestOrchestrateSubjectsListShape proves list_links only surfaces links
// where the leader is the granting session and the link carries the
// orchestrate grant — a Monitor-preset link (read only), and a link where
// the leader is the subject rather than the grantor, are both excluded.
func TestOrchestrateSubjectsListShape(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")
	other := mkSessionRouter(t, srv.URL, "other")
	third := mkSessionRouter(t, srv.URL, "third")

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create leader link: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)

	// Monitor preset (read only): should not appear in the orchestrate list.
	monResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  other.ID,
		Preset:            contracts.PresetMonitor,
	})
	if monResp.StatusCode != http.StatusCreated {
		t.Fatalf("create monitor link: status %d", monResp.StatusCode)
	}

	// Leader as subject rather than grantor: should not appear either.
	reverseResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: third.ID,
		SubjectSessionID:  leader.ID,
		Preset:            contracts.PresetLeader,
	})
	if reverseResp.StatusCode != http.StatusCreated {
		t.Fatalf("create reverse link: status %d", reverseResp.StatusCode)
	}

	listResp, err := http.Get(srv.URL + "/sessions/" + leader.ID + "/orchestrate/subjects")
	if err != nil {
		t.Fatal(err)
	}
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d", listResp.StatusCode)
	}
	type orchestrateSubject struct {
		LinkID           string   `json:"link_id"`
		SubjectSessionID string   `json:"subject_session_id"`
		SubjectName      string   `json:"subject_name"`
		Grants           []string `json:"grants"`
		Activity         string   `json:"activity"`
	}
	list := decodeJSON[[]orchestrateSubject](t, listResp)
	if len(list) != 1 {
		t.Fatalf("list = %+v, want 1 entry", list)
	}
	got := list[0]
	if got.LinkID != link.ID || got.SubjectSessionID != subject.ID || got.SubjectName != "subject" {
		t.Fatalf("list[0] = %+v, want link %q subject %q %q", got, link.ID, subject.ID, "subject")
	}
	if len(got.Grants) != 3 {
		t.Fatalf("grants = %v, want 3 entries", got.Grants)
	}
}

// TestOrchestrateMessageRefusedWithoutGrant proves a Monitor-preset link
// (read only) can't be used to send an orchestrate message.
func TestOrchestrateMessageRefusedWithoutGrant(t *testing.T) {
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

	resp := postJSON(t, srv.URL+"/links/"+link.ID+"/orchestrate/message", map[string]string{"text": "hello"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("send without grant: status %d, want 403", resp.StatusCode)
	}
}

// TestOrchestrateMessage404WhenSubjectNotRunning mirrors
// TestLinkInject404WhenSubjectNotRunning for the orchestrate send path.
func TestOrchestrateMessage404WhenSubjectNotRunning(t *testing.T) {
	srv, base := newTestServerWithData(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")

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

	resp := postJSON(t, srv.URL+"/links/"+link.ID+"/orchestrate/message", map[string]string{"text": "hello"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("send with subject not running: status %d, want 404", resp.StatusCode)
	}
}

// TestOrchestrateSummaryUsesSessionSummary proves get_summary prefers the
// subject's Summary field when one is set, without touching the transcript.
func TestOrchestrateSummaryUsesSessionSummary(t *testing.T) {
	srv, base := newTestServerWithData(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	direct := dataclient.New(base, "test-bootstrap")
	subject.Summary = "did the thing"
	if err := direct.UpdateSession(context.Background(), &subject); err != nil {
		t.Fatalf("set summary: %v", err)
	}

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetLeader,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}

	resp, err := http.Get(srv.URL + "/sessions/" + leader.ID + "/orchestrate/summary?for=" + subject.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary: status %d", resp.StatusCode)
	}
	got := decodeJSON[map[string]string](t, resp)
	if got["source"] != "summary" || got["text"] != "did the thing" {
		t.Fatalf("summary = %+v, want {source: summary, text: %q}", got, "did the thing")
	}
}

// TestOrchestrateSummaryNoOrchestrateLinkForbidden proves get_summary refuses
// to serve a subject the leader has no orchestrate link to.
func TestOrchestrateSummaryNoOrchestrateLinkForbidden(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	resp, err := http.Get(srv.URL + "/sessions/" + leader.ID + "/orchestrate/summary?for=" + subject.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("summary without link: status %d, want 403", resp.StatusCode)
	}
}

// TestOrchestrateMessageDeliversAttributed is the end-to-end happy path: a
// Leader-preset link sends an orchestrate message into a real running
// subject child, and it lands in the subject's transcript stamped with the
// link's id and the leader's name — the same attribution L2a's plain inject
// produces (docs/LINKED-SESSIONS-DESIGN.md decisions 1-3).
func TestOrchestrateMessageDeliversAttributed(t *testing.T) {
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

	sendResp := postJSON(t, srv.URL+"/links/"+link.ID+"/orchestrate/message", map[string]string{"text": "hello"})
	if sendResp.StatusCode != http.StatusAccepted {
		t.Fatalf("send: status %d, want 202", sendResp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, sendResp)
	if body["status"] != "delivered" {
		t.Fatalf("body = %+v, want status: delivered", body)
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
		t.Fatal("timed out waiting for delivered message in subject transcript")
	}
	got := msgs[0]
	if got.Role != "user" || got.Text != "hello" {
		t.Fatalf("msgs[0] = %+v, want {role: user, text: hello}", got)
	}
	if got.Attribution == nil || got.Attribution.LinkID != link.ID || got.Attribution.SourceName != "leader-1" {
		t.Fatalf("attribution = %+v, want {link_id: %q, source_name: leader-1}", got.Attribution, link.ID)
	}
}

// appendLinkMessageInjected simulates a digest (or any other L2a/L2b
// delivery) landing back on the leader's own session — the exact event
// Runner.recordInjectionAudit appends on every /inject, real or simulated.
func appendLinkMessageInjected(t *testing.T, c *dataclient.Client, leaderID, linkID, text string) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"link_id": linkID, "source_name": "subject-1", "text": text})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := contracts.Envelope{
		Type:      contracts.LinkMessageInjected,
		SessionID: leaderID,
		Actor:     "test",
		Payload:   raw,
	}
	if err := c.Append(context.Background(), &env); err != nil {
		t.Fatalf("append link.message_injected: %v", err)
	}
}

// TestOrchestrateMessageBoundedWaitReturnsReply proves send_message's
// optional wait_ms picks up a reply that lands as a link.message_injected
// event on the leader's own session before the deadline (docs/
// LINKED-SESSIONS-DESIGN.md decision 1: quick-question ergonomics on top of
// the existing digest machinery).
func TestOrchestrateMessageBoundedWaitReturnsReply(t *testing.T) {
	srv, base := newTestServerWithRealSession(t)
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

	direct := dataclient.New(base, "test-reply-simulator")
	go func() {
		time.Sleep(300 * time.Millisecond)
		appendLinkMessageInjected(t, direct, leader.ID, link.ID, "the answer is 4")
	}()

	sendResp := postJSON(t, srv.URL+"/links/"+link.ID+"/orchestrate/message", map[string]any{
		"text":    "what is 2+2?",
		"wait_ms": 3000,
	})
	if sendResp.StatusCode != http.StatusAccepted {
		t.Fatalf("send: status %d, want 202", sendResp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, sendResp)
	if body["reply"] != "the answer is 4" {
		t.Fatalf("body = %+v, want reply: %q", body, "the answer is 4")
	}
}

// TestOrchestrateMessageBoundedWaitTimesOut proves send_message degrades to
// the documented "still working" note when no reply lands before wait_ms
// elapses, rather than blocking indefinitely.
func TestOrchestrateMessageBoundedWaitTimesOut(t *testing.T) {
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

	sendResp := postJSON(t, srv.URL+"/links/"+link.ID+"/orchestrate/message", map[string]any{
		"text":    "what is 2+2?",
		"wait_ms": 200,
	})
	if sendResp.StatusCode != http.StatusAccepted {
		t.Fatalf("send: status %d, want 202", sendResp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, sendResp)
	if body["note"] != "still working; reply will arrive as a digest" {
		t.Fatalf("body = %+v, want the still-working note", body)
	}
	if _, ok := body["reply"]; ok {
		t.Fatalf("body = %+v, want no reply field", body)
	}
}
