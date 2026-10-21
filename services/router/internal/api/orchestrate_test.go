package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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

// TestOrchestrateSubjectsListShape proves list_linked_sessions surfaces links
// where the leader is the granting session and the link carries the orchestrate
// OR read grant (item 1, "pull tools for read grants") — a Monitor-preset link
// (read only) now appears alongside a Leader-preset link, while a link where
// the leader is the subject rather than the grantor is still excluded.
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

	// Monitor preset (read only): now appears too (item 1).
	monResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  other.ID,
		Preset:            contracts.PresetMonitor,
	})
	if monResp.StatusCode != http.StatusCreated {
		t.Fatalf("create monitor link: status %d", monResp.StatusCode)
	}
	monLink := decodeJSON[contracts.Link](t, monResp)

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
		Cadence          string   `json:"cadence"`
	}
	list := decodeJSON[[]orchestrateSubject](t, listResp)
	if len(list) != 2 {
		t.Fatalf("list = %+v, want 2 entries", list)
	}
	byLinkID := map[string]orchestrateSubject{}
	for _, s := range list {
		byLinkID[s.LinkID] = s
	}
	got, ok := byLinkID[link.ID]
	if !ok || got.SubjectSessionID != subject.ID || got.SubjectName != "subject" {
		t.Fatalf("list missing leader link, got %+v, want link %q subject %q %q", list, link.ID, subject.ID, "subject")
	}
	if len(got.Grants) != 3 {
		t.Fatalf("grants = %v, want 3 entries", got.Grants)
	}
	// The link tools' list_linked_sessions reports each link's cadence.
	if !contracts.LinkCadence(got.Cadence).Valid() {
		t.Fatalf("cadence = %q, want a registered cadence", got.Cadence)
	}
	gotMon, ok := byLinkID[monLink.ID]
	if !ok || gotMon.SubjectSessionID != other.ID || gotMon.SubjectName != "other" {
		t.Fatalf("list missing monitor link, got %+v, want link %q subject %q %q", list, monLink.ID, other.ID, "other")
	}
	if len(gotMon.Grants) != 1 || gotMon.Grants[0] != string(contracts.GrantRead) {
		t.Fatalf("monitor grants = %v, want [read]", gotMon.Grants)
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

// TestOrchestrateMessageWakesSleepingSubject mirrors
// TestLinkInjectWakesSleepingSubject for the orchestrate send path.
func TestOrchestrateMessageWakesSleepingSubject(t *testing.T) {
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
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send with subject asleep: status %d, want 202 (woken)", resp.StatusCode)
	}
}

// TestOrchestrateSummaryUsesSessionSummary proves get_session_summary prefers
// the subject's Summary field when one is set, without touching the transcript.
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
	got := decodeJSON[map[string]any](t, resp)
	if got["source"] != "summary" || got["text"] != "did the thing" {
		t.Fatalf("summary = %+v, want {source: summary, text: %q}", got, "did the thing")
	}
}

// TestOrchestrateSummaryNoOrchestrateLinkForbidden proves get_session_summary
// refuses to serve a subject the leader has no orchestrate link to.
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

// appendTurnEnded appends a session.turn_ended envelope carrying delta for
// sessionID — the same shape Runner.pushTurnEnded persists — and returns it
// with its assigned Seq, the turn number get_session_summary/get_session_turns
// anchor on.
func appendTurnEnded(t *testing.T, c *dataclient.Client, sessionID, delta string) contracts.Envelope {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"delta": delta})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := contracts.Envelope{
		Type:      contracts.TurnEnded,
		SessionID: sessionID,
		Actor:     "test",
		Payload:   raw,
	}
	if err := c.Append(context.Background(), &env); err != nil {
		t.Fatalf("append session.turn_ended: %v", err)
	}
	return env
}

