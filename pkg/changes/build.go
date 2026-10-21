// Package changes computes a session's ChangeSet (pkg/contracts/changes.go,
// docs/CHANGES-DESIGN.md): what differs on disk from a chosen baseline,
// with line counts, tiers, turn membership and commit membership.
//
// It is the reader half of the Changes architecture: the session service
// writes the shadow store (services/session/shadow) and the data service
// owns the touch ledger; this package only reads them, plus the user's
// repos. Callers gather inputs (session record, ledger, commits,
// transcript) and call Build — the Router serves the result. Kept
// service-shaped (one entry point, plain inputs) so moving it behind its
// own process later is mechanical.
//
// The file list comes from disk, not the ledger: for each repo root the
// session touched, a temp copy of the shadow store's index is refreshed
// against the work tree and diffed against the baseline commit. The ledger
// contributes attribution and turn membership, and is the only source for
// files outside every repo (diffed against their captured pre-images) and
// for repo roots whose baseline isn't there yet. Nothing here writes to
// the shadow store's index, refs or commits, or to the user's repos.
package changes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// Inputs is everything Build reads besides the filesystem.
type Inputs struct {
	Session contracts.Session
	// Ledger is the session's file-change ledger, in record order.
	Ledger []contracts.FileChange
	// Commits are the session's recorded commits, in record order.
	Commits []contracts.Commit
	// Transcript is the session's conversation, for turn numbering.
	Transcript []contracts.TranscriptMessage
	// Baseline is one of contracts.Baseline*; "" means session start.
	Baseline string
	// Now stamps ComputedAt; zero means time.Now().
	Now time.Time
}

// ErrBaselineUnavailable is returned (wrapped, with the reason) when the
// requested baseline doesn't apply to the session, or isn't a known id.
var ErrBaselineUnavailable = errors.New("baseline unavailable")

var commitHashRe = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// SessionFolder is the session's folder — its worktree, else its
// configured directory, else where it actually ran — canonicalized with
// shadow.RealPath. "" when the record names none.
func SessionFolder(sess contracts.Session) string {
	for _, d := range []string{sess.WorktreeDir, sess.Dir, sess.Cwd} {
		if d != "" {
			return shadow.RealPath(d)
		}
	}
	return ""
}

// ledgerFile is one canonical path's ledger rows, collapsed.
type ledgerFile struct {
	rows       []contracts.FileChange
	attributed bool
}

func (l *ledgerFile) first() string { return l.rows[0].Change }

func (l *ledgerFile) stamps() []time.Time {
	out := make([]time.Time, 0, len(l.rows))
	for _, r := range l.rows {
		out = append(out, r.Timestamp)
	}
	return out
}

func (l *ledgerFile) last() time.Time {
	var t time.Time
	for _, r := range l.rows {
		if r.Timestamp.After(t) {
			t = r.Timestamp
		}
	}
	return t
}

// LedgerPath canonicalizes a ledger row's path: the runner records paths
// as tools reported them, which can be relative to the session's cwd
// (recorded in Repo, else the session's folder), and may reach the folder
// through a symlink. "" when a relative path has nothing to anchor on —
// resolving it against this process's own working directory would pull an
// unrelated folder into the session.
func LedgerPath(fc contracts.FileChange, folder string) string {
	p := fc.Path
	if !filepath.IsAbs(p) {
		base := fc.Repo
		if base == "" {
			base = folder
		}
		if base == "" {
			return ""
		}
		p = filepath.Join(base, p)
	}
	return shadow.RealPath(p)
}

func collectLedger(rows []contracts.FileChange, folder string) map[string]*ledgerFile {
	out := map[string]*ledgerFile{}
	for _, fc := range rows {
		if fc.Path == "" {
			continue
		}
		p := LedgerPath(fc, folder)
		if p == "" {
			continue
		}
		lf := out[p]
		if lf == nil {
			lf = &ledgerFile{}
			out[p] = lf
		}
		lf.rows = append(lf.rows, fc)
		lf.attributed = lf.attributed || fc.Attributed
	}
	return out
}

// fileBuild is one file on its way to a ChangedFile.
type fileBuild struct {
	path    string
	root    string // containing repo root, "" outside every repo
	change  string
	added   int
	deleted int
	binary  bool
	unknown bool
	created bool
}

