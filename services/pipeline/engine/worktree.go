package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/githook"
)

// worktrees owns per-task isolation (spec §6, §17): worktree + branch per
// task, task context in per-worktree config for the hook, a single land
// attempt (rebase → gate → merge, spec's "Not Rocket Science Rule") at a
// time back into the primary, cleanup after a clean merge.
type worktrees struct {
	primary   string // where task branches fork from and merge into: the workspace repo, or an isolated session's folder
	repo      string // the user's own checkout, where repo-wide setup (hook, merge driver) is written; == primary unless merging into a session folder
	root      string // where task worktrees live
	dataURL   string
	sessionID string
	data      *dataclient.Client // task lookups for conflict diagnostics

	// mergirafPath is the mergiraf binary's path, or "" if unavailable — set
	// once at engine start (Config.MergirafPath or a PATH lookup). Rebase/
	// merge behavior is otherwise identical either way: this only gates
	// whether ensureSetup registers mergiraf as the primary repo's merge
	// driver (spec: "detect availability at engine start and fall back
	// cleanly to plain git rebase when absent").
	mergirafPath string

	setup   sync.Once
	setupEr error
	land    landQueue // exactly one land attempt at a time, stable FIFO order

	forkMu sync.Mutex
	forks  map[string]string // task ID -> trunk HEAD at worktree creation (the fork point)
}

// setFork records taskID's fork point — trunk HEAD as of worktree creation —
// so a later land attempt's conflict/gate-failure attribution can name
// exactly what landed on trunk since this task started (spec: "deterministic
// pairwise attribution").
func (w *worktrees) setFork(taskID, sha string) {
	w.forkMu.Lock()
	defer w.forkMu.Unlock()
	if w.forks == nil {
		w.forks = map[string]string{}
	}
	w.forks[taskID] = sha
}

// fork returns taskID's recorded fork point, or "" if none was recorded
// (e.g. a worktrees value built directly by a test without going through
// create).
func (w *worktrees) fork(taskID string) string {
	w.forkMu.Lock()
	defer w.forkMu.Unlock()
	return w.forks[taskID]
}

// git runs one git command in dir.
func (w *worktrees) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Identity for engine safety commits, without touching user config.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=mads-pipeline", "GIT_AUTHOR_EMAIL=pipeline@mads.local",
		"GIT_COMMITTER_NAME=mads-pipeline", "GIT_COMMITTER_EMAIL=pipeline@mads.local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v\nstderr: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ensureSetup enables per-worktree config and installs the post-commit hook,
// and — when a mergiraf binary was detected — registers it as the git merge
// driver for the shared repo, once per run. Both writes land in the shared
// primary repo's .git/config (via `git config`, which takes its own
// config.lock with no retry), so a second engine process racing this same
// one-time setup against the same workspace can otherwise fail outright
// instead of just waiting its turn — hence the same cross-process
// workspace lock the land queue uses, not just this process's sync.Once.
func (w *worktrees) ensureSetup(ctx context.Context) error {
	w.setup.Do(func() {
		release, err := acquireWorkspaceLock(ctx, w)
		if err != nil {
			w.setupEr = err
			return
		}
		defer release()
		// The main checkout, not primary: githook resolves the hooks
		// directory by joining dir and --git-common-dir, which is absolute
		// (and so joins to nonsense) when dir is a linked worktree such as
		// a session folder.
		if w.setupEr = githook.EnsureInstalled(ctx, w.setupDir()); w.setupEr != nil {
			return
		}
		if w.mergirafPath != "" {
			w.setupEr = w.ensureMergirafDriver(ctx)
		}
	})
	return w.setupEr
}

// setupDir is where ensureSetup writes repo-wide config: the user's checkout
// (repo), falling back to primary for a worktrees value built without one.
func (w *worktrees) setupDir() string {
	if w.repo != "" {
		return w.repo
	}
	return w.primary
}

// mergirafDriverName is the git merge driver name mergiraf is registered
// under (see ensureMergirafDriver).
const mergirafDriverName = "mergiraf"

