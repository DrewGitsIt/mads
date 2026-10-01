package shadow

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitRun runs one git command in dir, failing the test on error — the same
// helper shape services/router/internal/api's diff tests use.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// tempRoot is t.TempDir() in the canonical form this package records
// (RealPath): on macOS the temp dir lives under /var, a symlink into
// /private/var, so a raw t.TempDir() is not the path git — nor the store —
// reports back for it. Workspace roots in these fixtures come from here so
// the assertions compare like with like on every OS; shadow dirs still use
// t.TempDir() directly, since nothing compares a recorded path to those.
func tempRoot(t *testing.T) string {
	t.Helper()
	return RealPath(t.TempDir())
}

// newRepo initializes a git repo with one committed file and returns its
// path.
func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := tempRoot(t)
	gitRun(t, dir, "init", "-q")
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "seed")
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBaseline: baselining a root produces a tagged commit whose tree holds
// the tracked content, honors the user repo's ignore rules, and is
// idempotent (a resumed session must not stack baselines).
func TestBaseline(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string // returns root
		present map[string]string         // rel path → content at baseline
		absent  []string                  // rel paths that must not be in the baseline
	}{
		{
			name: "git repo honors gitignore",
			setup: func(t *testing.T) string {
				root := newRepo(t, map[string]string{"a.txt": "hello\n", ".gitignore": ".env\n"})
				writeFile(t, filepath.Join(root, ".env"), "SECRET=x\n")
				writeFile(t, filepath.Join(root, "b.txt"), "uncommitted\n")
				return root
			},
			present: map[string]string{"a.txt": "hello\n", "b.txt": "uncommitted\n"},
			absent:  []string{".env"},
		},
		{
			name: "non-git dir is its own root",
			setup: func(t *testing.T) string {
				root := tempRoot(t)
				writeFile(t, filepath.Join(root, "notes.md"), "plain\n")
				return root
			},
			present: map[string]string{"notes.md": "plain\n"},
		},
		{
			name: "mads exclude list skips build dirs",
			setup: func(t *testing.T) string {
				root := tempRoot(t)
				writeFile(t, filepath.Join(root, "src.go"), "package x\n")
				writeFile(t, filepath.Join(root, "node_modules", "dep.js"), "junk\n")
				return root
			},
			present: map[string]string{"src.go": "package x\n"},
			absent:  []string{"node_modules/dep.js"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.setup(t)
			s := New(t.TempDir(), "ses_test")
			s.Baseline(root)
			s.Baseline(root) // idempotent — must not fail or re-commit

			gitDir := filepath.Join(s.Dir(), RootKey(root))
			for rel, want := range tc.present {
				got, ok := BaselineOriginal(context.Background(), s.Dir(), root, rel)
				if !ok || got != want {
					t.Errorf("BaselineOriginal(%s) = (%q, %v), want (%q, true)", rel, got, ok, want)
				}
			}
			for _, rel := range tc.absent {
				if got, ok := BaselineOriginal(context.Background(), s.Dir(), root, rel); !ok || got != "" {
					t.Errorf("BaselineOriginal(%s) = (%q, %v), want excluded (\"\", true)", rel, got, ok)
				}
			}
			// Reverse lookup: the root's actual path lives in the shadow config.
			out, err := exec.Command("git", "--git-dir="+gitDir, "config", "mads.root").Output()
			if err != nil || strings.TrimSpace(string(out)) != root {
				t.Errorf("mads.root = %q (%v), want %q", strings.TrimSpace(string(out)), err, root)
			}
			// Exactly one commit: the second Baseline call was a no-op.
			out, err = exec.Command("git", "--git-dir="+gitDir, "rev-list", "--count", BaselineTag).Output()
			if err != nil || strings.TrimSpace(string(out)) != "1" {
				t.Errorf("baseline commit count = %q (%v), want 1", strings.TrimSpace(string(out)), err)
			}
		})
	}
}

