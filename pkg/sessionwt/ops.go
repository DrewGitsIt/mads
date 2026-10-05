package sessionwt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// Store is the part of the Data Service the session operations use.
// *dataclient.Client satisfies it; each caller passes its own, so events
// and records attribute to whichever process ran the operation.
type Store interface {
	Session(ctx context.Context, id string) (contracts.Session, error)
	FileChanges(ctx context.Context, sessionID string) ([]contracts.FileChange, error)
	RecordCommit(ctx context.Context, rep *dataclient.CommitReport) (contracts.Commit, error)
	DeleteCommit(ctx context.Context, sessionID, hash string) error
	SetSessionWorktree(ctx context.Context, id string, patch contracts.SessionWorktreePatch) error
	Append(ctx context.Context, env *contracts.Envelope) error
}

// Ops runs the session's Commit and Land actions end to end (D3): load the
// record and change list, take the locks, ensure the folder, do the git
// work, record what it made on the Data Service, publish events and the
// header counts. The live session child and the Router (for an inactive
// session) run the same Ops; only the caller-known Gates differ.
type Ops struct {
	Store Store
	// Refresh recomputes and publishes the header counts (D4) after a
	// change. Nil computes them here from the record (RefreshCounts); the
	// session child passes its own, which serializes with its other record
	// writes.
	Refresh func(ctx context.Context) error
}

func (o Ops) load(ctx context.Context, sessionID string) (contracts.Session, Target, []contracts.FileChange, error) {
	sess, err := o.Store.Session(ctx, sessionID)
	if err != nil {
		return contracts.Session{}, Target{}, nil, err
	}
	t, err := TargetFor(ctx, sess)
	if err != nil {
		return sess, Target{}, nil, err
	}
	rows, err := o.Store.FileChanges(ctx, sessionID)
	if err != nil {
		return sess, Target{}, nil, fmt.Errorf("load session files: %w", err)
	}
	return sess, t, rows, nil
}

// Status is GET /sessions/{id}/worktree. A session with no git folder
// reads as not isolated with nothing to commit, and Land blocked.
func (o Ops) Status(ctx context.Context, sessionID string, g Gates) (contracts.WorktreeStatus, error) {
	_, t, rows, err := o.load(ctx, sessionID)
	if errors.Is(err, ErrNoFolder) {
		return contracts.WorktreeStatus{
			Files:       []contracts.WorktreeFile{},
			LandBlocked: &contracts.LandRefusal{Reason: contracts.LandNotIsolated, Detail: "This session has no git folder."},
		}, nil
	}
	if err != nil {
		return contracts.WorktreeStatus{}, err
	}
	return Status(ctx, t, rows, g)
}

// SelectForDraft resolves a draft request's paths exactly as Commit
// would, returning the folder and the uncommitted files among them.
func (o Ops) SelectForDraft(ctx context.Context, sessionID string, paths []string) (folder string, files []string, err error) {
	_, t, rows, err := o.load(ctx, sessionID)
	if errors.Is(err, ErrNoFolder) {
		return "", nil, refuseCommit(contracts.CommitNoFolder, "This session has no git folder to commit in.")
	}
	if err != nil {
		return "", nil, err
	}
	valid, results, ok := SelectPaths(t, rows, paths)
	if !ok {
		r := refuseCommit(contracts.CommitInvalidPaths, "Some of the selected files can't be committed from this session.")
		r.Body.Paths, r.Body.Selection = results, paths
		return "", nil, r
	}
	changed, err := changes(ctx, t.Folder, valid)
	if err != nil {
		return "", nil, err
	}
	for _, f := range changed {
		files = append(files, f.Path)
	}
	if len(files) == 0 {
		r := refuseCommit(contracts.CommitNothingToCommit, "Nothing to commit: the selected files have no changes git can see.")
		r.Body.Selection = paths
		return "", nil, r
	}
	return t.Folder, files, nil
}

