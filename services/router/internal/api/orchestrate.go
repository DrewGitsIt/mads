package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/turndigest"
)

// orchestrateTranscriptTailLen bounds get_summary's live-transcript fallback
// (no Session.Summary yet) to a short, recent slice — this endpoint answers
// a leader agent's tool call, not a UI pane, so it stays terse by default.
const orchestrateTranscriptTailLen = 20

// orchestrateLiveTurnLimit bounds how many of a subject's most recent
// session.turn_ended envelopes get_summary's live-compression path
// considers — enough real material for a useful digest without querying a
// long-running subject's entire turn history on every leader tool call.
const orchestrateLiveTurnLimit = 20

// orchestrateSummaryBudgetTokens is get_summary's live-compression budget:
// terse enough for a leader's tool-call turn, generous enough that a
// long-transcript fixture still yields readable prose citing a turn range.
const orchestrateSummaryBudgetTokens = 1000

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
		if l.GrantingSessionID != leaderID || (!l.HasGrant(contracts.GrantOrchestrate) && !l.HasGrant(contracts.GrantRead)) {
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
// subjectID that carries the orchestrate or read grant — get_summary/
// get_turns are pull tools available to both leaders and monitors (item 1,
// "pull tools for read grants"); only send_message stays orchestrate-only.
// No dedicated single-pair lookup exists on the Data Service (L1), so this
// filters LinksForSession the same way handleOrchestrateSubjects does.
func (s *server) findOrchestrateLink(ctx context.Context, leaderID, subjectID string) (*contracts.Link, error) {
	links, err := s.data.LinksForSession(ctx, leaderID)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if l.GrantingSessionID == leaderID && l.SubjectSessionID == subjectID &&
			(l.HasGrant(contracts.GrantOrchestrate) || l.HasGrant(contracts.GrantRead)) {
			link := l
			return &link, nil
		}
	}
	return nil, nil
}

// handleOrchestrateSummary answers {id}'s (the leader) get_summary tool call
// for the subject named by the "for" query param. A live subject (one with
// session.turn_ended envelopes) gets a compressed digest of its most recent
// turns via pkg/turndigest, with the subject's durable Session.Summary
// prepended as a "prologue" field when present — the durable summary alone
// is stale for a subject that's still running, which is the leader's main
// case (docs/LINKED-SESSIONS-DESIGN.md decomposition item 5). A subject with
// no turn_ended envelopes falls back to the durable summary, or a short raw
// transcript tail if it has none either — unchanged from before this digest
// path existed.
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

	envs := s.orchestrateTurnEnvelopes(r.Context(), subjectID)
	if len(envs) == 0 {
		writeJSON(w, http.StatusOK, s.orchestrateSummaryFallback(r.Context(), subject))
		return
	}
	if len(envs) > orchestrateLiveTurnLimit {
		envs = envs[len(envs)-orchestrateLiveTurnLimit:]
	}
	turns := make([]turndigest.Turn, len(envs))
	for i, env := range envs {
		turns[i] = orchestrateTurnFromEnvelope(env)
	}

	res, err := turndigest.New(s.jobs.FastModel()).Digest(r.Context(), turndigest.Request{
		Turns:        turns,
		Audience:     turndigest.LeaderCatchup,
		BudgetTokens: orchestrateSummaryBudgetTokens,
		Subject:      subjectID,
	})
	if err != nil || res.Text == "" {
		// Best-effort: a hard digest failure, or every envelope decoding to
		// nothing, degrades to the same fallback as the no-envelopes case.
		writeJSON(w, http.StatusOK, s.orchestrateSummaryFallback(r.Context(), subject))
		return
	}

	resp := map[string]any{
		"source":    "compressed_recent",
		"text":      res.Text,
		"from_turn": res.FromTurn,
		"to_turn":   res.ToTurn,
	}
	if subject.Summary != "" {
		resp["prologue"] = subject.Summary
	}
	writeJSON(w, http.StatusOK, resp)
}

// orchestrateSummaryFallback is get_summary's pre-digest behavior, unchanged:
// the subject's durable Session.Summary when set, else a short raw
// transcript tail.
func (s *server) orchestrateSummaryFallback(ctx context.Context, subject contracts.Session) map[string]string {
	if subject.Summary != "" {
		return map[string]string{"source": "summary", "text": subject.Summary}
	}
	return map[string]string{
		"source": "transcript_tail",
		"text":   s.orchestrateTranscriptTail(ctx, subject),
	}
}

// orchestrateTurnEnvelopes returns subjectID's session.turn_ended envelopes,
// oldest first. Best-effort: a query error is treated as "no live turn data"
// rather than surfaced, matching every other best-effort path in this file.
func (s *server) orchestrateTurnEnvelopes(ctx context.Context, subjectID string) []contracts.Envelope {
	envs, err := s.data.Events(ctx, 0, subjectID, []string{string(contracts.TurnEnded)})
	if err != nil {
		return nil
	}
	return envs
}

