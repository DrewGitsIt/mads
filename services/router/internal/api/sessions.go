package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
)

func (s *server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	s.decorateSession(&sess)
	writeJSON(w, http.StatusOK, sess)
}

func (s *server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		contracts.Session
		// Links stages link requests to create between the session record and
		// its spawn, so a leader's orchestrate MCP is materialized on the
		// child's first turn (setupOrchestrate reads LinksForSession at boot).
		// Either endpoint may be contracts.NewSessionPlaceholder.
		Links []contracts.LinkRequest `json:"links,omitempty"`
		// OpeningPrompt is the composer's full text, sent only so agent="auto"
		// (see resolveAutoAgent) has free text to infer from — the session
		// record itself never persists it; Session.Name only ever carries the
		// first line (see Launchpad's deriveSessionName).
		OpeningPrompt string `json:"opening_prompt,omitempty"`
		// AttachmentDraft names the launchpad draft whose attachments the
		// opening message refers to — moved to the session once it's up.
		AttachmentDraft string `json:"attachment_draft,omitempty"`
	}
	if !decode(w, r, &req) {
		return
	}
	sess := &req.Session
	sess.ID = ""
	// Creation time and import provenance are the Data Service's to stamp
	// (and the import's to supply): a client can't backdate a new session.
	sess.CreatedAt = time.Time{}
	sess.ImportedFrom = ""
	if len(req.Links) > 0 {
		// Unlike requireLinkedSessions (which 404s whole routes), only the
		// links field is refused flag-off — POST /sessions must keep working.
		cfg, err := s.data.Config(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if !cfg.FeatureFlags.LinkedSessions {
			writeErrMsg(w, http.StatusBadRequest, "create session: ", errors.New(`"links" requires the linked-sessions feature flag`))
			return
		}
	}
	if err := s.resolveSessionDefaults(r.Context(), sess); err != nil {
		writeError(w, err)
		return
	}
	if err := checkSessionDir(sess.Dir); err != nil {
		writeErrMsg(w, http.StatusBadRequest, "create session: ", err)
		return
	}
	if sess.Agent == "auto" {
		if err := s.resolveAutoAgent(r.Context(), sess, req.OpeningPrompt); err != nil {
			writeErrMsg(w, http.StatusBadGateway, "create session: resolve auto agent: ", err)
			return
		}
	}
	if _, err := s.providers.Get(sess.Provider); err != nil {
		writeErrMsg(w, http.StatusBadRequest, "create session: ", err)
		return
	}
	if err := s.validateStagedLinks(r.Context(), sess, req.Links); err != nil {
		writeError(w, err)
		return
	}
	if err := s.data.CreateSession(r.Context(), sess); err != nil {
		writeError(w, err)
		return
	}
	links, err := s.createStagedLinks(r.Context(), sess.ID, req.Links)
	if err != nil {
		s.rollbackStagedCreate(links, sess.ID)
		writeError(w, err)
		return
	}
	addr, err := s.sup.SpawnSession(r.Context(), sess.ID)
	if err != nil {
		// Best-effort rollback so a failed spawn doesn't leave an orphaned
		// session record (or its staged links) with no child behind it. Uses
		// a background context: the records still need cleaning up even if
		// the request that asked for them is gone.
		s.rollbackStagedCreate(links, sess.ID)
		writeErrMsg(w, http.StatusBadGateway, "spawn session: ", err)
		return
	}
	s.adoptDraftAttachments(req.AttachmentDraft, sess.ID)
	s.decorateSession(sess)
	resp := map[string]any{"session": req.Session, "addr": addr}
	if len(req.Links) > 0 {
		resp["links"] = links
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleRenameSession sets a user-chosen name. NameSource=User marks it as
// human-claimed so the auto-title job leaves it alone (see internal/job/title.go),
// and the data store's Update emits session.renamed for live shells.
func (s *server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErrMsg(w, http.StatusBadRequest, "rename session: ", errors.New("name must not be empty"))
		return
	}
	sess, err := s.data.Session(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	sess.Name = name
	sess.NameSource = contracts.SessionNameSourceUser
	if err := s.data.UpdateSession(r.Context(), &sess); err != nil {
		writeError(w, err)
		return
	}
	s.decorateSession(&sess)
	writeJSON(w, http.StatusOK, sess)
}

// handleSetSessionTrust changes a session's trust level after creation —
// the "raise trust" path the blocked-permission banner points at. Trust is
// read exactly once, at child spawn (services/session/internal/runner), so
// when a child is currently running the session is bounced to apply the new
// level — same semantics as the header's restart action, meaning an
// in-flight turn is interrupted. With no live child, the updated record is
// simply what the next spawn reads.
func (s *server) handleSetSessionTrust(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Trust contracts.SessionTrust `json:"trust"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.Trust.Valid() {
		writeErrMsg(w, http.StatusBadRequest, "set session trust: ",
			errors.New("trust must be one of: "+contracts.SessionTrustChoices()))
		return
	}
	sess, err := s.data.Session(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	sess.Trust = body.Trust
	if err := s.data.UpdateSession(r.Context(), &sess); err != nil {
		writeError(w, err)
		return
	}
	if _, running := s.sup.SessionPID(id); running {
		if _, err := s.sup.BounceSession(r.Context(), id); err != nil {
			// The record is updated either way; report the bounce failure so
			// the UI can say "applies on next restart" instead of lying.
			writeErrMsg(w, http.StatusBadGateway, "set session trust: applied, but restarting the agent failed: ", err)
			return
		}
	}
	s.decorateSession(&sess)
	writeJSON(w, http.StatusOK, sess)
}

// handleSetSessionModel changes a session's model after creation — the
// header model pill and the composer's "/model" command. Like trust, the
// model is a spawn-time provider flag (providers/claudecode/session.go's
// --model), so it is persisted on the record first (that's what the next
// cold start reads) and then applied to the live child if there is one.
//
// Unlike trust, applying it is NOT a bounce. The child cycles its provider
// process in place, resuming the same ref, so the conversation survives —
// and if a turn is in flight the child queues the switch behind it rather
// than interrupting the agent (runner.SetModel). Queued comes back in the
// response so the UI can say the change lands at the end of the current turn
// instead of claiming it already took effect.
//
// A child that fails the switch is reported as a 502, but the record is
// already updated by then — the message says so rather than lying about
// which model is running.
func (s *server) handleSetSessionModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Model string `json:"model"`
	}
	if !decode(w, r, &body) {
		return
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		writeErrMsg(w, http.StatusBadRequest, "set session model: ",
			errors.New("model must not be empty"))
		return
	}
	sess, err := s.data.Session(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	sess.Model = model
	if err := s.data.UpdateSession(r.Context(), &sess); err != nil {
		writeError(w, err)
		return
	}
	s.decorateSession(&sess)
	addr, running := s.sup.SessionAddr(id)
	if !running {
		// Nothing live to reconfigure; the updated record is what the next
		// spawn reads. Not queued — there is no turn to queue behind.
		writeJSON(w, http.StatusOK, contracts.SetSessionModelResponse{Session: sess})
		return
	}
	queued, err := s.setChildModel(r.Context(), addr, model)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "set session model: saved, but applying it to the running agent failed: ", err)
		return
	}
	writeJSON(w, http.StatusOK, contracts.SetSessionModelResponse{Session: sess, Queued: queued})
}

// setChildModel forwards the new model to a running session child's own
// POST /model and reports back whether the child queued the switch behind an
// in-flight turn. Not proxyTo: the Router has to merge the child's answer
// with the session record it just updated, which a byte-transparent proxy
// can't do.
func (s *server) setChildModel(ctx context.Context, addr, model string) (queued bool, err error) {
	raw, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/model", bytes.NewReader(raw))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("child set model: status %d", resp.StatusCode)
	}
	var out struct {
		Queued bool `json:"queued"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, fmt.Errorf("child set model: decode response: %w", err)
	}
	return out.Queued, nil
}

func (s *server) handleTerminateSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.sup.TerminateSession(r.Context(), id); err != nil {
		writeErrMsg(w, http.StatusBadGateway, "terminate session: ", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleResumeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.data.Session(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if sess.Activity == contracts.SessionActive {
		writeErrMsg(w, http.StatusConflict, "resume session: ", errors.New("session is already active"))
		return
	}
	if _, tracked := s.sup.SessionAddr(id); tracked {
		writeErrMsg(w, http.StatusConflict, "resume session: ", errors.New("session already has a running child"))
		return
	}
	// Resolve the provider up front, exactly as create does — otherwise a
	// session persisted without one (e.g. an old record predating provider
	// defaulting) spawns a child that crashes at startup with an opaque
	// "exited before ready", surfacing as a 502 instead of a clear cause.
	if _, err := s.providers.Get(sess.Provider); err != nil {
		writeErrMsg(w, http.StatusBadRequest, "resume session: ", err)
		return
	}
	addr, err := s.sup.SpawnSession(r.Context(), id)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "resume session: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"addr": addr})
}

// wakeError distinguishes why wakeSession failed so the UI can show a
// specific reason instead of a bare "session not running" — mirrors
// services/session/internal/api's writeSwitchError/TransportSwitchError
// convention (code/retryable in the JSON body).
type wakeError struct {
	code string // "provider_missing" | "spawn_failed"
	err  error
}

func (e *wakeError) Error() string { return e.err.Error() }

// wakeSession spawns a child for a session with no tracked child, so a
// composer send resumes it transparently — no manual "Resume" step. Reuses
// Supervisor.SpawnSession, the exact path handleResumeSession already
// calls; provider continuity (ProviderRef, empty for a session that never
// conversed) is handled entirely inside the child on startup, same as an
// explicit resume. Callers must already have confirmed no child is tracked
// (SessionAddr false) before calling this — go through ensureSession, which
// makes that check under the session's wake lock.
func (s *server) wakeSession(ctx context.Context, id string) (string, error) {
	sess, err := s.data.Session(ctx, id)
	if err != nil {
		return "", err // not-found etc. — handled by writeError, same as any other lookup
	}
	if _, err := s.providers.Get(sess.Provider); err != nil {
		return "", &wakeError{code: "provider_missing", err: fmt.Errorf("wake session: %w", err)}
	}
	// Truth-enforce activity before respawning — exactly TerminateSession's
	// no-child branch (reconcileInactiveSession), reused rather than forked:
	// a stale "active" record left by a pre-restart orphan (Supervisor state
	// is in-memory only) must not be spawned over silently. No-op if the
	// record is already inactive. Safe here because the caller already
	// confirmed no child is tracked, so this never kills a live child.
	if err := s.sup.TerminateSession(ctx, id); err != nil {
		return "", &wakeError{code: "spawn_failed", err: fmt.Errorf("wake session: reconcile: %w", err)}
	}
	addr, err := s.sup.SpawnSession(ctx, id)
	if err != nil {
		return "", &wakeError{code: "spawn_failed", err: fmt.Errorf("wake session: %w", err)}
	}
	return addr, nil
}

// ensureSession returns id's live child address, waking the session first if
// no child is tracked. The check and the spawn run under a per-session lock:
// wakeSession's contract (the caller has confirmed no child is tracked) only
// holds if nobody else can spawn between the check and the spawn.
func (s *server) ensureSession(ctx context.Context, id string) (string, error) {
	mu, _ := s.wakeLocks.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if addr, ok := s.sup.SessionAddr(id); ok {
		return addr, nil
	}
	return s.wakeSession(ctx, id)
}

// WakeSession implements job.SessionWaker, letting job bodies (a
// task-terminal signal to a dormant pipeline session) wake a session exactly
// as a composer send does.
func (s *server) WakeSession(ctx context.Context, id string) (string, error) {
	return s.ensureSession(ctx, id)
}

func writeWakeError(w http.ResponseWriter, err error) {
	we, ok := err.(*wakeError)
	if !ok {
		writeError(w, err) // not-found etc.
		return
	}
	switch we.code {
	case "provider_missing":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": we.err.Error(), "code": we.code, "retryable": false})
	default: // spawn_failed
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": we.err.Error(), "code": we.code, "retryable": true})
	}
}

// handleDeleteSession deletes a session by terminating it first, never by
// refusing it. It does not gate on Activity or on a tracked child:
// TerminateSessionForDelete is correct in every state — it stops a tracked
// child with the standard grace, and with no child tracked it reconciles a
// record still stuck at active (Supervisor state is in-memory only, so
// Activity drifts from child liveness across a Router restart). Gating on
// the record was what made exactly that drifted session undeletable.
// Terminate-for-delete also suppresses the session-summary job the
// active -> inactive flip would otherwise spawn, so no summary job outlives
// the session it summarises — see Supervisor.TerminateSessionForDelete.
//
// Still failable, just not refusable: a terminate that errors leaves the
// child alive, so the session must not be deleted out from under it.
//
// Once the record is gone, an isolated session's worktree folder and
// branch go too (docs/SESSION-WORKTREES-DESIGN.md D7) — after the child,
// since git removes a folder even with a process working in it, and after
// the record, so a failed delete never loses work. Best-effort: a cleanup
// problem is logged and never fails the delete. The folder and branch stay
// while anything else works in that folder (folderKeepers); the dialog says
// so beforehand.
func (s *server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.data.Session(r.Context(), id)
	if err != nil {
		writeError(w, err) // 404 for an unknown id, before terminating anything
		return
	}
	if err := s.sup.TerminateSessionForDelete(r.Context(), id); err != nil {
		writeErrMsg(w, http.StatusBadGateway, "delete session: terminate: ", err)
		return
	}
	// The child may have written its worktree only now, at its first start.
	if cur, err := s.data.Session(r.Context(), id); err == nil {
		sess = cur
	}
	if err := s.data.DeleteSession(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	// Not while anything else still works in the folder: a linked session
	// that joined it, or this session's pipeline run.
	ctx := context.WithoutCancel(r.Context())
	if tenants, pipeline := s.folderKeepers(ctx, sess); len(tenants) > 0 || pipeline {
		log.Printf("router: delete %s: keeping its folder %s and branch: other sessions work in it %v, pipeline running %v", id, sess.WorktreeDir, tenants, pipeline)
	} else {
		sessionwt.Cleanup(ctx, sess)
	}
	s.removeSessionAttachments(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleBounceSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	addr, err := s.sup.BounceSession(r.Context(), id)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "bounce session: ", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"addr": addr})
}