// Commit is POST /sessions/{id}/commit with a message already in hand (a
// draft, if any, was written before). Refusals are *CommitRefusal.
func (o Ops) Commit(ctx context.Context, sessionID string, req contracts.CommitRequest) (contracts.CommitResult, error) {
	_, t, rows, err := o.load(ctx, sessionID)
	if errors.Is(err, ErrNoFolder) {
		return contracts.CommitResult{}, refuseCommit(contracts.CommitNoFolder, "This session has no git folder to commit in.")
	}
	if err != nil {
		return contracts.CommitResult{}, err
	}
	var res contracts.CommitResult
	err = WithSessionLock(ctx, t.Primary, sessionID, func() error {
		if err := o.ensure(ctx, t); err != nil {
			return err
		}
		res, err = CommitFiles(ctx, t, rows, req)
		return err
	})
	if err != nil {
		return contracts.CommitResult{}, err
	}
	res.Drafted = req.Drafted
	o.recordAs(ctx, t, res.Hash, res.Message, provenance{
		source: contracts.CommitSourceCommit, files: committedCount(res.Paths), drafted: req.Drafted,
	})
	o.refresh(ctx, t)
	return res, nil
}

// committedCount is how many paths a Commit actually took.
func committedCount(paths []contracts.CommitPath) int {
	n := 0
	for _, p := range paths {
		if p.Status == contracts.CommitPathCommitted {
			n++
		}
	}
	return n
}

// Undo is POST /sessions/{id}/commit/undo. An undone commit's record goes
// too (the Data Service appends commit.undone).
func (o Ops) Undo(ctx context.Context, sessionID, hash string) (contracts.UndoCommitResult, error) {
	_, t, _, err := o.load(ctx, sessionID)
	if errors.Is(err, ErrNoFolder) {
		return contracts.UndoCommitResult{Hash: hash, Reason: "no_folder", Detail: "This session has no git folder."}, nil
	}
	if err != nil {
		return contracts.UndoCommitResult{}, err
	}
	var res contracts.UndoCommitResult
	err = WithSessionLock(ctx, t.Primary, sessionID, func() error {
		var err error
		res, err = UndoCommit(ctx, t, hash)
		return err
	})
	if err != nil || !res.Undone {
		return res, err
	}
	if err := o.Store.DeleteCommit(ctx, sessionID, res.Hash); err != nil && !errors.Is(err, dataclient.ErrNotFound) {
		log.Printf("sessionwt: remove undone commit %s: %v", res.Hash, err)
	}
	o.refresh(ctx, t)
	return res, nil
}

// Land is POST /sessions/{id}/land: the gates, then LandSteps, under the
// session lock (LandSteps adds the repo lock for step 2). Every outcome is
// a LandResult and a conversation event: merge.refused for a gate or a
// refusing checkout, else merge.attempted then merge.completed or
// merge.conflicted.
func (o Ops) Land(ctx context.Context, sessionID string, g Gates) (contracts.LandResult, error) {
	_, t, _, err := o.load(ctx, sessionID)
	if errors.Is(err, ErrNoFolder) {
		t = Target{SessionID: sessionID}
		err = nil
	}
	if err != nil {
		return contracts.LandResult{}, err
	}
	if !t.Isolated() {
		r, _ := LandGate(ctx, t, g, 0, false)
		return o.refused(ctx, t, r), nil
	}

	var res contracts.LandResult
	attempted := false
	err = WithSessionLock(ctx, t.Primary, sessionID, func() error {
		if err := o.ensure(ctx, t); err != nil {
			return err
		}
		unlanded, err := CountUnlanded(ctx, t.Primary, t.Base, t.Branch)
		if err != nil {
			return err
		}
		refusal, err := LandGate(ctx, t, g, unlanded, true)
		if err != nil {
			return err
		}
		if refusal != nil {
			res = o.refused(ctx, t, refusal)
			return nil
		}
		o.event(ctx, t, contracts.MergeAttempted, nil)
		attempted = true
		res, err = LandSteps(ctx, t)
		return err
	})
	if err != nil {
		return contracts.LandResult{}, err
	}
	if !attempted {
		return res, nil
	}
	switch res.Outcome {
	case contracts.LandLanded:
		o.recordAs(ctx, t, res.MergeCommit, "Land "+landName(t), provenance{source: contracts.CommitSourceLand})
		o.event(ctx, t, contracts.MergeCompleted, map[string]any{
			"merge_commit": res.MergeCommit, "fast_forwarded": res.FastForwarded,
		})
	case contracts.LandConflicted:
		o.event(ctx, t, contracts.MergeConflicted, map[string]any{"files": res.Files, "step": "base"})
	case contracts.LandRefused:
		o.refused(ctx, t, &contracts.LandRefusal{Reason: res.Reason, Detail: res.Detail, Files: res.Files})
	}
	o.refresh(ctx, t)
	return res, nil
}