// ensureMergirafDriver registers mergiraf as a git merge driver (spec: the
// land queue's rebase step should resolve adjacent-edit conflicts
// automatically while leaving genuinely overlapping edits conflicted).
// This is git's native per-file merge-driver mechanism — a
// `merge.mergiraf.driver` config command template plus a `merge=mergiraf`
// gitattributes rule — not a bespoke whole-tree invocation: mergiraf's own
// docs (mergiraf.org/usage.html) prescribe exactly this integration, and it
// means rebaseBeforeMerge's plain `git merge` calls pick it up for free.
//
// extensions.worktreeConfig (enabled by githook.EnsureInstalled above) makes
// git config and .git/info/attributes shared across every task worktree, so
// this only needs to run once against the primary repo's common git dir.
//
// The %S/%X/%Y label placeholders are only substituted by git 2.44+; on
// older git they pass through to mergiraf as literal text, which is
// harmless (mergiraf just uses them as conflict-marker labels on any
// hunk it can't resolve) — see mergiraf.org/usage.html#with-a-driver.
func (w *worktrees) ensureMergirafDriver(ctx context.Context) error {
	dir := w.setupDir()
	if _, err := w.git(ctx, dir, "config", "merge.mergiraf.name", "mergiraf"); err != nil {
		return err
	}
	driver := w.mergirafPath + " merge --git %O %A %B -s %S -x %X -y %Y -p %P -l %L"
	if _, err := w.git(ctx, dir, "config", "merge.mergiraf.driver", driver); err != nil {
		return err
	}
	gitDir, err := w.git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	gitDir = strings.TrimSpace(gitDir)
	// --git-common-dir is relative to w.primary (typically ".git") when
	// w.primary is a normal checkout, but ABSOLUTE — pointing at the main
	// worktree's .git, elsewhere on disk entirely — when w.primary is itself
	// a linked worktree (e.g. the pipeline's own workspace was created via
	// `git worktree add`). Joining an absolute gitDir onto w.primary would
	// silently produce a bogus nested path that git never reads, making this
	// registration a no-op without ever returning an error.
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return err
	}
	attrPath := filepath.Join(infoDir, "attributes")
	if raw, err := os.ReadFile(attrPath); err == nil && strings.Contains(string(raw), "merge="+mergirafDriverName) {
		return nil
	}
	f, err := os.OpenFile(attrPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString("* merge=mergiraf\n")
	return err
}