// builder carries one Build's state.
type builder struct {
	in        Inputs
	now       time.Time
	folder    string
	ledger    map[string]*ledgerFile
	rootCache map[string]string
	captures  map[string]shadow.Record // path → first capture record
	files     map[string]*fileBuild
	notes     []string
	repoRoots map[string]bool // every repo root seen, for Git and commits
	unknownN  int
	nested    *nestedWorktrees // subagent worktrees under the folder, left out (worktrees.go)
}

// Build computes the session's ChangeSet against in.Baseline. Failures
// inside one root degrade to a Note; the only error is an unavailable
// baseline (ErrBaselineUnavailable) or a cancelled context.
func Build(ctx context.Context, in Inputs) (contracts.ChangeSet, error) {
	vs := in.Baseline
	if vs == "" {
		vs = contracts.BaselineSessionStart
	}
	folder := SessionFolder(in.Session)
	b := &builder{
		in:        in,
		now:       in.Now,
		folder:    folder,
		ledger:    collectLedger(in.Ledger, folder),
		rootCache: map[string]string{},
		captures:  map[string]shadow.Record{},
		files:     map[string]*fileBuild{},
		repoRoots: map[string]bool{},
		nested:    newNestedWorktrees(folder),
	}
	if files, wts := dropNestedWorktrees(b.ledger, b.nested); files > 0 {
		b.note("%d %s in %d subagent %s not listed; their work shows here once it lands",
			files, plural(files, "file", "files"), wts, plural(wts, "worktree is", "worktrees are"))
	}
	if b.now.IsZero() {
		b.now = time.Now().UTC()
	}
	folderRepo := ""
	if b.folder != "" {
		folderRepo = repoRootOf(b.folder, b.rootCache)
	}
	if folderRepo != "" {
		b.repoRoots[folderRepo] = true
	}

	baselines := resolveBaselines(ctx, in.Session, folderRepo)
	chosen, ok := findBaseline(baselines, vs)
	if !ok {
		return contracts.ChangeSet{}, fmt.Errorf("%w: unknown baseline %q", ErrBaselineUnavailable, vs)
	}
	if !chosen.Available {
		return contracts.ChangeSet{}, fmt.Errorf("%w: %s: %s", ErrBaselineUnavailable, vs, chosen.Reason)
	}

	b.loadCaptures()
	if vs == contracts.BaselineSessionStart {
		b.buildFromStart(ctx, folderRepo)
	} else {
		b.buildFromMergeBase(ctx, folderRepo, chosen)
	}
	if err := ctx.Err(); err != nil {
		return contracts.ChangeSet{}, err
	}
	return b.finish(ctx, vs, baselines), nil
}

func (b *builder) note(format string, args ...any) {
	b.notes = append(b.notes, fmt.Sprintf(format, args...))
}

func (b *builder) loadCaptures() {
	if b.in.Session.ShadowDir == "" {
		return
	}
	recs, err := shadow.ReadIndex(b.in.Session.ShadowDir)
	if err != nil {
		return
	}
	for _, rec := range recs {
		if rec.Kind != shadow.RecordCapture || rec.Path == "" {
			continue
		}
		if _, seen := b.captures[rec.Path]; !seen {
			b.captures[rec.Path] = rec
		}
	}
}

// rootOf is the repo root containing p ("" outside every repo).
func (b *builder) rootOf(p string) string {
	return repoRootOf(filepath.Dir(p), b.rootCache)
}