// TestBaselineOriginalNoShadow: a root that was never baselined reports
// ok=false — the caller's signal to fall back to its heuristic.
func TestBaselineOriginalNoShadow(t *testing.T) {
	if _, ok := BaselineOriginal(context.Background(), t.TempDir(), "/nope", "a.txt"); ok {
		t.Error("BaselineOriginal on an empty shadow dir = ok, want fallback signal")
	}
}

// TestCapture: the pre-image capture table — blob for a normal file,
// missing marker for a not-yet-created file, too-large marker over the
// guard, and paths outside any git repo still captured (path-agnostic).
func TestCapture(t *testing.T) {
	repo := newRepo(t, map[string]string{"tracked.txt": "v1\n"})
	plain := tempRoot(t)
	big := strings.Repeat("x", MaxBlobBytes+1)
	writeFile(t, filepath.Join(repo, "big.bin"), big)
	writeFile(t, filepath.Join(plain, "loose.txt"), "loose\n")

	tests := []struct {
		name      string
		toolUseID string
		path      string
		wantBlob  string // expected content behind the recorded blob
		missing   bool
		tooLarge  bool
		root      string
	}{
		{name: "existing file in repo", toolUseID: "toolu_1", path: filepath.Join(repo, "tracked.txt"), wantBlob: "v1\n", root: repo},
		{name: "file outside any repo", toolUseID: "toolu_2", path: filepath.Join(plain, "loose.txt"), wantBlob: "loose\n", root: plain},
		{name: "absent file records missing", toolUseID: "toolu_3", path: filepath.Join(repo, "new.txt"), missing: true, root: repo},
		{name: "over guard records too-large", toolUseID: "toolu_4", path: filepath.Join(repo, "big.bin"), tooLarge: true, root: repo},
	}

	s := New(t.TempDir(), "ses_test")
	t.Cleanup(s.Wait)
	for _, tc := range tests {
		s.Capture(tc.toolUseID, tc.path)
	}

	recs, err := ReadIndex(s.Dir())
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, _, ok := FindCapture(recs, tc.toolUseID)
			if !ok {
				t.Fatalf("no capture recorded for %s", tc.toolUseID)
			}
			if rec.Path != tc.path || rec.Root != tc.root || rec.RootKey != RootKey(tc.root) {
				t.Errorf("record = %+v, want path %s in root %s", rec, tc.path, tc.root)
			}
			if rec.Missing != tc.missing || rec.TooLarge != tc.tooLarge {
				t.Errorf("markers = missing:%v too_large:%v, want missing:%v too_large:%v",
					rec.Missing, rec.TooLarge, tc.missing, tc.tooLarge)
			}
			if tc.missing || tc.tooLarge {
				if rec.Blob != "" {
					t.Errorf("marker record carries blob %q, want none", rec.Blob)
				}
				return
			}
			got, err := BlobContent(context.Background(), s.Dir(), rec.RootKey, rec.Blob)
			if err != nil || got != tc.wantBlob {
				t.Errorf("BlobContent = (%q, %v), want %q", got, err, tc.wantBlob)
			}
		})
	}

	if rec, _, ok := FindCapture(recs, "toolu_4"); !ok || rec.Size <= MaxBlobBytes {
		t.Errorf("too-large record size = %d, want > %d", rec.Size, MaxBlobBytes)
	}
}

