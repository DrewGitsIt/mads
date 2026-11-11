package runner

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// rerootedTag marks a session whose folder was gone when it started.
const rerootedTag = "rerooted"

// dirGone reports whether dir is definitely not there: absent, or not a
// directory. An error that says nothing about existence (permissions) is
// not "gone" — moving a session off a folder we merely can't stat would be
// worse than letting the provider report the failure.
func dirGone(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		return os.IsNotExist(err)
	}
	return !fi.IsDir()
}

// rerootMissingDir picks the folder Start begins from. Normally that is
// sess.Dir, untouched. When the folder is gone — a deleted checkout, an
// unmounted drive, an imported conversation from another machine — the
// session moves to the default workspace's primary repo instead, and sess
// is updated in place (Dir, RerootedFrom, the "rerooted" tag, Workspace,
// RerootNotePending) for Start's record write to persist.
//
// It never falls back to "~" or Config.DefaultDir (which may be "~"):
// dropping an orphan there would quietly widen what the agent can reach to
// the whole home directory. No default workspace, no primary repo, or that
// folder gone too is a start error, not a guess.
//
// It runs before isolate, so a session that has never started and asks for
// worktree isolation gets its worktree from the new folder. A session whose
// worktree folder still exists is not re-rooted just because Dir vanished:
// the worktree is where it runs, so that is the returned folder. One whose
// worktree is gone as well loses its worktree fields and runs in place in
// the new folder — its branch and base lived in a repo that no longer
// exists, so repairing it there would mean branching from a name the new
// repo may not have.
//
// A session already re-rooted by an earlier start has an existing Dir and
// passes straight through; only RerootNotePending carries over.
func (r *Runner) rerootMissingDir(ctx context.Context, sess *contracts.Session) (string, error) {
	if sess.Dir == "" || !dirGone(sess.Dir) {
		return sess.Dir, nil
	}
	if sess.WorktreeDir != "" && !dirGone(sess.WorktreeDir) {
		return sess.WorktreeDir, nil
	}

	gone := sess.Dir
	cfg, err := r.data.Config(ctx)
	if err != nil {
		return "", fmt.Errorf("runner: load config to re-root session: %w", err)
	}
	if cfg.DefaultWorkspace == "" {
		return "", fmt.Errorf("runner: this session's folder %s no longer exists, and there is no default workspace to move it to — set a default workspace, or restore the folder", gone)
	}
	ws, err := r.data.Workspace(ctx, cfg.DefaultWorkspace)
	if err != nil {
		return "", fmt.Errorf("runner: this session's folder %s no longer exists, and the default workspace %q could not be loaded: %w", gone, cfg.DefaultWorkspace, err)
	}
	dir, ok := ws.PrimaryRepo()
	if !ok {
		return "", fmt.Errorf("runner: this session's folder %s no longer exists, and the default workspace %q has no primary repo to move it to", gone, ws.Name)
	}
	if dirGone(dir) {
		return "", fmt.Errorf("runner: this session's folder %s no longer exists, and neither does the default workspace %q's primary repo %s", gone, ws.Name, dir)
	}

	if sess.WorktreeDir != "" {
		var empty string
		var zero int
		r.recordMu.Lock()
		err := r.data.SetSessionWorktree(ctx, r.sessionID, contracts.SessionWorktreePatch{
			WorktreeDir: &empty, WorktreeBranch: &empty, WorktreeBase: &empty,
			UncommittedCount: &zero, UnlandedCount: &zero, OutsideCount: &zero,
		})
		r.recordMu.Unlock()
		if err != nil {
			return "", fmt.Errorf("runner: clear lost worktree while re-rooting: %w", err)
		}
		sess.WorktreeDir, sess.WorktreeBranch, sess.WorktreeBase = "", "", ""
		sess.UncommittedCount, sess.UnlandedCount, sess.OutsideCount = 0, 0, 0
	}
	sess.RerootedFrom = gone
	sess.Dir = dir
	sess.Workspace = ws.ID
	if !slices.Contains(sess.Tags, rerootedTag) {
		sess.Tags = append(sess.Tags, rerootedTag)
	}
	sess.RerootNotePending = true
	return dir, nil
}

