// Package shadow implements the per-session shadow git store behind
// historically-accurate tool diffs (docs/SHADOW-GIT-DIFFS-ADR.md). Two
// capture mechanisms feed it: per-mutating-tool pre-image blobs
// (Store.Capture — a single `git hash-object -w` on the hot path) and
// turn-boundary tree commits (Store.CommitTurnAsync — quiescent-path
// commits per dirtied root, plus a one-time baseline commit per root).
//
// Layout: one shadow git dir per (session, root) at
// <shadowRoot>/<session_id>/<root_key>/, where root_key is a short hash of
// the root's absolute path (RootKey) and the actual path is recorded in the
// shadow dir's git config (`mads.root`) for reverse lookup. Roots are
// discovered lazily from captured paths (`git rev-parse --show-toplevel`,
// cached); a non-git directory counts as its own root. Every path entering
// the package is canonicalized first (RealPath) — git reports roots
// physically, so an unresolved path would key a second, parallel root and
// break every root-relative lookup against it. Tool_use_id → blob
// mappings live in a shadow-local append-only JSONL index (index.go), not
// the event log, keeping this decoupled from pkg/contracts.
//
// The package is deliberately importable outside services/session (not
// internal/): the session runner is the sole writer, but the Router reads
// the same dirs and index to serve diffs (services/router/internal/api),
// and both sides must share RootKey and the index format.
//
// Everything here is best-effort: any failure logs and degrades to the
// pre-shadow behavior (input-derived diffs, HEAD-based working diffs) —
// nothing ever blocks or delays the agent's turn.
package shadow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// BaselineTag names the per-root baseline commit — the "state of the
	// root when the session first touched it" ref the Router diffs against.
	BaselineTag = "mads-baseline"

	// MaxBlobBytes is the per-file capture guard (ADR: ~10 MB): larger
	// files record a too-large marker instead of a blob, which the UI can
	// render honestly.
	MaxBlobBytes = 10 << 20

	// gitTimeout bounds every git invocation except the baseline commit,
	// which legitimately runs seconds-to-minutes on large checkouts.
	gitTimeout = 10 * time.Second

	// baselineTimeout bounds the baseline `add -A` + commit. Measured 6.3s
	// on a 39 GB checkout (~20 s per GB of tracked content) — 30 minutes is
	// far beyond any repo we expect, while still not "forever" if git hangs.
	baselineTimeout = 30 * time.Minute
)

// excludeList is the mads-side ignore list layered on top of the user
// repo's own ignore rules (which apply naturally: tree ops run with
// --work-tree=<root>, so the root's .gitignore files and the user's global
// core.excludesFile are honored). This only covers what a bare checkout
// wouldn't ignore itself: the repo's own .git dir and common build/venv
// output dirs in non-git roots.
var excludeList = []string{
	".git/",
	"node_modules/",
	"target/",
	"dist/",
	"build/",
	"out/",
	".venv/",
	"venv/",
	"__pycache__/",
	".next/",
	".gradle/",
	".cache/",
}

// RootKey is the directory name of a root's shadow git dir: a short,
// filename-safe hash of the root's absolute path. Both the writer (session
// runner) and reader (Router) derive it from the same input, so neither
// side ever guesses.
func RootKey(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])[:12]
}

// RealPath is the canonical form of a path for everything in this package:
// absolute, with symlinks in it resolved. Both sides must agree on it for the
// same reason they must agree on RootKey.
//
// Roots arrive from `git rev-parse --show-toplevel`, which always answers
// with the *physical* path. A captured file path arrives from the agent's own
// tool call, which does not. Mixing the two silently breaks every
// root-relative lookup built on them: filepath.Rel("/private/var/x",
// "/var/x/f") is not "f" but a ../.. escape, so the Router asked its baseline
// for a path the commit cannot contain, read that as "the session added this
// file", and served an empty original — a modified file rendered as wholly
// new content. RootKey(root) drifts the same way for a non-git root.
//
// macOS makes this unconditional (/var and /tmp are symlinks into /private),
// which is how a first macOS CI run surfaced it; it was never macOS-specific,
// though. Any workspace reached through a symlink — ~/work -> /mnt/data/work,
// a symlinked checkout — splits identically on Linux.
//
// A path whose leaf does not exist yet still resolves: Capture records the
// pre-image of a file a tool is about to create, so "not there yet" is an
// ordinary case, and only the directory can be resolved then. A path whose
// directory is also absent resolves to merely absolute — nothing to follow,
// and git will discover its root the same way.
func RealPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	dir, base := filepath.Split(abs)
	if resolved, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		return filepath.Join(resolved, base)
	}
	return abs
}