// create makes the task's worktree and branch, records the fork point (trunk
// HEAD right now, before the worktree exists to diverge from it), and stamps
// the task context into per-worktree config for the hook.
func (w *worktrees) create(ctx context.Context, task contracts.Task) (dir, branch string, err error) {
	if err := w.ensureSetup(ctx); err != nil {
		return "", "", err
	}
	branch = "mads/" + task.ID
	dir = filepath.Join(w.root, task.ID)
	// A reclaimed task (reap-on-launch) may still have its dead run's
	// worktree/branch on disk; discard both so `worktree add` starts
	// clean instead of failing on an existing path/ref. Harmless no-op
	// (remove already discards its own errors) when there's nothing to
	// remove — the common case.
	w.remove(ctx, dir, branch)
	head, err := w.git(ctx, w.primary, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	if _, err := w.git(ctx, w.primary, "worktree", "add", "-b", branch, dir); err != nil {
		return "", "", err
	}
	w.setFork(task.ID, strings.TrimSpace(head))
	if err := githook.SetWorktreeConfig(ctx, dir, githook.Config{
		TaskID:    task.ID,
		SessionID: w.sessionID,
		DataURL:   w.dataURL,
	}); err != nil {
		return "", "", err
	}
	return dir, branch, nil
}

// commitStray commits any uncommitted agent work so nothing is ever lost at
// merge time; the trailer keeps the commit linked to its task (spec §3, §5).
func (w *worktrees) commitStray(ctx context.Context, dir string, task contracts.Task) error {
	status, err := w.git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}
	if _, err := w.git(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	msg := fmt.Sprintf("%s (pipeline auto-commit)\n\nTask: %s", task.Title, task.ID)
	_, err = w.git(ctx, dir, "commit", "-m", msg)
	return err
}

// merge brings the task branch into the primary. mergeCmd comes from the
// template (e.g. "git merge --no-ff"); the engine supplies the branch and
// message. By the time this runs, rebaseBeforeMerge has already reconciled
// the branch with trunk and the gate has already passed, both under the same
// land-queue turn, so a conflict here is a rare defensive fallback rather
// than the primary path — it still aborts cleanly and returns the conflicted
// files (with the same structured attribution as rebaseBeforeMerge) as the
// error (spec §6).
func (w *worktrees) merge(ctx context.Context, mergeCmd string, task contracts.Task, branch, trunkHead string) error {
	// Two -m flags make subject + body paragraphs — %q on a multiline
	// string would bake literal \n into the subject and break the trailer.
	subject := fmt.Sprintf("Merge %s: %s", branch, task.Title)
	// mergeCmd is the template's own words (e.g. "git merge --no-ff"), never
	// task data — safe to split on whitespace. branch/subject/task.ID are
	// task-derived and must stay separate argv elements, never
	// shell-composed (a backtick or $() in a task title must never reach a
	// shell).
	args := append(strings.Fields(mergeCmd)[1:], branch, "-m", subject, "-m", "Task: "+task.ID)
	if _, err := w.git(ctx, w.primary, args...); err != nil {
		conflicts, _ := w.git(ctx, w.primary, "diff", "--name-only", "--diff-filter=U")
		files := splitLines(conflicts)
		var hunks []ConflictHunk
		if len(files) > 0 {
			// Attribution must read the conflict markers before the abort
			// below discards them.
			hunks = w.attributeConflict(ctx, w.primary, files, trunkHead)
		}
		_, _ = w.git(ctx, w.primary, "merge", "--abort")
		if len(files) == 0 {
			return fmt.Errorf("merge failed: %w", err)
		}
		ids := w.landedTaskIDs(ctx, trunkHead)
		return &ConflictError{
			Files: files, Hunks: hunks, LandedTaskIDs: ids,
			msg: fmt.Sprintf("merge conflict with the current trunk in:\n%s\n\nthe merge was aborted; the task's work is intact on branch %s — resolve by rebasing that branch or adjusting the conflicting tasks", strings.Join(files, "\n"), branch),
		}
	}
	return nil
}

// rebaseBeforeMerge merges current trunk (primary HEAD) into the branch,
// inside the task's own worktree (dir), before the trunk-side merge — so
// "trunk moved under me" is resolved here instead of surfacing as a conflict
// against the primary. Called under the land queue, same as the trunk merge
// itself. trunkHead is the CURRENT primary HEAD as of this call — captured
// fresh on every land attempt, never cached, so a task's gate always runs
// against (and only ever lands against) the trunk state named here.
func (w *worktrees) rebaseBeforeMerge(ctx context.Context, dir string, task contracts.Task) (rebased bool, trunkHead string, err error) {
	head, err := w.git(ctx, w.primary, "rev-parse", "HEAD")
	if err != nil {
		return false, "", err
	}
	trunkHead = strings.TrimSpace(head)

	// A previous loop_back attempt may have left a merge in progress here
	// (a merge_resolve agent stage edited files but the resolution wasn't
	// finished). Don't re-invoke `git merge` on top of that — either finish
	// it mechanically (nothing left unmerged) or re-surface the same
	// conflict.
	if _, err := w.git(ctx, dir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		conflicts, _ := w.git(ctx, dir, "diff", "--name-only", "--diff-filter=U")
		if strings.TrimSpace(conflicts) != "" {
			return false, trunkHead, w.conflictErr(ctx, dir, conflicts, trunkHead)
		}
		if _, err := w.git(ctx, dir, "commit", "--no-edit"); err != nil {
			return false, trunkHead, fmt.Errorf("rebase-before-merge: finishing the resolved merge: %v", err)
		}
		return true, trunkHead, nil
	}

	if _, err := w.git(ctx, dir, "merge-base", "--is-ancestor", trunkHead, "HEAD"); err == nil {
		return false, trunkHead, nil // trunk hasn't moved since this branch last caught up: a non-event
	}

	subject := fmt.Sprintf("Merge trunk into %s (pipeline rebase-before-merge)", task.ID)
	if _, err := w.git(ctx, dir, "merge", trunkHead, "-m", subject); err == nil {
		return true, trunkHead, nil
	}
	conflicts, _ := w.git(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if strings.TrimSpace(conflicts) == "" {
		return false, trunkHead, fmt.Errorf("rebase-before-merge: merging trunk into the branch failed")
	}
	return false, trunkHead, w.conflictErr(ctx, dir, conflicts, trunkHead)
}

// ConflictHunk names one conflicting region: the file, its line range on
// trunk (best-effort — 0 if the conflicting text couldn't be located
// verbatim in the trunk blob), and the trunk-side commit/task attributed to
// it via git blame (spec: "git blame on trunk's side of each conflicting
// hunk names the exact landed commit -> task").
type ConflictHunk struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Commit    string `json:"commit,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
}

// ConflictError carries a rebase/merge conflict's structured pairwise
// attribution alongside the human-readable message every existing caller
// already surfaces via Error() (blocker notes, loop-back feedback) — the
// engine's merge.conflicted event reads the structured fields directly
// instead of re-parsing prose (spec: "this attribution payload is an
// acceptance criterion, not a nice-to-have").
type ConflictError struct {
	Files         []string
	Hunks         []ConflictHunk
	LandedTaskIDs []string
	msg           string
}

func (e *ConflictError) Error() string { return e.msg }

// conflictErr reports a rebase-before-merge conflict: the conflicted files
// plus the trunk-side tasks whose merges introduced the conflicting changes.
// The conflict state (markers, MERGE_MSG) is left in dir for a merge_resolve
// stage or manual resolution — no `merge --abort` here.
func (w *worktrees) conflictErr(ctx context.Context, dir, conflicts, trunkHead string) error {
	files := splitLines(conflicts)
	hunks := w.attributeConflict(ctx, dir, files, trunkHead)
	base, _ := w.git(ctx, dir, "merge-base", "HEAD", trunkHead)
	rangeSpec := trunkHead
	if b := strings.TrimSpace(base); b != "" {
		rangeSpec = b + ".." + trunkHead
	}
	landed := w.tasksLandedSince(ctx, rangeSpec)
	ids := make([]string, len(landed))
	for i, t := range landed {
		ids[i] = t.id
	}
	return &ConflictError{
		Files: files, Hunks: hunks, LandedTaskIDs: ids,
		msg: fmt.Sprintf(
			"rebase-before-merge: trunk has moved and conflicts with this branch's changes in:\n%s\n\n%s\n\nthe worktree merge was left unresolved (conflict markers and MERGE_MSG intact) for a merge_resolve stage or manual resolution",
			strings.Join(files, "\n"), formatLandedTasks("conflicting trunk-side task(s) since this branch's base", landed)),
	}
}

// gateFailureErr formats a post-rebase gate failure's block note. Both trunk
// and the task were independently green (the task passed its own gate at
// fork; trunk was green before this land attempt), so the failure's suspect
// set is exactly what landed on trunk between fork and this attempt — named
// explicitly here rather than requiring bisection (spec: "serial keeps the
// suspect set minimal by construction").
func (w *worktrees) gateFailureErr(ctx context.Context, forkSHA, trunkHead string, gerr error) error {
	forkSHA = strings.TrimSpace(forkSHA)
	if forkSHA == "" {
		return fmt.Errorf("gates passed at fork <unrecorded>, failed at trunk %s:\n%s", trunkHead, gerr)
	}
	if forkSHA == trunkHead {
		return fmt.Errorf("gates passed at fork %s, failed with trunk unchanged since fork — the regression is in this task's own work:\n%s", forkSHA, gerr)
	}
	landed := w.tasksLandedSince(ctx, forkSHA+".."+trunkHead)
	return fmt.Errorf("gates passed at fork %s, failed after rebasing over %s\n%s",
		forkSHA, formatLandedTasks("the task(s) landed on trunk since fork", landed), gerr)
}

// landedTaskIDs is landedTasksSince's IDs alone, over fork..trunkHead where
// fork is trunkHead's parent along first-parent history back to the merge
// base with the working tree — used by merge's defensive conflict path where
// only trunkHead (not a separate fork SHA) is available.
func (w *worktrees) landedTaskIDs(ctx context.Context, trunkHead string) []string {
	landed := w.tasksLandedSince(ctx, trunkHead)
	ids := make([]string, len(landed))
	for i, t := range landed {
		ids[i] = t.id
	}
	return ids
}

// landedTask is one trunk-side commit range's attributed task, best-effort:
// a Data Service lookup failure degrades the message, never the caller's
// actual failure path.
type landedTask struct {
	id    string
	title string
	desc  string
	err   string
}

// tasksLandedSince names the tasks whose merges landed the trunk commits in
// rangeSpec, via the Task: trailer worktrees.merge stamps on every merge
// commit (spec §5).
func (w *worktrees) tasksLandedSince(ctx context.Context, rangeSpec string) []landedTask {
	out, err := w.git(ctx, w.primary, "log", rangeSpec, "--format=%(trailers:key=Task,valueonly)")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var tasks []landedTask
	for _, line := range strings.Split(out, "\n") {
		id := strings.TrimSpace(line)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		lt := landedTask{id: id}
		if t, err := w.data.Task(ctx, id); err != nil {
			lt.err = err.Error()
		} else {
			lt.title, lt.desc = t.Title, t.Description
		}
		tasks = append(tasks, lt)
	}
	return tasks
}

// formatLandedTasks renders tasksLandedSince for a blocker note or loop-back
// feedback, under a caller-supplied intro naming what the range represents
// (conflict attribution vs. gate-failure suspect set).
func formatLandedTasks(intro string, tasks []landedTask) string {
	if len(tasks) == 0 {
		return intro + ": none identified (no Task trailer on the trunk commits in range)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n", intro)
	for _, t := range tasks {
		if t.err != "" {
			fmt.Fprintf(&b, "- %s (could not fetch task: %s)\n", t.id, t.err)
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", t.id, t.title)
		if t.desc != "" {
			fmt.Fprintf(&b, "  %s\n", strings.ReplaceAll(t.desc, "\n", "\n  "))
		}
	}
	return b.String()
}

// attributeConflict maps each conflicting file's "incoming" (trunk-side)
// hunks to the trunk commit that introduced them, via git blame — best
// effort: a hunk whose content can't be located verbatim in the trunk blob
// (e.g. it was itself later rebased/rewritten) degrades to a bare
// file-scoped ConflictHunk rather than failing the caller.
func (w *worktrees) attributeConflict(ctx context.Context, dir string, files []string, trunkHead string) []ConflictHunk {
	var hunks []ConflictHunk
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			hunks = append(hunks, ConflictHunk{File: file})
			continue
		}
		blocks := theirsBlocks(string(raw))
		if len(blocks) == 0 {
			hunks = append(hunks, ConflictHunk{File: file})
			continue
		}
		trunkRaw, err := w.git(ctx, w.primary, "show", trunkHead+":"+file)
		if err != nil {
			for range blocks {
				hunks = append(hunks, ConflictHunk{File: file})
			}
			continue
		}
		trunkLines := strings.Split(trunkRaw, "\n")
		for _, block := range blocks {
			h := ConflictHunk{File: file}
			if start, end, ok := locateBlock(trunkLines, block); ok {
				h.StartLine, h.EndLine = start, end
				if commit, ok := w.blameCommit(ctx, file, trunkHead, start, end); ok {
					h.Commit = commit
					h.TaskID = w.trailerTask(ctx, commit)
				}
			}
			hunks = append(hunks, h)
		}
	}
	return hunks
}

// theirsBlocks extracts the incoming (trunk) side of every conflict hunk in
// a file's raw content: the lines between "=======" and ">>>>>>>".
func theirsBlocks(content string) [][]string {
	var blocks [][]string
	var cur []string
	inTheirs := false
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "======="):
			inTheirs = true
			cur = nil
		case strings.HasPrefix(line, ">>>>>>> "):
			if inTheirs {
				blocks = append(blocks, cur)
			}
			inTheirs = false
		case strings.HasPrefix(line, "<<<<<<< "):
			inTheirs = false
		case inTheirs:
			cur = append(cur, line)
		}
	}
	return blocks
}

// locateBlock finds block's 1-based [start,end] line range in trunkLines,
// contiguous and byte-identical — git copies conflict-marker content
// verbatim from the trunk blob, so an exact match is expected whenever the
// trunk-side text hasn't itself been rewritten since.
func locateBlock(trunkLines, block []string) (start, end int, ok bool) {
	if len(block) == 0 {
		return 0, 0, false
	}
	for i := 0; i+len(block) <= len(trunkLines); i++ {
		match := true
		for j, l := range block {
			if trunkLines[i+j] != l {
				match = false
				break
			}
		}
		if match {
			return i + 1, i + len(block), true
		}
	}
	return 0, 0, false
}

// blameCommit runs git blame over trunkHead's [start,end] line range in file
// and returns the commit that introduced it.
func (w *worktrees) blameCommit(ctx context.Context, file, trunkHead string, start, end int) (string, bool) {
	out, err := w.git(ctx, w.primary, "blame", "--porcelain", "-L", fmt.Sprintf("%d,%d", start, end), trunkHead, "--", file)
	if err != nil {
		return "", false
	}
	line, _, _ := strings.Cut(out, "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

// trailerTask reads a commit's Task: trailer, naming which task landed it.
func (w *worktrees) trailerTask(ctx context.Context, commit string) string {
	out, err := w.git(ctx, w.primary, "show", "-s", "--format=%(trailers:key=Task,valueonly)", commit)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// splitLines splits a newline-separated listing (e.g. `git diff
// --name-only`'s output) into trimmed, non-empty entries.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// sweepHandoff removes any file named HANDOFF.md, at any depth, still
// present on the task branch — the architect→executor convention says the
// executor deletes it, but that's chronically forgotten; this is the
// backstop (Drew: "we can blindly delete anything called HANDOFF.md
// pre-merge").
func (w *worktrees) sweepHandoff(ctx context.Context, dir string, task contracts.Task) error {
	out, err := w.git(ctx, dir, "ls-files", "-z")
	if err != nil {
		return err
	}
	var stale []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" && filepath.Base(p) == "HANDOFF.md" {
			stale = append(stale, p)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	if _, err := w.git(ctx, dir, append([]string{"rm", "--"}, stale...)...); err != nil {
		return err
	}
	msg := fmt.Sprintf("Remove HANDOFF.md before merge (pipeline convention)\n\nTask: %s", task.ID)
	_, err = w.git(ctx, dir, "commit", "-m", msg)
	return err
}

// remove cleans up after a fully-merged task. Blocked tasks keep their
// worktrees for inspection and manual merge-back.
func (w *worktrees) remove(ctx context.Context, dir, branch string) {
	_, _ = w.git(ctx, w.primary, "worktree", "remove", "--force", dir)
	_, _ = w.git(ctx, w.primary, "branch", "-D", branch)
}

// backupBranch force-updates backup/<taskID> to the current head of the
// task's branch (mads/<taskID>), if that branch exists. This is the salvage
// net for a block: create (above) deliberately wipes a reclaimed task's
// worktree and branch before starting fresh, so anything not backed up
// before the block is lost the moment the task gets requeued and claimed
// again. Reports whether a branch existed to back up — false is the common
// case for a task that blocked before ever creating a worktree (e.g. the
// scheduler's cycle rejection), not an error.
func (w *worktrees) backupBranch(ctx context.Context, taskID string) bool {
	branch := "mads/" + taskID
	if _, err := w.git(ctx, w.primary, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return false
	}
	_, err := w.git(ctx, w.primary, "branch", "--force", "backup/"+taskID, branch)
	return err == nil
}

// deleteBackupBranch removes backup/<taskID> if present — stale-backup
// hygiene once a task's work has actually landed, mirroring what shepherds
// were doing by hand. Best-effort: no backup branch is the common case, not
// an error.
func (w *worktrees) deleteBackupBranch(ctx context.Context, taskID string) {
	_, _ = w.git(ctx, w.primary, "branch", "-D", "backup/"+taskID)
}
