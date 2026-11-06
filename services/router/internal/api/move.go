package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/drewkidwell/mads/pkg/changes"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// Moving a file the session changed outside every repo into one, so it can
// be committed (docs/COMMIT-LAND-PUSH-DESIGN.md D4):
//
//	POST /sessions/{id}/move           move it now
//	GET  /sessions/{id}/move/folders   folders in a repo to suggest
//
// The Router runs both itself: neither touches anything the agent's turn
// holds, and the agent hears about the move through a note owed to its
// next turn (Session.PendingNotes), not a turn of its own.

func moveRefusal(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, contracts.MoveError{Code: code, Error: msg})
}

func (s *server) handleSessionMove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess, err := s.data.Session(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	var req contracts.MoveRequest
	if !decode(w, r, &req) {
		return
	}
	src := filepath.Clean(req.Path)
	if !filepath.IsAbs(src) {
		moveRefusal(w, http.StatusBadRequest, contracts.MoveNotSessionFile, "The file must be named by its full path.")
		return
	}
	info, err := os.Lstat(src)
	if err != nil {
		moveRefusal(w, http.StatusConflict, contracts.MoveMissing, src+" isn't there any more.")
		return
	}
	if !info.Mode().IsRegular() {
		moveRefusal(w, http.StatusBadRequest, contracts.MoveNotSessionFile, src+" isn't a file.")
		return
	}
	ledger, err := s.data.FileChanges(ctx, sess.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !sessionChanged(sess, ledger, src) {
		moveRefusal(w, http.StatusBadRequest, contracts.MoveNotSessionFile, "This session didn't change "+src+".")
		return
	}
	if sessionwt.TopOf(ctx, src) != "" {
		moveRefusal(w, http.StatusConflict, contracts.MoveInRepo, src+" is already in a repo: commit it there.")
		return
	}
	top, ok := repoTop(ctx, req.Repo)
	if !ok {
		moveRefusal(w, http.StatusBadRequest, contracts.MoveUnknownRepo, req.Repo+" isn't a git repo.")
		return
	}
	dir, ok := folderIn(top, req.Dir)
	if !ok {
		moveRefusal(w, http.StatusBadRequest, contracts.MoveOutsideRepo, req.Dir+" isn't inside "+filepath.Base(top)+".")
		return
	}
	dest := filepath.Join(dir, filepath.Base(src))
	if _, err := os.Lstat(dest); err == nil {
		moveRefusal(w, http.StatusConflict, contracts.MoveExists, relTo(top, dest)+" already exists: rename it, or pick another folder.")
		return
	}
	if ignored(ctx, top, relTo(top, dest)) {
		moveRefusal(w, http.StatusConflict, contracts.MoveIgnored, filepath.Base(top)+" ignores "+relTo(top, dest)+", so it couldn't be committed there.")
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		moveRefusal(w, http.StatusInternalServerError, contracts.MoveFailed, err.Error())
		return
	}
	if err := moveFile(src, dest, info.Mode().Perm()); err != nil {
		moveRefusal(w, http.StatusInternalServerError, contracts.MoveFailed, err.Error())
		return
	}

	// The moved file is the session's: its own change in the repo now.
	if err := s.data.RecordFileChange(context.WithoutCancel(ctx), &contracts.FileChange{
		SessionID: sess.ID, Path: dest, Change: "added", Origin: contracts.OriginToolUse,
	}); err != nil {
		log.Printf("router: record moved file %s: %v", dest, err)
	}
	s.oweNote(context.WithoutCancel(ctx), sess.ID,
		fmt.Sprintf("[mads] The user moved %s to %s, in the %s repo. Use the new path from now on.", src, dest, filepath.Base(top)))
	if err := s.worktreeOps().Recount(context.WithoutCancel(ctx), sess.ID); err != nil {
		log.Printf("router: recount after move for %s: %v", sess.ID, err)
	}
	writeJSON(w, http.StatusOK, contracts.MoveResult{Path: dest})
}

// oweNote puts a note on the record for the agent's next turn, and tells a
// live child, which holds the record it read at start.
func (s *server) oweNote(ctx context.Context, id, note string) {
	if err := s.data.SetSessionNotes(ctx, id, contracts.SessionNotesPatch{Add: []string{note}}); err != nil {
		log.Printf("router: owe note to %s: %v", id, err)
	}
	if addr, ok := s.sup.SessionAddr(id); ok {
		if status, _, err := callChild(ctx, addr, http.MethodPost, "/notes", map[string]string{"note": note}); err != nil || status >= 300 {
			log.Printf("router: tell %s's child about a note: %d %v", id, status, err)
		}
	}
}

// sessionChanged reports whether the session's own tools changed path.
func sessionChanged(sess contracts.Session, ledger []contracts.FileChange, path string) bool {
	folder := changes.SessionFolder(sess)
	want := shadow.RealPath(path)
	for _, fc := range ledger {
		if fc.TaskID == "" && fc.Attributed && changes.LedgerPath(fc, folder) == want {
			return true
		}
	}
	return false
}

// repoTop is root as the top of a git working tree, if it is one.
func repoTop(ctx context.Context, root string) (string, bool) {
	if root == "" || !filepath.IsAbs(root) {
		return "", false
	}
	top, err := sessionwt.Toplevel(ctx, root)
	if err != nil || shadow.RealPath(top) != shadow.RealPath(root) {
		return "", false
	}
	return top, true
}

// folderIn is rel as a folder inside top, with any part of it that already
// exists resolved through its symlinks; ok false when it leads out.
func folderIn(top, rel string) (string, bool) {
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == "." {
		return top, true
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	dir := filepath.Join(top, rel)
	if within(shadow.RealPath(top), shadow.RealPath(dir)) {
		return dir, true
	}
	return "", false
}

func relTo(top, p string) string {
	if rel, err := filepath.Rel(top, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return p
}

// ignored reports whether the repo at top ignores rel.
func ignored(ctx context.Context, top, rel string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", top, "check-ignore", "-q", "--no-index", "--", rel)
	return cmd.Run() == nil
}

// moveFile renames src to dest, copying across filesystems.
func moveFile(src, dest string, perm os.FileMode) error {
	err := os.Rename(src, dest)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dest)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return os.Remove(src)
}

// maxMoveFolders caps the folder suggestions.
const maxMoveFolders = 500

// handleSessionMoveFolders lists the folders in a repo a file could move
// into: every folder holding files git doesn't ignore (git ls-files, so
// build output and node_modules never show), those already holding files
// with the extension ext first.
func (s *server) handleSessionMoveFolders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := s.data.Session(ctx, r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	top, ok := repoTop(ctx, r.URL.Query().Get("repo"))
	if !ok {
		moveRefusal(w, http.StatusBadRequest, contracts.MoveUnknownRepo, "Not a git repo.")
		return
	}
	ext := strings.ToLower(r.URL.Query().Get("ext"))
	out, err := sessionwt.Git(ctx, top, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "list folders: ", err)
		return
	}
	matches := map[string]int{}
	for _, f := range strings.Split(out, "\x00") {
		if f == "" {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(f))
		hit := ext != "" && strings.ToLower(filepath.Ext(f)) == ext
		for d := dir; d != "." && d != "/"; d = filepath.ToSlash(filepath.Dir(d)) {
			if _, seen := matches[d]; !seen {
				matches[d] = 0
			}
			if hit && d == dir {
				matches[d]++
			}
		}
	}
	folders := make([]contracts.MoveFolder, 0, len(matches))
	for d, n := range matches {
		folders = append(folders, contracts.MoveFolder{Path: d + "/", Matches: n})
	}
	sort.Slice(folders, func(i, j int) bool {
		if folders[i].Matches != folders[j].Matches {
			return folders[i].Matches > folders[j].Matches
		}
		return folders[i].Path < folders[j].Path
	})
	if len(folders) > maxMoveFolders {
		folders = folders[:maxMoveFolders]
	}
	writeJSON(w, http.StatusOK, folders)
}

// moveTargets are the repos a file outside every repo can move into: the
// plan's repos, those directly inside a session folder that isn't one, and
// the session's workspace's repos.
func (s *server) moveTargets(ctx context.Context, sess contracts.Session, plan contracts.CommitPlan) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(root string) {
		if top, ok := repoTop(ctx, root); ok && !seen[shadow.RealPath(top)] {
			seen[shadow.RealPath(top)] = true
			out = append(out, top)
		}
	}
	for _, r := range plan.Repos {
		add(r.Root)
	}
	if dir := sess.Dir; dir != "" && filepath.IsAbs(dir) && dirStatus(ctx, dir) == contracts.DirStatusPlain {
		for _, repo := range childRepos(dir) {
			add(repo)
		}
	}
	if sess.Workspace != "" {
		if ws, err := s.data.Workspace(ctx, sess.Workspace); err == nil {
			for _, r := range ws.Repos {
				add(r.AbsPath())
			}
		}
	}
	return out
}