// Store is one session's shadow store. The zero value is not usable; New
// builds one. All methods are safe on a nil *Store (no-ops), so callers can
// wire it unconditionally and let configuration decide.
type Store struct {
	dir string // <shadowRoot>/<session_id>

	mu        sync.Mutex
	roots     map[string]*rootState // abs root path → state
	rootByDir map[string]string     // abs dir → abs root path (rev-parse cache)

	indexMu sync.Mutex // serializes appends to the JSONL index

	// wg tracks the store's own background work (first-touch baselines,
	// turn commits) so Wait can drain it — tests and orderly teardown; the
	// hot path never waits on it.
	wg sync.WaitGroup
}

// Wait blocks until all background work started so far (first-touch
// baselines, async turn commits) has finished. Nil-safe.
func (s *Store) Wait() {
	if s == nil {
		return
	}
	s.wg.Wait()
}

type rootState struct {
	path   string // abs root path
	gitDir string // <store.dir>/<RootKey(path)>

	// mu serializes tree operations (baseline, turn commit) in this root's
	// shadow git dir. Blob captures don't take it: the object store is
	// concurrent-safe and captures must never wait behind a baseline.
	mu        sync.Mutex
	baselined bool // baseline commit exists (guarded by mu)
}

// New builds the Store for sessionID under shadowRoot (the
// $XDG_DATA_HOME/mads/shadow base — see DefaultRoot). The dir is made
// absolute so the path recorded on the session record resolves from any
// process's cwd (the Router reads it). Nothing is created on disk until
// the first capture or baseline.
func New(shadowRoot, sessionID string) *Store {
	dir := filepath.Join(shadowRoot, sessionID)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return &Store{
		dir:       dir,
		roots:     map[string]*rootState{},
		rootByDir: map[string]string{},
	}
}

// DefaultRoot is the conventional shadow base: $XDG_DATA_HOME/mads/shadow,
// falling back to ~/.local/share/mads/shadow per the XDG spec's documented
// default. Empty (caller should disable shadowing) only if the home dir is
// unresolvable.
func DefaultRoot() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "mads", "shadow")
}

// Dir is the session's shadow base directory — the value recorded on the
// session record as shadow_dir, so the Router never re-derives it.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Baseline ensures root's shadow git dir exists and holds a baseline
// commit (tagged BaselineTag) of the root's current tree. Idempotent: a
// root already baselined (including by a previous process for the same
// session — bounce/resume) is a cheap no-op. Synchronous — callers on the
// provisioning path run it in a goroutine (measured 6.3s on a 39 GB
// checkout; must never block provisioning).
func (s *Store) Baseline(root string) {
	if s == nil || root == "" {
		return
	}
	rs := s.rootState(RealPath(root))
	if rs == nil {
		return
	}
	s.baselineRoot(rs)
}

// Capture records the pre-image of one mutating tool call: it reads absPath
// and writes it into the shadow object store of the path's root, appending
// a capture record (tool_use_id → blob hash) to the session's index. Safe
// on the hot path: one stat + one `git hash-object -w` (~1–5 ms). Files
// over MaxBlobBytes record a too-large marker; a not-yet-existing file
// records a missing marker (the honest "no old side" for a fresh Write).
// The root is discovered (and its baseline kicked off in the background) on
// first touch. Best-effort: every failure logs and returns.
func (s *Store) Capture(toolUseID, absPath string) {
	if s == nil || toolUseID == "" || absPath == "" {
		return
	}
	abs := RealPath(absPath)
	root := s.rootFor(abs)
	rs := s.rootState(root)
	if rs == nil {
		return
	}
	// First-touch baseline for roots other than the provisioning-time
	// primary — backgrounded, never on the hot path. No-op if already done.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.baselineRoot(rs)
	}()

	rec := Record{
		Kind:      RecordCapture,
		ToolUseID: toolUseID,
		Path:      abs,
		Root:      root,
		RootKey:   RootKey(root),
		Time:      time.Now().UTC(),
	}
	fi, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		rec.Missing = true
	case err != nil:
		logf("shadow: stat %s: %v", abs, err)
		return
	case fi.Size() > MaxBlobBytes:
		rec.TooLarge = true
		rec.Size = fi.Size()
	default:
		blob, err := s.hashObject(rs.gitDir, abs)
		if err != nil {
			logf("shadow: hash-object %s: %v", abs, err)
			return
		}
		rec.Blob = blob
	}
	if err := s.appendRecord(rec); err != nil {
		logf("shadow: append capture record: %v", err)
	}
}

