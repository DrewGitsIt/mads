package api

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// Push (docs/COMMIT-LAND-PUSH-DESIGN.md D5):
//
//	GET  /sessions/{id}/push?repo=&target=   fetch, then what the confirmation shows
//	POST /sessions/{id}/push                 push (rebasing first only if asked)
//
// The Router runs both itself, live child or not: a push touches no file
// the agent's turn holds, and its rebase — the one step that rewrites a
// checkout — is gated on every session working in the repo being idle,
// which only the Router can see across sessions.

func (s *server) handleSessionPushPreview(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	p, err := s.worktreeOps().PushPreview(r.Context(), sess.ID, q.Get("repo"), q.Get("target"), s.pushGates(r.Context(), sess))
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "push preview: ", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) handleSessionPush(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	var req contracts.PushRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	res, err := s.worktreeOps().Push(ctx, sess.ID, req, s.pushGates(ctx, sess))
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "push: ", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// pushGates is what gates a push's rebase: any session with a turn in
// flight working in one of this session's repos (this one included), and
// a pipeline run for this session. A failed listing counts as busy.
func (s *server) pushGates(ctx context.Context, sess contracts.Session) sessionwt.Gates {
	g := sessionwt.Gates{Pipeline: s.sup.HasPipeline(sess.ID)}
	_, all, _, err := s.worktreeOps().Repos(ctx, sess.ID)
	if err != nil && !errors.Is(err, sessionwt.ErrNoFolder) {
		log.Printf("router: push gates for %s: %v", sess.ID, err)
		g.Busy = true
		return g
	}
	var dirs []string
	for _, t := range all {
		dirs = append(dirs, shadow.RealPath(t.Folder), shadow.RealPath(t.Primary))
	}
	// Records spell folders as they were given, git as they resolve (a
	// symlinked home, macOS's /var): compare them resolved.
	in := func(dir, p string) bool { return p != "" && within(dir, shadow.RealPath(p)) }
	others, err := s.data.Sessions(ctx, "")
	if err != nil {
		log.Printf("router: push gates for %s: list sessions: %v", sess.ID, err)
		g.Busy = true
		return g
	}
	for _, o := range others {
		if o.AgentState == nil || o.AgentState.State != contracts.AgentWorking {
			continue
		}
		for _, d := range dirs {
			if d != "" && (in(d, o.Dir) || in(d, o.Cwd) || in(d, o.WorktreeDir)) {
				g.Busy = true
				return g
			}
		}
	}
	return g
}