// refused appends merge.refused and returns the refusal as Land's result.
func (o Ops) refused(ctx context.Context, t Target, r *contracts.LandRefusal) contracts.LandResult {
	p := map[string]any{"reason": r.Reason}
	if r.Detail != "" {
		p["detail"] = r.Detail
	}
	if len(r.Files) > 0 {
		p["files"] = r.Files
	}
	o.event(ctx, t, contracts.MergeRefused, p)
	return contracts.LandResult{
		Outcome: contracts.LandRefused, Branch: t.Branch, Base: t.Base,
		Reason: r.Reason, Detail: r.Detail, Files: r.Files,
	}
}

// event appends one of Land's merge.* events with the common fields.
// Best-effort: the git work is done whatever the event log says.
func (o Ops) event(ctx context.Context, t Target, typ contracts.EventType, extra map[string]any) {
	p := map[string]any{"kind": "land"}
	for k, v := range map[string]string{"branch": t.Branch, "base": t.Base, "target_branch": t.Base} {
		if v != "" {
			p[k] = v
		}
	}
	for k, v := range extra {
		p[k] = v
	}
	raw, _ := json.Marshal(p)
	if err := o.Store.Append(context.WithoutCancel(ctx), &contracts.Envelope{
		Type: typ, SessionID: t.SessionID, Payload: raw,
	}); err != nil {
		log.Printf("sessionwt: append %s: %v", typ, err)
	}
}

// ensure makes an isolated session's folder usable before Commit or Land
// touch it (D2), and says so with worktree.ensured when it had to repair
// anything. Caller holds the session lock.
func (o Ops) ensure(ctx context.Context, t Target) error {
	if !t.Isolated() {
		return nil
	}
	res, err := Ensure(ctx, Spec{Primary: t.Primary, Root: t.Root, SessionID: t.SessionID, Base: t.Base})
	if err != nil {
		return fmt.Errorf("ensure session folder: %w", err)
	}
	if res.Notable() {
		raw, _ := json.Marshal(res.Payload(t.Base))
		if err := o.Store.Append(ctx, &contracts.Envelope{
			Type: contracts.WorktreeEnsured, SessionID: t.SessionID, Payload: raw,
		}); err != nil {
			log.Printf("sessionwt: append worktree.ensured: %v", err)
		}
	}
	return nil
}

// provenance says which of this package's own operations made a recorded
// commit (dataclient.CommitReport.Source and friends); zero for none.
type provenance struct {
	source  string
	files   int
	drafted bool
}

