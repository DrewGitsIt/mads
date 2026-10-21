package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/madsbin"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// orchestrateRefreshTimeout bounds notifyOrchestrateRefresh's fire-and-forget
// call to the granting session's own child — long enough to cover a
// swapTransport cycle (close old process, start new one) without holding the
// notification open indefinitely.
const orchestrateRefreshTimeout = 30 * time.Second

// notifyOrchestrateRefresh tells sessionID's running child (if any) to
// re-evaluate its held orchestrate grants (docs/LINKED-SESSIONS-DESIGN.md:
// dynamic orchestrate materialization) — POST /orchestrate/refresh, the
// runner route that wraps Runner.RefreshOrchestrate
// (services/session/internal/runner/capability.go). Fire-and-forget by
// design: it runs in its own goroutine with its own timeout context, never
// blocking or writing to the link CRUD handler's own ResponseWriter — a
// session that isn't running has nothing to refresh (silent no-op, not an
// error), and a session that is running gets this exactly once per grant
// mutation regardless of how long the resulting cycle takes.
func (s *server) notifyOrchestrateRefresh(sessionID string) {
	addr, ok := s.sup.SessionAddr(sessionID)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), orchestrateRefreshTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/orchestrate/refresh", nil)
		if err != nil {
			log.Printf("router: build orchestrate refresh request for %s: %v", sessionID, err)
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("router: notify orchestrate refresh for %s: %v", sessionID, err)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusAccepted {
			log.Printf("router: orchestrate refresh for %s: status %d", sessionID, resp.StatusCode)
		}
	}()
}

// announceOrchestrateGrant handles orchestrate newly granted over link
// (a create or an upgrade) while the granting session's child is live. The
// grant is pending_restart at that point: its MCP tools mount only when the
// runner can cycle its provider process, which waits for the agent to be
// idle. So the agent first gets one attributed notice naming the subject and
// giving shell commands that work right away (the mads CLI's orchestrate
// subcommands, by absolute path), and only then the refresh. The notice's
// turn is in flight when the refresh lands, so the runner defers the cycle
// to that turn's end instead of swapping the process out from under it.
// With no live child this is just the refresh, a no-op: the tools mount when
// the session next starts.
func (s *server) announceOrchestrateGrant(link contracts.Link) {
	addr, ok := s.sup.SessionAddr(link.GrantingSessionID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), orchestrateRefreshTimeout)
		defer cancel()
		subjectName := link.SubjectSessionID
		if sess, err := s.data.Session(ctx, link.SubjectSessionID); err == nil && sess.Name != "" {
			subjectName = sess.Name
		}
		notice := orchestrateGrantNotice(madsbin.Path(), s.routerURL, link.GrantingSessionID, link.SubjectSessionID, subjectName)
		attribution := contracts.Attribution{
			LinkID: link.ID, SourceName: grantNoticeSource, DigestAttributed: true,
		}
		if !ok {
			// Asleep: the tools mount when the session next starts anyway,
			// so the notice is deferred and replays then (job.FlushDeferred)
			// rather than waking the leader for it.
			body, hadNoop := strings.CutSuffix(notice, job.NoopInstruction)
			job.DeferPush(ctx, job.Deps{Data: s.data}, link, body, hadNoop, attribution)
			return
		}
		if err := postInject(ctx, addr, contracts.InjectRequest{
			Text:        notice,
			Attribution: attribution,
		}); err != nil {
			log.Printf("router: orchestrate grant notice for %s: %v", link.GrantingSessionID, err)
		}
		s.notifyOrchestrateRefresh(link.GrantingSessionID)
	}()
}

// grantNoticeSource attributes the Router's own grant notice. It isn't a
// message from the subject, so awaitOrchestrateReply never takes it for a
// reply, even though it's injected over the same link.
const grantNoticeSource = "mads"

// orchestrateGrantNotice is the text of announceOrchestrateGrant's notice.
// Its tone matches the link tools' own MCP instructions (pkg/orchestratemcp,
// 2026-10-19): the user made this link and suggests these tools, so the
// notice names them by what they're for rather than as a capability list.
func orchestrateGrantNotice(mads, routerURL, leaderID, subjectID, subjectName string) string {
	flags := "-session " + leaderID
	if routerURL != "" {
		flags += " -router " + routerURL
	}
	return fmt.Sprintf("The user has linked you to the session %q (%s) as its leader, "+
		"and suggests you communicate with it through your MADS link tools: "+
		"message_session to send it instructions or answers, get_session_summary to read its replies, "+
		"get_session_turns to audit its exact tool calls. Messages you send appear in its conversation, "+
		"attributed to you; its replies come back to you as digests. "+
		"The tools load the next time you're idle. Until then, the same actions work from a shell:\n\n"+
		"  %[3]s orchestrate subjects %[4]s\n"+
		"  %[3]s orchestrate summary %[4]s -subject %[2]s\n"+
		"  %[3]s orchestrate send %[4]s -subject %[2]s -text \"your message\"\n\n"+
		"(If nothing here needs your action, reply with exactly NOOP.)",
		subjectName, subjectID, mads, flags)
}