// TestMultiRootDiscovery: paths across two repos and a plain directory land
// in three distinct per-root shadow git dirs, each recording its root.
func TestMultiRootDiscovery(t *testing.T) {
	repoA := newRepo(t, map[string]string{"a.txt": "a\n"})
	repoB := newRepo(t, map[string]string{"b.txt": "b\n"})
	plain := tempRoot(t)
	writeFile(t, filepath.Join(plain, "c.txt"), "c\n")
	// A file in a subdirectory must resolve to the repo root, not the subdir.
	writeFile(t, filepath.Join(repoA, "sub", "deep.txt"), "deep\n")

	s := New(t.TempDir(), "ses_test")
	t.Cleanup(s.Wait)
	s.Capture("t_a", filepath.Join(repoA, "a.txt"))
	s.Capture("t_deep", filepath.Join(repoA, "sub", "deep.txt"))
	s.Capture("t_b", filepath.Join(repoB, "b.txt"))
	s.Capture("t_c", filepath.Join(plain, "c.txt"))

	recs, err := ReadIndex(s.Dir())
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	wantRoots := map[string]string{"t_a": repoA, "t_deep": repoA, "t_b": repoB, "t_c": plain}
	keys := map[string]bool{}
	for id, root := range wantRoots {
		rec, _, ok := FindCapture(recs, id)
		if !ok {
			t.Fatalf("no capture for %s", id)
		}
		if rec.Root != root {
			t.Errorf("%s root = %q, want %q", id, rec.Root, root)
		}
		keys[rec.RootKey] = true
		if _, err := os.Stat(filepath.Join(s.Dir(), rec.RootKey, "HEAD")); err != nil {
			t.Errorf("no shadow git dir for root %s: %v", root, err)
		}
	}
	if len(keys) != 3 {
		t.Errorf("distinct root keys = %d, want 3", len(keys))
	}
}

// TestCommitTurn: a dirtied root gets one turn commit (recorded in the
// index, content readable via FileAtCommit); a clean root's turn end is a
// no-op — no commit, no index growth, and cheap.
func TestCommitTurn(t *testing.T) {
	root := newRepo(t, map[string]string{"a.txt": "v1\n"})
	s := New(t.TempDir(), "ses_test")
	t.Cleanup(s.Wait)
	s.Baseline(root)
	// Register the root the way real traffic does, so commitTurn sees it.
	s.Capture("toolu_edit", filepath.Join(root, "a.txt"))

	writeFile(t, filepath.Join(root, "a.txt"), "v2\n")
	s.commitTurn()

	recs, err := ReadIndex(s.Dir())
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	var commit Record
	for _, rec := range recs {
		if rec.Kind == RecordTurnCommit {
			commit = rec
		}
	}
	if commit.Commit == "" {
		t.Fatalf("no turn_commit record after a dirty turn; index: %+v", recs)
	}
	got, err := FileAtCommit(context.Background(), s.Dir(), commit.RootKey, commit.Commit, "a.txt")
	if err != nil || got != "v2\n" {
		t.Errorf("FileAtCommit = (%q, %v), want v2", got, err)
	}

	// No-op turn: nothing changed since the commit above — the index must
	// not grow and the pass must be quick (warm index + status check; the
	// ADR budget is ~0 ms, the bound here is generous purely against
	// pathological regressions like re-committing every turn).
	before := len(recs)
	start := time.Now()
	s.commitTurn()
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("no-op turn commit took %v, want ~instant", d)
	}
	recs, err = ReadIndex(s.Dir())
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(recs) != before {
		t.Errorf("no-op turn grew the index from %d to %d records", before, len(recs))
	}
}

// TestCommitTurnSkipsUnbaselinedRoot: a root whose baseline hasn't landed
// yet is skipped by the turn commit (never raced), not errored.
func TestCommitTurnSkipsUnbaselinedRoot(t *testing.T) {
	root := tempRoot(t)
	writeFile(t, filepath.Join(root, "a.txt"), "v1\n")
	s := New(t.TempDir(), "ses_test")
	// Register the root without baselining it — the deterministic stand-in
	// for "primary baseline still running in the background".
	rs := s.rootState(root)
	if rs == nil {
		t.Fatal("rootState returned nil")
	}

	s.commitTurn()
	if _, err := ReadIndex(s.Dir()); !os.IsNotExist(err) {
		t.Errorf("index after skipped turn commit: err = %v, want not-exist (no records)", err)
	}
	if err := exec.Command("git", "--git-dir="+rs.gitDir, "rev-parse", "--verify", "HEAD").Run(); err == nil {
		t.Error("un-baselined root has a HEAD commit — turn commit should have skipped it")
	}
}