// rerootNote is what the agent is told once after a re-root. The history it
// resumes is full of absolute paths under the old folder, and without this
// it can write straight back into them — outside anything mads tracks.
func rerootNote(from, to string) string {
	return fmt.Sprintf("[mads] This conversation began in %s, which no longer exists. You're now working in %s. Don't recreate files under the old path.", from, to)
}

// claimRerootNoteLocked prepends the pending note to text — the first turn
// the runner sends, of whatever kind — and returns the note it used ("" when
// none was owed). The note is the visible start of that message, not a
// system prompt (see Start's "no mads preamble" rule), and rides the turn
// rather than costing one of its own. It is claimed here, under r.mu with
// the busy flag, so a later turn never repeats it; a send that then fails
// hands the note back to restoreRerootNote.
//
// A linked session's injection counts: it can be the first thing an agent
// hears after a wake, and the risk the note guards against (writing into the
// old path) doesn't depend on who asked.
//
// Terminal input in PTY mode is not covered: keystrokes typed into the
// terminal reach the provider without passing through Send, so there is no
// message to prepend to. The note stays owed (in memory and on the record)
// until the next Send, Inject or queued message.
//
// The notes owed for other reasons (Session.PendingNotes: a file the user
// moved into a repo) ride the same turn, after the re-root note.
func (r *Runner) claimRerootNoteLocked(text string) (wire string, owed owedNotes) {
	owed = owedNotes{reroot: r.rerootNote, notes: r.notes}
	r.rerootNote, r.notes = "", nil
	if owed.empty() {
		return text, owed
	}
	return owed.text() + "\n\n" + text, owed
}

// owedNotes is what one turn claimed: the re-root note and the other owed
// notes, so a failed send can re-arm exactly those and a delivered one can
// clear exactly those.
type owedNotes struct {
	reroot string
	notes  []string
}

func (o owedNotes) empty() bool { return o.reroot == "" && len(o.notes) == 0 }

func (o owedNotes) text() string {
	parts := o.notes
	if o.reroot != "" {
		parts = append([]string{o.reroot}, o.notes...)
	}
	return strings.Join(parts, "\n")
}

// restoreRerootNote re-arms notes claimed by a send that then failed.
func (r *Runner) restoreRerootNote(owed owedNotes) {
	if owed.empty() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rerootNote == "" {
		r.rerootNote = owed.reroot
	}
	for _, n := range slices.Backward(owed.notes) {
		if !slices.Contains(r.notes, n) {
			r.notes = slices.Insert(r.notes, 0, n)
		}
	}
}

// AddNote owes the agent note on its next turn (docs/COMMIT-LAND-PUSH-
// DESIGN.md D4) — the live half of Session.PendingNotes, which the Router
// has already written. A note already owed isn't owed twice.
func (r *Runner) AddNote(note string) {
	if note == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.notes, note) {
		r.notes = append(r.notes, note)
	}
}

// noteDelivered clears Session.RerootNotePending once a turn carrying the
// note has been sent, so a later wake doesn't repeat it. A crash between the
// send and this write repeats the note on the next wake — the agent hears it
// twice rather than never. Best effort, like the other record writes made
// after a turn is already underway.
func (r *Runner) noteDelivered(ctx context.Context, owed owedNotes) {
	if len(owed.notes) > 0 {
		if err := r.data.SetSessionNotes(ctx, r.sessionID, contracts.SessionNotesPatch{Remove: owed.notes}); err != nil {
			log.Printf("runner: clear delivered notes: %v", err)
		}
	}
	if owed.reroot == "" {
		return
	}
	r.recordMu.Lock()
	defer r.recordMu.Unlock()
	cur, err := r.data.Session(ctx, r.sessionID)
	if err != nil {
		log.Printf("runner: load session to clear reroot note: %v", err)
		return
	}
	if !cur.RerootNotePending {
		return
	}
	cur.RerootNotePending = false
	if err := r.data.UpdateSession(ctx, &cur); err != nil {
		log.Printf("runner: clear reroot note: %v", err)
	}
}