// CommitTurnAsync runs the turn-boundary tree commit in the background: in
// every known root, refresh the index (`add -A`) and commit only if the
// tree actually changed — the ADR-measured no-op pair (`add -A` + `status
// --porcelain`, warm index, ~0 ms) keeps clean turns free. Checking every
// known root rather than only capture-dirtied ones is what catches
// Bash-driven mutations (`sed -i` carries no Path on any wire); the status
// check is the "dirtied that turn" test. Each committed turn appends a
// turn_commit record to the index so per-tool diffs can resolve their "next
// state" side. `git gc --auto` runs after any commit.
func (s *Store) CommitTurnAsync() {
	if s == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.commitTurn()
	}()
}

func (s *Store) commitTurn() {
	s.mu.Lock()
	roots := make([]*rootState, 0, len(s.roots))
	for _, rs := range s.roots {
		roots = append(roots, rs)
	}
	s.mu.Unlock()

	for _, rs := range roots {
		rs.mu.Lock()
		if !rs.baselined {
			// Baseline still running (or failed): committing now would race
			// it or produce a bogus first commit. This turn's changes are
			// still caught — the eventual baseline snapshots the tree as it
			// stands, and later turns commit deltas from there.
			rs.mu.Unlock()
			continue
		}
		commit, err := s.commitRoot(rs)
		rs.mu.Unlock()
		if err != nil {
			logf("shadow: turn commit in %s: %v", rs.path, err)
			continue
		}
		if commit == "" {
			continue // clean root — no-op turn here
		}
		if err := s.appendRecord(Record{
			Kind:    RecordTurnCommit,
			Root:    rs.path,
			RootKey: RootKey(rs.path),
			Commit:  commit,
			Time:    time.Now().UTC(),
		}); err != nil {
			logf("shadow: append turn_commit record: %v", err)
		}
		if err := s.git(gitTimeout, rs.gitDir, "", "gc", "--auto", "--quiet"); err != nil {
			logf("shadow: gc --auto in %s: %v", rs.gitDir, err)
		}
	}
}

// rootFor resolves the git root containing path's directory, cached per
// directory. A non-git directory (rev-parse fails, including a directory
// that doesn't exist yet) counts as its own root.
func (s *Store) rootFor(absPath string) string {
	dir := filepath.Dir(absPath)
	s.mu.Lock()
	if root, ok := s.rootByDir[dir]; ok {
		s.mu.Unlock()
		return root
	}
	s.mu.Unlock()

	root := dir
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err == nil {
		if top := strings.TrimSpace(string(out)); top != "" {
			root = top
		}
	}

	s.mu.Lock()
	s.rootByDir[dir] = root
	s.mu.Unlock()
	return root
}

// rootState returns (creating and git-initializing if needed) the state for
// an absolute root path. Returns nil when the shadow git dir can't be
// initialized — the caller's signal to silently skip.
func (s *Store) rootState(root string) *rootState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rs, ok := s.roots[root]; ok {
		return rs
	}
	rs := &rootState{path: root, gitDir: filepath.Join(s.dir, RootKey(root))}
	if err := s.initGitDir(rs); err != nil {
		logf("shadow: init shadow git dir for %s: %v", root, err)
		return nil
	}
	s.roots[root] = rs
	return rs
}