// postInject POSTs req to a session child's /inject.
func postInject(ctx context.Context, addr string, req contracts.InjectRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/inject", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("child inject: status %d", resp.StatusCode)
	}
	return nil
}

// requireLinkedSessions 404s while the linked-sessions flag is off — every
// route this feature exposes is gated identically
// (docs/LINKED-SESSIONS-DESIGN.md: "Everything ships behind the
// linked-sessions flag"). Gating lives only in the Router; the Data Service
// stays a flag-agnostic persistence layer, consistent with how no other
// feature flag is enforced below the Router today.
func (s *server) requireLinkedSessions(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := s.data.Config(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if !cfg.FeatureFlags.LinkedSessions {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

// handleCreateLink notifies the granting session's running child
// (notifyOrchestrateRefresh) whenever orchestrate is held before or after —
// the case this task exists for: a leader link created onto an
// already-running session must materialize the tools without waiting for a
// bounce, where the provider supports it. A create for a pair that already
// has a link merges into it (the Data Service's Links.Create), so it can
// also drop orchestrate (leader re-created as monitor), which needs the same
// teardown refresh an update would get.
func (s *server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	var req contracts.LinkRequest
	if !decode(w, r, &req) {
		return
	}
	prior := s.pairLink(r.Context(), req.GrantingSessionID, req.SubjectSessionID)
	link, err := s.data.CreateLink(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	switch {
	case link.HasGrant(contracts.GrantOrchestrate) && (prior == nil || !prior.HasGrant(contracts.GrantOrchestrate)):
		s.announceOrchestrateGrant(link)
	case link.HasGrant(contracts.GrantOrchestrate) || (prior != nil && prior.HasGrant(contracts.GrantOrchestrate)):
		s.notifyOrchestrateRefresh(link.GrantingSessionID)
	}
	writeJSON(w, http.StatusCreated, link)
}

// pairLink returns the existing non-implicit link from grantingID to
// subjectID, or nil if there is none or it can't be read. Best-effort: it
// only decides whether a create needs an orchestrate refresh, and the create
// itself re-reads the pair under its own transaction.
func (s *server) pairLink(ctx context.Context, grantingID, subjectID string) *contracts.Link {
	links, err := s.data.LinksForSession(ctx, grantingID)
	if err != nil {
		return nil
	}
	for _, l := range links {
		if l.GrantingSessionID == grantingID && l.SubjectSessionID == subjectID && !l.Implicit {
			return &l
		}
	}
	return nil
}

func (s *server) handleGetLink(w http.ResponseWriter, r *http.Request) {
	link, err := s.data.Link(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

// handleUpdateLink notifies on either side of an orchestrate-grant change —
// newly added (materialize) or newly dropped (teardown) — by comparing the
// grant set before and after the update. A monitor-only edit (orchestrate
// absent both before and after) never notifies at all.
func (s *server) handleUpdateLink(w http.ResponseWriter, r *http.Request) {
	var req contracts.LinkRequest
	if !decode(w, r, &req) {
		return
	}
	old, err := s.data.Link(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	link, err := s.data.UpdateLink(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeError(w, err)
		return
	}
	switch {
	case link.HasGrant(contracts.GrantOrchestrate) && !old.HasGrant(contracts.GrantOrchestrate):
		s.announceOrchestrateGrant(link)
	case old.HasGrant(contracts.GrantOrchestrate) || link.HasGrant(contracts.GrantOrchestrate):
		s.notifyOrchestrateRefresh(link.GrantingSessionID)
	}
	writeJSON(w, http.StatusOK, link)
}

// handleDeleteLink notifies the granting session's running child after
// removing a link that carried the orchestrate grant — the symmetric
// teardown half of handleCreateLink's materialization.
func (s *server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	old, err := s.data.Link(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.data.DeleteLink(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	if old.HasGrant(contracts.GrantOrchestrate) {
		s.notifyOrchestrateRefresh(old.GrantingSessionID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *server) handleListSessionLinks(w http.ResponseWriter, r *http.Request) {
	list, err := s.data.LinksForSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleLinkInject delivers a message from the link's granting session into
// its subject's conversation (docs/LINKED-SESSIONS-DESIGN.md decisions
// 1-2): the Router resolves the Attribution here — link id and the granting
// session's Name, snapshotted at delivery time — and forwards it to the
// subject's running child, which queues it behind any in-flight turn.
func (s *server) handleLinkInject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	link, err := s.data.Link(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !link.HasGrant(contracts.GrantInject) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "link does not carry the inject grant"})
		return
	}
	// A sleeping subject is woken, not refused: the message needs its
	// attention. The wake is ensureSession's, minus the composer's
	// clear-aside (a link message isn't the user changing their mind).
	addr, err := s.ensureSession(r.Context(), link.SubjectSessionID)
	if err != nil {
		writeWakeError(w, err)
		return
	}
	status, resp, err := job.PostInject(r.Context(), addr, contracts.InjectRequest{
		Text:        body.Text,
		Attribution: s.resolveLinkAttribution(r.Context(), link),
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(resp)
}