// buildFromStart lists files against the session-start baseline: a disk
// diff in every baselined repo root, the ledger everywhere else.
func (b *builder) buildFromStart(ctx context.Context, folderRepo string) {
	sess := b.in.Session
	candidates := map[string]bool{}
	if folderRepo != "" {
		candidates[folderRepo] = true
	}
	if sess.ShadowDir != "" {
		if recs, err := shadow.ReadIndex(sess.ShadowDir); err == nil {
			for _, rec := range recs {
				if rec.Root != "" && b.nested.rootOf(rec.Root) == "" {
					candidates[rec.Root] = true
				}
			}
		}
	}
	for p := range b.ledger {
		if r := b.rootOf(p); r != "" {
			candidates[r] = true
		}
	}
	// A repo the session recorded a commit in is one of its roots even when
	// no ledger row or capture points there (a worktree session whose
	// commit landed in the primary checkout, a commit made by a script).
	for _, c := range b.in.Commits {
		if c.Repo == "" || !filepath.IsAbs(c.Repo) {
			continue
		}
		if r := repoRootOf(shadow.RealPath(c.Repo), b.rootCache); r != "" && b.nested.rootOf(r) == "" {
			candidates[r] = true
		}
	}

	diffed := map[string]string{} // repo root → shadow git dir
	pending, missing, anchored := 0, 0, 0
	for _, root := range sortedKeys(candidates) {
		if repoRootOf(root, b.rootCache) != root {
			continue // a non-repo capture root: ledger only
		}
		b.repoRoots[root] = true
		if sess.ShadowDir == "" {
			if b.diffFromFirstCommit(ctx, root) {
				anchored++
			}
			continue
		}
		gitDir := filepath.Join(sess.ShadowDir, shadow.RootKey(root))
		if !hasShadowBaseline(ctx, gitDir) {
			if b.diffFromFirstCommit(ctx, root) {
				anchored++
				continue
			}
			// The session folder's root is baselined at provisioning, so
			// no baseline there yet means it's still running. Another root
			// with no shadow dir at all was never captured into (touched
			// only by shell commands, say) and won't be baselined.
			if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil && root != folderRepo {
				missing++
			} else {
				pending++
			}
			continue
		}
		entries, err := shadowDiff(ctx, gitDir, root)
		if err != nil {
			b.note("couldn't read changes in %s (%v); files there are listed from the session's record", root, err)
			continue
		}
		diffed[root] = gitDir
		for _, e := range entries {
			p := filepath.Join(root, filepath.FromSlash(e.Rel))
			if b.nested.rootOf(p) != "" {
				continue // a subagent worktree the user's repo doesn't ignore
			}
			b.files[p] = &fileBuild{
				path: p, root: root, change: e.Change,
				added: e.Added, deleted: e.Deleted, binary: e.Binary,
				created: e.Change == "added",
			}
		}
	}
	switch {
	case sess.ShadowDir == "" && len(b.ledger) > 0 && anchored == 0:
		b.note("no session-start snapshot for this session; files are listed from the session's record")
	case pending > 0:
		b.note("session-start snapshot still running; some files are listed from the session's record")
	}
	if missing > 0 {
		b.note("%d %s no session-start snapshot; files there are listed from the session's record", missing, plural(missing, "repo has", "repos have"))
	}

	// Ledger paths: in a diffed root they're already listed unless reverted
	// or ignored — only the ignored ones come back, from the ledger.
	ignoredCheck := map[string][]string{} // root → rels
	var ledgerOnly []string
	for _, p := range sortedKeys(b.ledger) {
		if _, listed := b.files[p]; listed {
			continue
		}
		root := b.rootOf(p)
		if _, ok := diffed[root]; ok {
			if rel, ok := relUnder(root, p); ok {
				ignoredCheck[root] = append(ignoredCheck[root], rel)
			}
			continue
		}
		ledgerOnly = append(ledgerOnly, p)
	}
	for root, rels := range ignoredCheck {
		// Ignored by the user's rules or the shadow store's excludes: the
		// disk diff can't see these, so the ledger lists them. Whether the
		// user's repo ignores them (the scratch rule) is decided in finish.
		for rel := range checkIgnored(ctx, root, diffed[root], rels) {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if fb := b.ledgerOnlyFile(ctx, p, root); fb != nil {
				b.files[p] = fb
			}
		}
	}
	for _, p := range ledgerOnly {
		if fb := b.ledgerOnlyFile(ctx, p, b.rootOf(p)); fb != nil {
			b.files[p] = fb
		}
	}
}