// initGitDir creates rs.gitDir as a git dir if it isn't one yet (fast,
// idempotent — safe on the capture hot path). Configuration set here:
//   - user.name/user.email: commit identity, no dependence on user config
//   - commit.gpgsign off: never invoke the user's signing setup
//   - gc.pruneExpire never: captured pre-image blobs are referenced only by
//     the JSONL index, not by any git ref — default gc would prune them as
//     unreachable after two weeks, silently destroying history
//   - gc.autoDetach off: no orphaned background gc processes
//   - mads.root: the root's actual path, for reverse lookup from a root_key
//   - info/exclude: the mads exclude list (excludeList)
func (s *Store) initGitDir(rs *rootState) error {
	if _, err := os.Stat(filepath.Join(rs.gitDir, "HEAD")); err == nil {
		return nil // already initialized (resume/bounce)
	}
	if err := os.MkdirAll(rs.gitDir, 0o755); err != nil {
		return err
	}
	if err := s.git(gitTimeout, rs.gitDir, "", "init", "--quiet"); err != nil {
		return err
	}
	for _, kv := range [][2]string{
		{"user.name", "mads-shadow"},
		{"user.email", "shadow@mads.local"},
		{"commit.gpgsign", "false"},
		{"gc.pruneExpire", "never"},
		{"gc.autoDetach", "false"},
		{"mads.root", rs.path},
	} {
		if err := s.git(gitTimeout, rs.gitDir, "", "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	exclude := filepath.Join(rs.gitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return err
	}
	return os.WriteFile(exclude, []byte(strings.Join(excludeList, "\n")+"\n"), 0o644)
}

// baselineRoot creates the root's baseline commit if it doesn't exist yet.
// Holds the root's tree lock for the duration, so turn commits queue behind
// it rather than racing.
func (s *Store) baselineRoot(rs *rootState) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.baselined {
		return
	}
	// A previous process (bounce/resume) may have baselined already.
	if s.git(gitTimeout, rs.gitDir, "", "rev-parse", "--quiet", "--verify", BaselineTag+"^{commit}") == nil {
		rs.baselined = true
		return
	}
	if err := s.git(baselineTimeout, rs.gitDir, rs.path, "add", "-A"); err != nil {
		logf("shadow: baseline add in %s: %v", rs.path, err)
		return
	}
	// --allow-empty: an empty (or fully ignored) root still needs a
	// baseline commit for the Router to diff against.
	if err := s.git(baselineTimeout, rs.gitDir, rs.path,
		"commit", "--quiet", "--allow-empty", "-m", "mads shadow baseline: "+rs.path); err != nil {
		logf("shadow: baseline commit in %s: %v", rs.path, err)
		return
	}
	if err := s.git(gitTimeout, rs.gitDir, "", "tag", "-f", BaselineTag); err != nil {
		logf("shadow: tag baseline in %s: %v", rs.path, err)
		return
	}
	rs.baselined = true
}

// commitRoot refreshes the index and commits if anything changed, returning
// the new commit hash ("" for a clean tree). Caller holds rs.mu.
func (s *Store) commitRoot(rs *rootState) (string, error) {
	if err := s.git(baselineTimeout, rs.gitDir, rs.path, "add", "-A"); err != nil {
		return "", err
	}
	out, err := s.gitOutput(gitTimeout, rs.gitDir, rs.path, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", nil
	}
	if err := s.git(baselineTimeout, rs.gitDir, rs.path,
		"commit", "--quiet", "-m", "mads shadow turn"); err != nil {
		return "", err
	}
	hash, err := s.gitOutput(gitTimeout, rs.gitDir, "", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(hash), nil
}

func (s *Store) hashObject(gitDir, path string) (string, error) {
	out, err := s.gitOutput(gitTimeout, gitDir, "", "hash-object", "-w", "--", path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// git runs one git command against a shadow git dir (and optionally a work
// tree), discarding output.
func (s *Store) git(timeout time.Duration, gitDir, workTree string, args ...string) error {
	_, err := s.gitOutput(timeout, gitDir, workTree, args...)
	return err
}

func (s *Store) gitOutput(timeout time.Duration, gitDir, workTree string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	full := []string{"--git-dir=" + gitDir}
	if workTree != "" {
		full = append(full, "--work-tree="+workTree)
	}
	full = append(full, args...)
	out, err := exec.CommandContext(ctx, "git", full...).Output() //nolint:gosec // args are internally constructed
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func logf(format string, args ...any) {
	log.Printf(format, args...)
}
