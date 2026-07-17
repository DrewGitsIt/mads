package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// orchestrateTranscriptTailLen bounds get_summary's live-transcript fallback
// (no Session.Summary yet) to a short, recent slice — this endpoint answers
// a leader agent's tool call, not a UI pane, so it stays terse by default.
const orchestrateTranscriptTailLen = 20

// maxOrchestrateWaitMS caps send_message's optional bounded wait so a
// leader's tool call can never hold the Router's handler goroutine open
// indefinitely.
const maxOrchestrateWaitMS int64 = 30000

// orchestratePollInterval is how often the bounded wait re-checks for a
// reply landing back on the leader's own session.
const orchestratePollInterval = 50 * time.Millisecond

// handleOrchestrateSubjects lists the subjects {id} (the leader session)
// holds an orchestrate grant over — the orchestrate MCP server's list_links
// tool (docs/LINKED-SESSIONS-DESIGN.md decomposition item 5).
func (s *server) handleOrchestrateSubjects(w http.ResponseWriter, r *http.Request) {
	leaderID := r.PathValue("id")
	links, err := s.data.LinksForSession(r.Context(), leaderID)
	if err != nil {
		writeError(w, err)
		return
	}
	type orchestrateSubject struct {
		LinkID           string   `json:"link_id"`
		SubjectSessionID string   `json:"subject_session_id"`
		SubjectName      string   `json:"subject_name"`
		Grants           []string `json:"grants"`
		Activity         string   `json:"activity"`
	}
	out := []orchestrateSubject{}
	for _, l := range links {
		if l.GrantingSessionID != leaderID || !l.HasGrant(contracts.GrantOrchestrate) {
			continue
		}
		name := l.SubjectSessionID
		activity := ""
		if sess, err := s.data.Session(r.Context(), l.SubjectSessionID); err == nil {
			name = sess.Name
			activity = string(sess.Activity)
		}
		grants := make([]string, len(l.Grants))
		for i, g := range l.Grants {
			grants[i] = string(g)
		}
		out = append(out, orchestrateSubject{
			LinkID:           l.ID,
			SubjectSessionID: l.SubjectSessionID,
			SubjectName:      name,
			Grants:           grants,
			Activity:         activity,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// findOrchestrateLink locates the (at most one) link from leaderID to
// subjectID that carries the orchestrate grant. No dedicated single-pair
// lookup exists on the Data Service (L1), so this filters LinksForSession
// the same way handleOrchestrateSubjects does.
func (s *server) findOrchestrateLink(ctx context.Context, leaderID, subjectID string) (*contracts.Link, error) {
	links, err := s.data.LinksForSession(ctx, leaderID)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.GrantingSessionID == leaderID && l.SubjectSessionID == subjectID && l.HasGrant(contracts.GrantOrchestrate) {
			link := l
			return &link, nil
		}
	}
	return nil, nil
}

// handleOrchestrateSummary answers {id}'s (the leader) get_summary tool call
// for the subject named by the "for" query param: the subject's durable
// Session.Summary when one exists, else a short live-transcript tail — the
// one place this endpoint invents behavior beyond what already exists
// (docs/LINKED-SESSIONS-DESIGN.md decomposition item 5).
func (s *server) handleOrchestrateSummary(w http.ResponseWriter, r *http.Request) {
	leaderID := r.PathValue("id")
	subjectID := r.URL.Query().Get("for")
	link, err := s.findOrchestrateLink(r.Context(), leaderID, subjectID)
	if err != nil {
		writeError(w, err)
		return
	}
	if link == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no orchestrate link to that subject"})
		return
	}
	subject, err := s.data.Session(r.Context(), subjectID)
	if err != nil {
		writeError(w, err)
		return
	}
	if subject.Summary != "" {
		writeJSON(w, http.StatusOK, map[string]string{"source": "summary", "text": subject.Summary})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"source": "transcript_tail",
		"text":   s.orchestrateTranscriptTail(r.Context(), subject),
	})
}

// fetchChildJSON GETs path from a running child and decodes its JSON body
// into v — the GET-based counterpart to postJSONToChild, needed here since
// proxyTo writes straight to the ResponseWriter and can't hand the Router a
// value to inspect server-side.
func (s *server) fetchChildJSON(ctx context.Context, addr, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("child %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// orchestrateTranscriptTail reads subject's transcript the same way
// handleSessionTranscript does (live child first, durable fallback), then
// takes the last orchestrateTranscriptTailLen messages and joins them into
// plain text for a leader agent to read directly.
func (s *server) orchestrateTranscriptTail(ctx context.Context, subject contracts.Session) string {
	var msgs []contracts.TranscriptMessage
	if addr, ok := s.sup.SessionAddr(subject.ID); ok {
		if err := s.fetchChildJSON(ctx, addr, "/transcript", &msgs); err != nil {
			msgs = nil
		}
	}
	if msgs == nil {
		msgs = s.transcriptFor(ctx, subject)
	}
	if len(msgs) > orchestrateTranscriptTailLen {
		msgs = msgs[len(msgs)-orchestrateTranscriptTailLen:]
	}
	lines := make([]string, len(msgs))
	for i, m := range msgs {
		lines[i] = m.Role + ": " + m.Text
	}
	return strings.Join(lines, "\n")
}

// resolveLinkAttribution resolves a link's current Attribution for delivery
// into its subject — the granting session's Name snapshotted at delivery
// time, falling back to the raw ID if that session's since been deleted
// (links don't cascade). Shared by handleLinkInject and
// handleOrchestrateMessage.
func (s *server) resolveLinkAttribution(ctx context.Context, link contracts.Link) contracts.Attribution {
	sourceName := link.GrantingSessionID
	if granting, err := s.data.Session(ctx, link.GrantingSessionID); err == nil {
		sourceName = granting.Name
	}
	return contracts.Attribution{LinkID: link.ID, SourceName: sourceName}
}

// deliverLinkMessage POSTs text into link's subject session's /inject
// endpoint. It duplicates deliverDigest's POST logic (job/digest.go) rather
// than reusing postJSONToChild: send_message composes its own response
// (optionally augmented with a bounded-wait reply below), so there is
// nothing for postJSONToChild's direct-to-ResponseWriter write to attach to
// — the same "different execution context" reasoning that already justifies
// deliverDigest's duplication. delivered is false with a nil error when the
// subject isn't currently running.
func (s *server) deliverLinkMessage(ctx context.Context, link contracts.Link, text string) (delivered bool, err error) {
	addr, ok := s.sup.SessionAddr(link.SubjectSessionID)
	if !ok {
		return false, nil
	}
	raw, err := json.Marshal(contracts.InjectRequest{
		Text:        text,
		Attribution: s.resolveLinkAttribution(ctx, link),
	})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/inject", bytes.NewReader(raw))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return false, fmt.Errorf("child inject: status %d", resp.StatusCode)
	}
	return true, nil
}

// handleOrchestrateMessage delivers a leader's send_message tool call into
// its subject's conversation, always attributed and queued exactly like
// plain inject (decisions 1-2) — the orchestrate grant only changes who may
// call it, not how delivery works (decision 3: trust stays capped by the
// subject's own StartOptions.Trust, unaffected by anything here). An
// optional bounded wait_ms polls the leader's own session for the digest
// L2b delivers back once the subject replies, for quick-question ergonomics.
func (s *server) handleOrchestrateMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text   string `json:"text"`
		WaitMS int64  `json:"wait_ms,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	link, err := s.data.Link(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !link.HasGrant(contracts.GrantOrchestrate) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "link does not carry the orchestrate grant"})
		return
	}

	waitMS := body.WaitMS
	if waitMS > maxOrchestrateWaitMS {
		waitMS = maxOrchestrateWaitMS
	}
	var sinceSeq int64
	if waitMS > 0 {
		sinceSeq = s.latestLinkMessageInjectedSeq(r.Context(), link.GrantingSessionID)
	}

	sendTime := time.Now()
	delivered, err := s.deliverLinkMessage(r.Context(), link, body.Text)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if !delivered {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "subject session not running"})
		return
	}

	resp := map[string]string{"status": "delivered"}
	if waitMS > 0 {
		if reply, ok := s.awaitOrchestrateReply(r.Context(), link, sinceSeq, sendTime, time.Duration(waitMS)*time.Millisecond); ok {
			resp["reply"] = reply
		} else {
			resp["note"] = "still working; reply will arrive as a digest"
		}
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// latestLinkMessageInjectedSeq returns the highest Seq already recorded for
// sessionID's link.message_injected events (0 if none) — the baseline
// awaitOrchestrateReply polls forward from, so an unrelated event already on
// the leader's session before this send can never be mistaken for the reply.
func (s *server) latestLinkMessageInjectedSeq(ctx context.Context, sessionID string) int64 {
	envs, err := s.data.Events(ctx, 0, sessionID, []string{string(contracts.LinkMessageInjected)})
	if err != nil {
		return 0
	}
	var max int64
	for _, e := range envs {
		if e.Seq > max {
			max = e.Seq
		}
	}
	return max
}

// awaitOrchestrateReply polls the leader's own session (link.GrantingSessionID,
// not the subject's) for a link.message_injected event landing after
// sinceSeq/sendTime: L2b's digest delivery lands back on the leader via the
// leader's own POST /inject, which runs through the same audit path
// (runner.recordInjectionAudit) and appends this event type scoped to the
// leader's session — not the subject's. Best-effort correlation only: no
// per-request correlation ID exists (out of scope to add one), so an
// unrelated digest on the same link landing during the wait window could be
// mis-picked-up as "the reply" (docs/LINKED-SESSIONS-DESIGN.md decomposition
// item 5, noted non-goal).
func (s *server) awaitOrchestrateReply(ctx context.Context, link contracts.Link, sinceSeq int64, sendTime time.Time, wait time.Duration) (text string, ok bool) {
	deadline := time.Now().Add(wait)
	for {
		envs, err := s.data.Events(ctx, sinceSeq, link.GrantingSessionID, []string{string(contracts.LinkMessageInjected)})
		if err == nil {
			for _, e := range envs {
				if e.Seq > sinceSeq {
					sinceSeq = e.Seq
				}
				if e.Timestamp.Before(sendTime) {
					continue
				}
				var payload struct {
					LinkID string `json:"link_id"`
					Text   string `json:"text"`
				}
				if json.Unmarshal(e.Payload, &payload) == nil && payload.LinkID == link.ID {
					return payload.Text, true
				}
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", false
		}
		sleep := orchestratePollInterval
		if remaining < sleep {
			sleep = remaining
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(sleep):
		}
	}
}