// diffFromFirstCommit approximates session start in a repo root that has
// no shadow baseline (a session from before the shadow store, an imported
// conversation, shadowing off, or a baseline still running): the parent of
// the session's earliest recorded commit there. It lists tracked files
// that differ from that commit, so work the session committed stays
// listed — the ledger fallback alone compares against HEAD, where a
// committed file looks unchanged. Untracked files are left to the ledger,
// so the user's own untracked clutter isn't presented as session work. No
// recorded commit in the root: nothing to anchor on, the ledger decides.
func (b *builder) diffFromFirstCommit(ctx context.Context, root string) bool {
	rev := ""
	for _, c := range b.in.Commits { // record order: oldest first
		if !commitHashRe.MatchString(c.Hash) {
			continue
		}
		if _, ok := resolveRef(ctx, root, c.Hash); ok {
			rev = c.Hash + "^"
			break
		}
	}
	if rev == "" {
		return false
	}
	if _, ok := resolveRef(ctx, root, rev); !ok {
		rev = emptyTree // the session's first commit is the repo's root commit
	}
	entries, err := trackedDiff(ctx, root, rev)
	if err != nil {
		b.note("couldn't read changes in %s (%v); files there are listed from the session's record", root, err)
		return false
	}
	for _, e := range entries {
		p := filepath.Join(root, filepath.FromSlash(e.Rel))
		b.files[p] = &fileBuild{
			path: p, root: root, change: e.Change,
			added: e.Added, deleted: e.Deleted, binary: e.Binary,
			created: e.Change == "added",
		}
	}
	b.note("no session-start snapshot in %s; compared against the commit before the session's first commit there", filepath.Base(root))
	return true
}

// emptyTree is git's well-known empty tree object.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// ledgerOnlyFile resolves a file the disk diff can't see: its net change
// from the ledger and disk, its line counts against the first captured
// pre-image (else, in a repo, HEAD). nil when it nets out to no change
// (created then deleted, or back to its original content).
func (b *builder) ledgerOnlyFile(ctx context.Context, p, root string) *fileBuild {
	lf := b.ledger[p]
	if lf == nil {
		return nil
	}
	fi, statErr := os.Lstat(p)
	exists := statErr == nil
	if exists && fi.IsDir() {
		return nil // a directory isn't a file change (a mkdir'd tool target)
	}
	rec, captured := b.captures[p]
	created := lf.first() == "added" || (captured && rec.Missing)
	if !exists && created {
		return nil
	}
	fb := &fileBuild{path: p, root: root, created: created}
	switch {
	case !exists:
		fb.change = "deleted"
	case created:
		fb.change = "added"
	default:
		fb.change = "modified"
	}

	original, known := "", false
	switch {
	case captured && rec.Missing:
		known = true
	case captured && !rec.TooLarge && rec.Blob != "":
		if c, err := shadow.BlobContent(ctx, b.in.Session.ShadowDir, rec.RootKey, rec.Blob); err == nil {
			original, known = c, true
		}
	case created:
		known = true
	case root != "":
		if rel, ok := relUnder(root, p); ok {
			original, known = showFile(ctx, root, "HEAD", rel)
		}
	}
	if !known {
		fb.unknown = true
		b.unknownN++
		return fb
	}
	e, same, err := noIndexNumstat(ctx, original, p)
	if err != nil {
		fb.unknown = true
		b.unknownN++
		return fb
	}
	if same {
		return nil
	}
	fb.added, fb.deleted, fb.binary = e.Added, e.Deleted, e.Binary
	return fb
}

// buildFromMergeBase lists files in the session folder's repo against a
// merge base (base or main): committed and uncommitted work, plus
// untracked files.
func (b *builder) buildFromMergeBase(ctx context.Context, repo string, bl resolvedBaseline) {
	entries, err := mergeBaseDiff(ctx, repo, bl.mb)
	if err != nil {
		b.note("couldn't read changes in %s (%v)", repo, err)
		return
	}
	rels := make([]string, 0, len(entries))
	for _, e := range entries {
		rels = append(rels, e.Rel)
	}
	// "Created in session" is against session start, not the merge base:
	// the shadow baseline when there is one, else the ledger.
	var atStart map[string]bool
	gitDir := ""
	if sd := b.in.Session.ShadowDir; sd != "" {
		gitDir = filepath.Join(sd, shadow.RootKey(repo))
		if hasShadowBaseline(ctx, gitDir) {
			atStart = inShadowBaseline(ctx, gitDir, rels)
		}
	}
	for _, e := range entries {
		p := filepath.Join(repo, filepath.FromSlash(e.Rel))
		if b.nested.rootOf(p) != "" {
			continue // a subagent worktree the user's repo doesn't ignore
		}
		created := false
		if atStart != nil {
			created = !atStart[e.Rel]
		} else if lf := b.ledger[p]; lf != nil {
			created = lf.first() == "added"
		}
		b.files[p] = &fileBuild{
			path: p, root: repo, change: e.Change,
			added: e.Added, deleted: e.Deleted, binary: e.Binary,
			created: created && e.Change != "deleted",
		}
	}
	outside := 0
	for p := range b.ledger {
		if _, ok := relUnder(repo, p); !ok {
			outside++
		}
	}
	if outside > 0 {
		b.note("%d changed files outside %s are only compared against Session start", outside, repo)
	}
}