// orchestrateTurnFromEnvelope decodes a session.turn_ended envelope's
// {"delta": "..."} payload into a turndigest.Turn, using the envelope's Seq
// (the Data Service's global, gapless, monotonically increasing log
// position, pkg/contracts/event.go) as the turn's anchor number — the one
// stable identifier a turn_ended envelope carries, and exactly what
// get_turns's from_turn/to_turn need to recover the same range a digest
// cites. turndigest.Parse recovers the structured Entry list (kind + text,
// tool calls reduced to name+path) from the delta's rendered line format,
// the same normalized shape GenerateDigest already builds from the same
// payload for its own compression (services/router/internal/job/digest.go)
// — a digest-of-already-normalized-data, not a re-summarization from
// scratch. A payload that fails to decode yields an empty turn (skipped by
// Digest, since it contributes no entries).
func orchestrateTurnFromEnvelope(env contracts.Envelope) turndigest.Turn {
	var payload struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return turndigest.Turn{Number: int(env.Seq)}
	}
	return turndigest.Turn{Number: int(env.Seq), Entries: turndigest.Parse(payload.Delta)}
}

// orchestrateTurnEntry is one turndigest.Entry tagged with the turn number
// (envelope Seq) it came from — get_turns's response shape, so a leader can
// see turn boundaries within the range it's drilling into.
type orchestrateTurnEntry struct {
	Turn int    `json:"turn"`
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
	Tool string `json:"tool,omitempty"`
	Path string `json:"path,omitempty"`
}

// handleOrchestrateGetTurns answers {id}'s (the leader) get_turns tool call:
// the normalized (turndigest.Parse'd, not model-compressed) entries for the
// subject's session.turn_ended envelopes whose turn number (Seq) falls in
// [from_turn, to_turn] — recoverable loss instead of minimal loss, letting a
// leader drill down into the anchor range a get_summary digest cited. Gated
// by the same orchestrate-link check as get_summary.
func (s *server) handleOrchestrateGetTurns(w http.ResponseWriter, r *http.Request) {
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
	fromTurn, _ := strconv.Atoi(r.URL.Query().Get("from_turn"))
	toTurn, _ := strconv.Atoi(r.URL.Query().Get("to_turn"))

	entries := []orchestrateTurnEntry{}
	for _, env := range s.orchestrateTurnEnvelopes(r.Context(), subjectID) {
		turn := orchestrateTurnFromEnvelope(env)
		if turn.Number < fromTurn || turn.Number > toTurn {
			continue
		}
		for _, e := range turn.Entries {
			entries = append(entries, orchestrateTurnEntry{Turn: turn.Number, Kind: e.Kind, Text: e.Text, Tool: e.Tool, Path: e.Path})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from_turn": fromTurn,
		"to_turn":   toTurn,
		"entries":   entries,
	})
}

// handleOrchestrateSetAttention answers {id}'s (the caller's own session,
// trustworthy the same way leaderID is in handleOrchestrateSummary/
// handleOrchestrateGetTurns) set_attention tool call: flips one of its own
// held links' Cadence at runtime (item 2, "agent-selectable attention") —
// this is what turns manual_only into a real "catch up when requested" mode
// rather than a one-way, unrecoverable dead end. Scoped strictly to links
// {id} itself holds the granting side of; a link_id for a link {id} doesn't
// hold is rejected, never silently resolved to someone else's link.
// handleOrchestrateDrainMailbox is the agent-facing check_mailbox tool:
// atomically return and clear the caller's own link's queued digest backlog
// (docs/LINKED-SESSIONS-DESIGN.md decision 4, the on_signal pull half).
// Ownership check mirrors set_attention: only the granting session may
// drain, and only over a read-granting link.
func (s *server) handleOrchestrateDrainMailbox(w http.ResponseWriter, r *http.Request) {
	callerID := r.PathValue("id")
	var body struct {
		LinkID string `json:"link_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	link, err := s.data.Link(r.Context(), body.LinkID)
	if err != nil {
		writeError(w, err)
		return
	}
	if link.GrantingSessionID != callerID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "session does not hold that link"})
		return
	}
	if !link.HasGrant(contracts.GrantRead) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "link carries no read grant"})
		return
	}
	digests, err := s.data.DrainLinkDigests(r.Context(), link.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if digests == nil {
		digests = []contracts.LinkDigest{}
	}
	writeJSON(w, http.StatusOK, digests)
}

func (s *server) handleOrchestrateSetAttention(w http.ResponseWriter, r *http.Request) {
	callerID := r.PathValue("id")
	var body struct {
		LinkID  string                `json:"link_id"`
		Cadence contracts.LinkCadence `json:"cadence"`
	}
	if !decode(w, r, &body) {
		return
	}
	link, err := s.data.Link(r.Context(), body.LinkID)
	if err != nil {
		writeError(w, err)
		return
	}
	if link.GrantingSessionID != callerID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "session does not hold that link"})
		return
	}
	updated, err := s.data.UpdateLink(r.Context(), link.ID, contracts.LinkRequest{
		GrantingSessionID: link.GrantingSessionID,
		SubjectSessionID:  link.SubjectSessionID,
		Grants:            link.Grants,
		Cadence:           body.Cadence,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
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
					LinkID     string `json:"link_id"`
					SourceName string `json:"source_name"`
					Text       string `json:"text"`
				}
				if json.Unmarshal(e.Payload, &payload) == nil && payload.LinkID == link.ID &&
					payload.SourceName != grantNoticeSource {
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
