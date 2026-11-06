package api

import (
	"context"
	"log"
	"net/http"
	"path/filepath"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// handleSessionCommitPlan is GET /sessions/{id}/commit-plan: what the
// Commit dialog lists (docs/COMMIT-LAND-PUSH-DESIGN.md D2). Read-only and
// lock-free like the worktree status, so the Router answers it itself
// whether or not a child is running.
func (s *server) handleSessionCommitPlan(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	plan, err := s.commitPlan(r.Context(), sess)
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "commit plan: ", err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// commitPlan lists every uncommitted file in each of the session's repos.
// The session's own are Mine. For the rest it says who and when as far as
// it can tell: another session's ledger names who; the session-start
// change set says whether it changed during this session or was already
// uncommitted before it, in a repo with a session-start snapshot. Files
// the session changed outside every repo come from the change set too.
func (s *server) commitPlan(ctx context.Context, sess contracts.Session) (contracts.CommitPlan, error) {
	plan := contracts.CommitPlan{Repos: []contracts.CommitPlanRepo{}, Outside: []contracts.CommitPlanOutside{}}
	primary, all, rows, err := s.worktreeOps().Repos(ctx, sess.ID)
	if err != nil {
		return plan, err
	}

	// Best-effort: without the change set, "when" is left unsaid and
	// nothing is listed outside the repos.
	changed := map[string]contracts.ChangedFile{}
	set, _, err := s.computeChangeSet(ctx, sess, contracts.BaselineSessionStart)
	if err != nil {
		log.Printf("router: commit plan for %s: change set: %v", sess.ID, err)
	}
	for _, f := range set.Files {
		changed[shadow.RealPath(f.Path)] = f
	}

	type ref struct{ repo, file int }
	refs := map[string][]ref{} // absolute path → the files to name
	var others []string
	for i, t := range all {
		files, err := sessionwt.ChangedFiles(ctx, t.Folder)
		if err != nil {
			log.Printf("router: commit plan for %s: status in %s: %v", sess.ID, t.Folder, err)
			continue
		}
		if len(files) == 0 {
			continue
		}
		mine := map[string]bool{}
		for _, p := range sessionwt.SessionFiles(t.Folder, rows) {
			mine[p] = true
		}
		branch := t.Branch
		if branch == "" {
			branch, _ = sessionwt.CurrentBranch(ctx, t.Folder)
		}
		baselined := shadow.HasBaseline(ctx, sess.ShadowDir, shadow.RealPath(t.Folder))
		repo := contracts.CommitPlanRepo{
			Root: t.Folder, Branch: branch, Isolated: t.Isolated(), Primary: primary != nil && i == 0,
			Files: make([]contracts.CommitPlanFile, 0, len(files)),
		}
		for _, f := range files {
			pf := contracts.CommitPlanFile{Path: f.Path, Kind: f.Kind, Mine: mine[f.Path]}
			if !pf.Mine {
				abs := filepath.Join(t.Folder, filepath.FromSlash(f.Path))
				if _, ok := changed[shadow.RealPath(abs)]; ok {
					pf.Since = contracts.ChangedDuringSession
				} else if baselined {
					pf.Since = contracts.ChangedBeforeSession
				}
				if refs[abs] == nil {
					others = append(others, abs)
				}
				refs[abs] = append(refs[abs], ref{len(plan.Repos), len(repo.Files)})
			}
			repo.Files = append(repo.Files, pf)
		}
		plan.Repos = append(plan.Repos, repo)
	}

	if len(others) > 0 {
		touchers, err := s.data.PathTouchers(ctx, others, sess.ID)
		if err != nil {
			log.Printf("router: commit plan for %s: who changed: %v", sess.ID, err)
		}
		for _, tc := range touchers {
			for _, at := range refs[tc.Path] {
				plan.Repos[at.repo].Files[at.file].By = &contracts.WorktreeTenant{ID: tc.SessionID, Name: tc.SessionName}
			}
		}
	}

	for _, f := range set.Files {
		if !f.Attributed || sessionwt.TopOf(ctx, f.Path) != "" {
			continue
		}
		plan.Outside = append(plan.Outside, contracts.CommitPlanOutside{Path: f.Path, Kind: f.Change})
	}
	return plan, nil
}