// relUnder is p relative to root (slash-separated), ok=false when p isn't
// under root.
func relUnder(root, p string) (string, bool) {
	if root == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// finish classifies every file, joins turns and commits, and assembles
// the ChangeSet.
func (b *builder) finish(ctx context.Context, vs string, baselines []resolvedBaseline) contracts.ChangeSet {
	turns := Turns(b.in.Transcript)

	// Batch git lookups per repo root.
	byRoot := map[string][]*fileBuild{}
	for _, fb := range b.files {
		if fb.root != "" {
			byRoot[fb.root] = append(byRoot[fb.root], fb)
			b.repoRoots[fb.root] = true
		}
	}
	generated := map[string]bool{}
	ignored := map[string]bool{}
	dirty := map[string]func(rel string) bool{}
	for root, fbs := range byRoot {
		var all []string
		for _, fb := range fbs {
			rel, _ := relUnder(root, fb.path)
			all = append(all, rel)
		}
		for rel := range checkAttrGenerated(ctx, root, all) {
			generated[filepath.Join(root, filepath.FromSlash(rel))] = true
		}
		// The user's own ignore rules only — not the shadow store's extra
		// excludes (build/, dist/, …), which a repo may well track. Tracked
		// files never match (check-ignore skips them), so this is also
		// "git status can't see it" for Uncommitted.
		for rel := range checkIgnored(ctx, root, "", all) {
			ignored[filepath.Join(root, filepath.FromSlash(rel))] = true
		}
		files, untrackedDirs, err := dirtyPaths(ctx, root)
		if err != nil {
			b.note("couldn't read git status in %s (%v); commit state may be off", root, err)
			dirty[root] = func(string) bool { return true }
			continue
		}
		dirty[root] = func(rel string) bool {
			if files[rel] {
				return true
			}
			for _, d := range untrackedDirs {
				if strings.HasPrefix(rel, d) {
					return true
				}
			}
			return false
		}
	}

	commits, commitsByPath := b.commits(ctx)

	out := make([]contracts.ChangedFile, 0, len(b.files))
	for _, fb := range b.files {
		cf := contracts.ChangedFile{
			Path:          fb.path,
			Change:        fb.change,
			Added:         fb.added,
			Deleted:       fb.deleted,
			Binary:        fb.binary,
			CountsUnknown: fb.unknown,
			Turns:         []int{},
			Commits:       []string{},
		}
		if fb.binary {
			cf.Added, cf.Deleted = 0, 0
		}
		cf.Display, cf.Outside = b.display(fb.path)

		facts := FileFacts{
			// A file in one of the session's commits is deliberate work, so
			// never scratch (and never offered for deletion), however it
			// looks: scratch requires Created.
			Created:           fb.created && len(commitsByPath[fb.path]) == 0,
			Outside:           cf.Outside,
			Ignored:           ignored[fb.path],
			LinguistGenerated: generated[fb.path],
		}
		if fb.change != "deleted" {
			facts.GeneratedHeader = HasGeneratedHeader(fb.path)
		}
		facts.Rel = b.classifyRel(fb)
		cf.Tier, cf.ScratchRule = Classify(facts)

		if lf := b.ledger[fb.path]; lf != nil {
			cf.Attributed = lf.attributed
			cf.Turns = FileTurns(turns, lf.stamps())
			cf.LastChanged = lf.last()
		}
		if cf.LastChanged.IsZero() {
			if fi, err := os.Lstat(fb.path); err == nil {
				cf.LastChanged = fi.ModTime().UTC()
			} else {
				cf.LastChanged = b.now
			}
		}
		if hs := commitsByPath[fb.path]; len(hs) > 0 {
			cf.Commits = hs
		}
		cf.Uncommitted = true
		if fb.root != "" && !facts.Ignored {
			if rel, ok := relUnder(fb.root, fb.path); ok {
				cf.Uncommitted = dirty[fb.root](rel)
			}
		}
		out = append(out, cf)
	}

	tierRank := map[string]int{}
	for i, t := range contracts.ChangeTiers {
		tierRank[t] = i
	}
	sort.Slice(out, func(i, j int) bool {
		a, c := out[i], out[j]
		if tierRank[a.Tier] != tierRank[c.Tier] {
			return tierRank[a.Tier] < tierRank[c.Tier]
		}
		if sa, sc := a.Added+a.Deleted, c.Added+c.Deleted; sa != sc {
			return sa > sc
		}
		return a.Path < c.Path
	})

	turnFiles := map[int]int{}
	var totals contracts.ChangeSetTotals
	for _, f := range out {
		for _, n := range f.Turns {
			turnFiles[n]++
		}
		totals.Files++
		totals.Added += f.Added
		totals.Deleted += f.Deleted
	}
	for i := range turns {
		turns[i].Files = turnFiles[turns[i].N]
	}

	if b.unknownN > 0 {
		b.note("line counts unavailable for %d %s with no saved original", b.unknownN, plural(b.unknownN, "file", "files"))
	}

	list := make([]contracts.ChangeBaseline, len(baselines))
	for i, bl := range baselines {
		list[i] = bl.ChangeBaseline
	}
	return contracts.ChangeSet{
		Baseline:   vs,
		Baselines:  list,
		Git:        len(b.repoRoots) > 0,
		Files:      out,
		Turns:      turns,
		Commits:    commits,
		Totals:     totals,
		Notes:      b.notes,
		ComputedAt: b.now,
	}
}

// display is Path relative to the session folder, or Path itself (and
// outside=true) when it isn't under it.
func (b *builder) display(p string) (string, bool) {
	if b.folder == "" {
		return p, false
	}
	if rel, ok := relUnder(b.folder, p); ok {
		return rel, false
	}
	return p, true
}

// classifyRel is the path the tier rules read: relative to the file's
// repo root, else the session folder, else its base name.
func (b *builder) classifyRel(fb *fileBuild) string {
	if rel, ok := relUnder(fb.root, fb.path); ok {
		return rel
	}
	if rel, ok := relUnder(b.folder, fb.path); ok {
		return rel
	}
	return filepath.Base(fb.path)
}

// commits resolves the session's recorded commits, newest first, with
// absolute paths, and indexes them by path. A worktree shares its object
// store with the primary checkout, so every repo can resolve every hash;
// paths are anchored in the first repo whose checked-out branch contains
// the commit — the session folder's first, so a worktree session's own
// commits key the worktree's files, while a commit that landed in the
// primary checkout keys the primary's. No branch contains it (amended
// away, say): the first repo that has it.
func (b *builder) commits(ctx context.Context) ([]contracts.ChangeCommit, map[string][]string) {
	out := []contracts.ChangeCommit{}
	byPath := map[string][]string{}
	recorded := b.in.Commits
	if len(recorded) == 0 {
		return out, byPath
	}
	var repos []string
	if b.folder != "" {
		if r := repoRootOf(b.folder, b.rootCache); r != "" {
			repos = append(repos, r)
		}
	}
	for _, r := range sortedKeys(b.repoRoots) {
		if len(repos) == 0 || repos[0] != r {
			repos = append(repos, r)
		}
	}
	for i := len(recorded) - 1; i >= 0; i-- {
		c := recorded[i]
		if !commitHashRe.MatchString(c.Hash) {
			continue
		}
		cc := contracts.ChangeCommit{Hash: c.Hash, Message: firstLine(c.Message), Timestamp: c.Timestamp, Paths: []string{}}
		tryRepos := repos
		if c.Repo != "" {
			tryRepos = append(append([]string{}, repos...), shadow.RealPath(c.Repo))
		}
		anchor := ""
		for _, repo := range tryRepos {
			if _, ok := resolveRef(ctx, repo, c.Hash); !ok {
				continue
			}
			if anchor == "" {
				anchor = repo
			}
			if isAncestorOfHEAD(ctx, repo, c.Hash) {
				anchor = repo
				break
			}
		}
		if repo := anchor; repo != "" {
			rels, err := commitFiles(ctx, repo, c.Hash)
			if err != nil {
				break
			}
			if err == nil {
				for _, rel := range rels {
					p := filepath.Join(repo, filepath.FromSlash(rel))
					cc.Paths = append(cc.Paths, p)
					byPath[p] = append(byPath[p], c.Hash)
				}
			}
		}
		out = append(out, cc)
	}
	return out, byPath
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