// TestNilStore: every method is a safe no-op on a nil *Store — the
// runner's "shadowing disabled" wiring calls them unconditionally.
func TestNilStore(t *testing.T) {
	var s *Store
	s.Baseline("/tmp")
	s.Capture("toolu_x", "/tmp/x")
	s.CommitTurnAsync()
	if s.Dir() != "" {
		t.Errorf("nil Dir() = %q, want empty", s.Dir())
	}
}

// TestReadIndexTolerance: malformed lines (a torn final write) are
// skipped, not fatal, and a missing index reads as not-exist.
func TestReadIndexTolerance(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadIndex(dir); !os.IsNotExist(err) {
		t.Errorf("missing index error = %v, want not-exist", err)
	}
	lines := bytes.Join([][]byte{
		[]byte(`{"kind":"capture","tool_use_id":"toolu_ok","path":"/p","root":"/r","root_key":"abc","blob":"b"}`),
		[]byte(`{"kind":"capt`), // torn
	}, []byte("\n"))
	if err := os.WriteFile(filepath.Join(dir, IndexFile), lines, 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadIndex(dir)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if len(recs) != 1 || recs[0].ToolUseID != "toolu_ok" {
		t.Errorf("records = %+v, want just toolu_ok", recs)
	}
}

// TestSymlinkedRootSharesOneIdentity: a workspace reached through a symlink
// must land in the same shadow root as the physical path, and a record must
// be internally consistent — Path under Root, so Rel(Root, Path) is the
// plain relative name the Router asks its baseline for.
//
// This is the defect a first macOS CI run exposed (there /var is a symlink
// into /private/var, so every t.TempDir() took this path), but nothing about
// it is macOS-specific: before RealPath, Root came back physical from git
// while Path stayed symlinked, Rel went up and out with ../.., the baseline
// lookup missed, and a modified file was served with an empty original.
func TestSymlinkedRootSharesOneIdentity(t *testing.T) {
	repo := newRepo(t, map[string]string{"tracked.txt": "v1\n"})
	link := filepath.Join(tempRoot(t), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	s := New(t.TempDir(), "ses_test")
	t.Cleanup(s.Wait)
	s.Capture("t_physical", filepath.Join(repo, "tracked.txt"))
	s.Capture("t_symlinked", filepath.Join(link, "tracked.txt"))

	recs, err := ReadIndex(s.Dir())
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	physical, _, ok := FindCapture(recs, "t_physical")
	if !ok {
		t.Fatal("no capture through the physical path")
	}
	linked, _, ok := FindCapture(recs, "t_symlinked")
	if !ok {
		t.Fatal("no capture through the symlink")
	}

	// One root, one shadow git dir, one blob: the symlink is not a second
	// workspace, and the Router keys on RootKey alone.
	if linked.Root != physical.Root || linked.RootKey != physical.RootKey {
		t.Errorf("symlinked root = %q (key %s), want %q (key %s)",
			linked.Root, linked.RootKey, physical.Root, physical.RootKey)
	}
	if linked.Blob != physical.Blob {
		t.Errorf("symlinked blob = %q, want the same pre-image as %q", linked.Blob, physical.Blob)
	}
	for _, rec := range []Record{physical, linked} {
		rel, err := filepath.Rel(rec.Root, rec.Path)
		if err != nil || rel != "tracked.txt" {
			t.Errorf("Rel(%q, %q) = %q, %v; want \"tracked.txt\"", rec.Root, rec.Path, rel, err)
		}
	}

	// And the baseline the Router reads answers for that relative name.
	s.Baseline(link)
	original, ok := BaselineOriginal(context.Background(), s.Dir(), linked.Root, "tracked.txt")
	if !ok || original != "v1\n" {
		t.Errorf("BaselineOriginal = %q, %v; want \"v1\\n\", true", original, ok)
	}
}