// TestOrchestrateSummaryCompressesLiveTurns proves get_session_summary on a
// subject with session.turn_ended envelopes returns compressed prose citing the
// turn range, with the durable Summary carried alongside as a "prologue" rather
// than short-circuiting the whole response (the staleness this rework fixes: a
// durable summary alone can't reflect turns that landed after it was written).
func TestOrchestrateSummaryCompressesLiveTurns(t *testing.T) {
	srv, base := newTestServerWithData(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	direct := dataclient.New(base, "test-bootstrap")
	subject.Summary = "did the thing (stale)"
	if err := direct.UpdateSession(context.Background(), &subject); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	first := appendTurnEnded(t, direct, subject.ID, "user: fix the bug\nassistant: on it\n")
	last := appendTurnEnded(t, direct, subject.ID, "assistant: fixed and tested\n")

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
	got := decodeJSON[map[string]any](t, resp)
	if got["source"] != "compressed_recent" {
		t.Fatalf("summary = %+v, want source: compressed_recent", got)
	}
	if got["text"] == "" || got["text"] == nil {
		t.Fatalf("summary = %+v, want non-empty text", got)
	}
	if got["prologue"] != "did the thing (stale)" {
		t.Fatalf("summary = %+v, want prologue %q", got, "did the thing (stale)")
	}
	// The event-log seqs the digest used to cite are gone (2026-10-19): they
	// meant nothing to get_session_turns, which numbers transcript turns.
	if _, ok := got["from_turn"]; ok {
		t.Fatalf("summary = %+v, want no from_turn", got)
	}
	if _, ok := got["turns"].(float64); !ok {
		t.Fatalf("summary = %+v, want a numeric turns count", got)
	}
	for _, seq := range []int64{first.Seq, last.Seq} {
		if strings.Contains(got["text"].(string), fmt.Sprintf("turn %d", seq)) {
			t.Fatalf("summary text %q cites event-log seq %d", got["text"], seq)
		}
	}
}

// TestOrchestrateSummaryLongTranscriptHonorsBudget proves a long-transcript
// fixture (many turn_ended envelopes) still yields output the digest
// budget bounds, rather than growing unbounded with turn count.
func TestOrchestrateSummaryLongTranscriptHonorsBudget(t *testing.T) {
	srv, base := newTestServerWithData(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	direct := dataclient.New(base, "test-bootstrap")
	for range 100 {
		appendTurnEnded(t, direct, subject.ID,
			"user: keep going\nassistant: "+strings.Repeat("did more work ", 40)+"\n")
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
	got := decodeJSON[map[string]any](t, resp)
	if got["source"] != "compressed_recent" {
		t.Fatalf("summary = %+v, want source: compressed_recent", got)
	}
	// ~4 bytes/token heuristic (pkg/turndigest): a generous multiple of the
	// endpoint's budget proves the output didn't just grow with turn count.
	const budgetTokens = 1000
	if text := got["text"].(string); len(text) > budgetTokens*4*2 {
		t.Fatalf("text length %d, want <= %d (budget honored)", len(text), budgetTokens*4*2)
	}
}

// TestOrchestrateSummaryNoEnvelopesUnchanged proves a subject with no
// session.turn_ended envelopes still gets exactly today's behavior: the
// durable summary alone, or the raw transcript tail — no compression
// attempted at all.
func TestOrchestrateSummaryNoEnvelopesUnchanged(t *testing.T) {
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
	got := decodeJSON[map[string]any](t, resp)
	if got["source"] != "summary" || got["text"] != "did the thing" {
		t.Fatalf("summary = %+v, want {source: summary, text: %q}", got, "did the thing")
	}
}

// TestOrchestrateGetTurnsNoOrchestrateLinkForbidden proves get_session_turns is
// gated by the same orchestrate-link check as get_session_summary.
func TestOrchestrateGetTurnsNoOrchestrateLinkForbidden(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")
	subject := mkSessionRouter(t, srv.URL, "subject")

	resp, err := http.Get(srv.URL + "/sessions/" + leader.ID + "/orchestrate/turns?for=" + subject.ID + "&count=2")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("get_session_turns without link: status %d, want 403", resp.StatusCode)
	}
}

// TestOrchestrateSetAttentionFlipsCadence proves set_attention lets a
// monitor session flip its own link's cadence at runtime (item 2,
// "agent-selectable attention") — manual_only becomes a real, reachable mode
// instead of a one-way dead end.
func TestOrchestrateSetAttentionFlipsCadence(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	monitor := mkSessionRouter(t, srv.URL, "monitor")
	subject := mkSessionRouter(t, srv.URL, "subject")

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: monitor.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetMonitor,
		Cadence:           contracts.CadencePerTurn,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create monitor link: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)
	if link.Cadence != contracts.CadencePerTurn {
		t.Fatalf("initial cadence = %q, want per_turn", link.Cadence)
	}

	attnResp := postJSON(t, srv.URL+"/sessions/"+monitor.ID+"/orchestrate/attention", map[string]string{
		"link_id": link.ID,
		"cadence": string(contracts.CadenceManualOnly),
	})
	if attnResp.StatusCode != http.StatusOK {
		t.Fatalf("set_attention: status %d", attnResp.StatusCode)
	}
	updated := decodeJSON[contracts.Link](t, attnResp)
	if updated.Cadence != contracts.CadenceManualOnly {
		t.Fatalf("cadence after set_attention = %q, want manual_only", updated.Cadence)
	}
	if len(updated.Grants) != 1 || updated.Grants[0] != contracts.GrantRead {
		t.Fatalf("grants after set_attention = %v, want [read] unchanged", updated.Grants)
	}

	getResp, err := http.Get(srv.URL + "/links/" + link.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[contracts.Link](t, getResp)
	if got.Cadence != contracts.CadenceManualOnly {
		t.Fatalf("stored cadence = %q, want manual_only", got.Cadence)
	}
}

// TestOrchestrateSetAttentionRejectsNonOwnedLink proves set_attention is
// scoped strictly to links the calling session holds the granting side of —
// a session naming a link it doesn't hold is rejected, never silently
// resolved to someone else's link.
func TestOrchestrateSetAttentionRejectsNonOwnedLink(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	monitor := mkSessionRouter(t, srv.URL, "monitor")
	subject := mkSessionRouter(t, srv.URL, "subject")
	other := mkSessionRouter(t, srv.URL, "other")

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: monitor.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetMonitor,
		Cadence:           contracts.CadencePerTurn,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create monitor link: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)

	attnResp := postJSON(t, srv.URL+"/sessions/"+other.ID+"/orchestrate/attention", map[string]string{
		"link_id": link.ID,
		"cadence": string(contracts.CadenceManualOnly),
	})
	if attnResp.StatusCode != http.StatusForbidden {
		t.Fatalf("set_attention from non-owning session: status %d, want 403", attnResp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/links/" + link.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[contracts.Link](t, getResp)
	if got.Cadence != contracts.CadencePerTurn {
		t.Fatalf("cadence changed despite rejection: %q, want per_turn", got.Cadence)
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
	if want := (contracts.Attribution{SourceName: "leader-1", Kind: "message", Relation: "your leader"}); got.Attribution == nil || *got.Attribution != want {
		t.Fatalf("attribution = %+v, want %+v", got.Attribution, want)
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

// TestOrchestrateMessageBoundedWaitReturnsReply proves message_session's
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

// TestOrchestrateMessageBoundedWaitTimesOut proves message_session degrades to
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