// recordAs posts a commit this package made to the Data Service (D5, D6
// step 3, D8), with its provenance: the hook can't be relied on for these —
// it is silenced for Commit and a pipeline's save, and never fires for a
// merge. Best-effort; the commit exists either way.
func (o Ops) recordAs(ctx context.Context, t Target, hash, message string, p provenance) {
	if hash == "" {
		return
	}
	dir := t.Folder
	if !exists(dir) {
		dir = t.Primary
	}
	repo, err := RepoOf(ctx, dir)
	if err != nil {
		log.Printf("sessionwt: resolve repo for commit %s: %v", hash, err)
	}
	if _, err := o.Store.RecordCommit(context.WithoutCancel(ctx), &dataclient.CommitReport{
		Commit: contracts.Commit{Hash: hash, SessionID: t.SessionID, Repo: repo, Message: Subject(message)},
		Source: p.source, Files: p.files, Drafted: p.drafted,
	}); err != nil {
		log.Printf("sessionwt: record commit %s: %v", hash, err)
	}
}

func (o Ops) refresh(ctx context.Context, t Target) {
	ctx = context.WithoutCancel(ctx)
	var err error
	if o.Refresh != nil {
		err = o.Refresh(ctx)
	} else {
		err = RefreshCounts(ctx, o.Store, t)
	}
	if err != nil {
		log.Printf("sessionwt: refresh counts for %s: %v", t.SessionID, err)
	}
}

// RefreshCounts recomputes a session's header counts (D4) from its record
// and change list and writes them through the scoped worktree patch — the
// Router's way, for a session with no live child to do it.
func RefreshCounts(ctx context.Context, store Store, t Target) error {
	c := Counts{}
	if exists(t.Folder) {
		rows, err := store.FileChanges(ctx, t.SessionID)
		if err != nil {
			return err
		}
		if c.Uncommitted, err = CountUncommitted(ctx, t.Folder, SessionFiles(t.Folder, rows)); err != nil {
			return err
		}
	}
	if t.Isolated() {
		var err error
		if c.Unlanded, err = CountUnlanded(ctx, t.Primary, t.Base, t.Branch); err != nil {
			return err
		}
	}
	return store.SetSessionWorktree(ctx, t.SessionID, contracts.SessionWorktreePatch{
		UncommittedCount: &c.Uncommitted, UnlandedCount: &c.Unlanded,
	})
}

// Cleanup removes a deleted session's worktree and branch (D7), under the
// session lock, then the lock file itself. Best-effort and logged: it
// never blocks the delete, and does nothing for a session that was never
// isolated. Call it only once the session's child is gone — git removes a
// worktree even with a process working inside it.
func Cleanup(ctx context.Context, sess contracts.Session) {
	if sess.WorktreeDir == "" {
		return
	}
	repo := ""
	for _, dir := range []string{sess.Dir, sess.WorktreeDir} {
		if implicit(dir) || !exists(dir) {
			continue
		}
		if main, err := mainWorktree(ctx, dir); err == nil && main != "" {
			repo = main
			break
		}
	}
	if repo == "" {
		log.Printf("sessionwt: cleanup %s: no repository left to remove its worktree from", sess.ID)
		return
	}
	branch := sess.WorktreeBranch
	if branch == "" {
		branch = Branch(sess.ID)
	}
	err := WithSessionLock(ctx, repo, sess.ID, func() error {
		// Twice --force: removes it even if locked, as a deleted session's
		// folder may be.
		if out, err := Git(ctx, repo, "worktree", "remove", "--force", "--force", sess.WorktreeDir); err != nil && exists(sess.WorktreeDir) {
			log.Printf("sessionwt: cleanup %s: remove worktree: %s", sess.ID, gitOutput(out, err))
		}
		_, _ = Git(ctx, repo, "worktree", "prune")
		if refExists(ctx, repo, "refs/heads/"+branch) {
			if out, err := Git(ctx, repo, "branch", "-D", branch); err != nil {
				log.Printf("sessionwt: cleanup %s: delete branch: %s", sess.ID, gitOutput(out, err))
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("sessionwt: cleanup %s: %v", sess.ID, err)
		return
	}
	if p, err := SessionLockPath(ctx, repo, sess.ID); err == nil {
		_ = os.Remove(p)
	}
}
