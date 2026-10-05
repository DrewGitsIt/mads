package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
)

// validateStagedLinks pre-checks staged link entries before the session
// record exists, so a malformed payload never creates a session. Each entry
// must carry contracts.NewSessionPlaceholder on exactly one endpoint and an
// existing session on the other; everything else (preset resolution, one
// orchestrate holder per subject, ...) stays with the Data Service at create
// time. Must run after resolveSessionDefaults — the join below reads the
// Dir/Workspace it fills in.
//
// Linked sessions must share one tree (docs/LINKED-SESSIONS-DESIGN.md), and
// with isolation on by default nearly every counterpart has its own
// worktree. So a new session staged against a counterpart isolated in the
// same repo joins it rather than being refused: it works in place inside
// the counterpart's folder (Dir set there, Isolation current_dir), its
// commits land on the counterpart's branch, and the counterpart lands
// everything. A new session can join one such folder, not two.
func (s *server) validateStagedLinks(ctx context.Context, sess *contracts.Session, links []contracts.LinkRequest) error {
	var v contracts.ValidationError
	join := ""
	for i, l := range links {
		field := fmt.Sprintf("links[%d]", i)
		grantingNew := l.GrantingSessionID == contracts.NewSessionPlaceholder
		subjectNew := l.SubjectSessionID == contracts.NewSessionPlaceholder
		switch {
		case grantingNew && subjectNew:
			v.Add(field, "the new-session placeholder cannot be both endpoints — a session cannot link to itself")
			continue
		case !grantingNew && !subjectNew:
			v.Add(field, "exactly one endpoint must be the new-session placeholder \"$new\" — a link between two existing sessions belongs on POST /links")
			continue
		}
		counterpartID := l.GrantingSessionID
		if grantingNew {
			counterpartID = l.SubjectSessionID
		}
		if counterpartID == "" {
			v.Add(field, "the non-placeholder endpoint must name an existing session")
			continue
		}
		counterpart, err := s.data.Session(ctx, counterpartID)
		if err != nil {
			return err // not-found → 404 via writeError, before any session is created
		}
		if counterpart.WorktreeDir == "" || !s.sameRepoAs(ctx, sess, counterpart) {
			continue
		}
		switch {
		case join != "" && join != counterpart.WorktreeDir:
			v.Add(field+".worktree", "these sessions each have their own isolated copy of the repo, and a new session can work in only one of them — link it to sessions that share one copy")
		case !dirExists(counterpart.WorktreeDir):
			v.Add(field+".worktree", fmt.Sprintf("session %q works in an isolated copy of the repo that is missing on disk — open it once to restore its folder, then link to it", counterpart.Name))
		default:
			join = counterpart.WorktreeDir
		}
	}
	if !v.Empty() {
		return &v
	}
	if join != "" {
		dir := join
		// Started from a subdirectory of the repo: the same subdirectory of
		// the shared folder.
		if prefix, err := sessionwt.Prefix(ctx, sess.Dir); err == nil && prefix != "" && dirExists(filepath.Join(join, prefix)) {
			dir = filepath.Join(join, prefix)
		}
		sess.Dir, sess.Isolation = dir, contracts.IsolationCurrentDir
	}
	return nil
}

// sameRepoAs reports whether the new session would work in the same repo
// as counterpart: the same workspace, or a Dir in the repo counterpart's
// worktree belongs to.
func (s *server) sameRepoAs(ctx context.Context, sess *contracts.Session, counterpart contracts.Session) bool {
	if sess.Workspace != "" && sess.Workspace == counterpart.Workspace {
		return true
	}
	return sess.Dir != "" && dirExists(counterpart.WorktreeDir) && sessionwt.SameRepo(ctx, sess.Dir, counterpart.WorktreeDir)
}

func dirExists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// createStagedLinks substitutes the placeholder with the new session's ID
// (into a copy — req.Links stays untouched for error reporting) and creates
// each link through the Data Service, where all remaining validation lives.
// Entries run sequentially; each create runs in its own tx and sees prior
// rows, so cross-entry conflicts fail correctly on the later entry. Returns
// the links created so far alongside any error, so the caller can roll them
// back.
func (s *server) createStagedLinks(ctx context.Context, sessionID string, reqs []contracts.LinkRequest) ([]contracts.Link, error) {
	created := make([]contracts.Link, 0, len(reqs))
	for _, lr := range reqs {
		if lr.GrantingSessionID == contracts.NewSessionPlaceholder {
			lr.GrantingSessionID = sessionID
		}
		if lr.SubjectSessionID == contracts.NewSessionPlaceholder {
			lr.SubjectSessionID = sessionID
		}
		link, err := s.data.CreateLink(ctx, lr)
		if err != nil {
			return created, err
		}
		created = append(created, link)
	}
	return created, nil
}

// rollbackStagedCreate best-effort deletes the created links, then the
// session, after a mid-create failure — session deletion deliberately does
// not cascade to links (see contracts.Link), so skipping the links would
// leave dangling rows that LinksForSession still returns. Uses a background
// context: the records still need cleaning up even if the request that asked
// for them is gone.
func (s *server) rollbackStagedCreate(links []contracts.Link, sessionID string) {
	ctx := context.Background()
	for _, l := range links {
		_ = s.data.DeleteLink(ctx, l.ID)
	}
	_ = s.data.DeleteSession(ctx, sessionID)
}
