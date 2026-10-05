package sessionwt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/mergedriver"
)

// TestMain pins mergiraf to "not installed" for the whole package, so a
// developer's own mergiraf can't change what these scratch-repo merges do.
// The tests below that want a driver set findMergiraf themselves.
func TestMain(m *testing.M) {
	findMergiraf = func() string { return "" }
	os.Exit(m.Run())
}

// withMergiraf makes path the mergiraf ensure finds, for one test.
func withMergiraf(t *testing.T, path string) {
	t.Helper()
	saved := findMergiraf
	findMergiraf = func() string { return path }
	t.Cleanup(func() { findMergiraf = saved })
}

// fakeMergiraf is a stand-in driver with mergiraf's calling convention
// (`merge --git %O %A %B ...`) that resolves every conflict by keeping both
// sides' lines (git merge-file --union) — for the plumbing tests, which
// need a driver that resolves what plain git can't, deterministically.
func fakeMergiraf(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mergiraf")
	script := "#!/bin/sh\nexec git merge-file --union \"$4\" \"$3\" \"$5\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// realMergirafOrSkip is the real mergiraf binary: $MADS_TEST_MERGIRAF, else
// PATH. It's an optional runtime tool, so tests that need it skip without.
func realMergirafOrSkip(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MADS_TEST_MERGIRAF"); p != "" {
		if _, err := exec.LookPath(p); err != nil {
			t.Fatalf("MADS_TEST_MERGIRAF=%s: %v", p, err)
		}
		return p
	}
	p, err := exec.LookPath("mergiraf")
	if err != nil {
		t.Skip("mergiraf not installed (set MADS_TEST_MERGIRAF or put it on PATH); skipping")
	}
	return p
}

func attributes(t *testing.T, repo string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repo, ".git", "info", "attributes"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

// Ensure registers mergiraf for the repo when it's installed, and doing it
// again (a later ensure, another process's ensure) changes nothing.
func TestEnsureRegistersMergeDriver(t *testing.T) {
	driver := fakeMergiraf(t)
	withMergiraf(t, driver)
	s := newSpec(t)
	res := ensure(t, s)

	if got := mergedriver.Registered(context.Background(), s.Primary); got != mergedriver.Command(driver) {
		t.Errorf("driver = %q, want %q", got, mergedriver.Command(driver))
	}
	if got := mergedriver.Registered(context.Background(), res.Dir); got != mergedriver.Command(driver) {
		t.Errorf("the session folder sees driver %q, want the repo's", got)
	}
	if got := attributes(t, s.Primary); got != "* merge=mergiraf\n" {
		t.Errorf("attributes = %q", got)
	}

	registered.Delete(strings.TrimSpace(gitOut(t, s.Primary, "rev-parse", "--path-format=absolute", "--git-common-dir")))
	if _, err := Ensure(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got := attributes(t, s.Primary); got != "* merge=mergiraf\n" {
		t.Errorf("attributes after a second ensure = %q, want the rule once", got)
	}
	if st := gitOut(t, res.Dir, "status", "--porcelain"); st != "" {
		t.Errorf("registration dirtied the session folder: %q", st)
	}
}

// The user's own checkout may itself be a linked worktree: the driver
// still lands in the shared git dir, not under a bogus joined path.
func TestEnsureRegistersMergeDriverFromLinkedCheckout(t *testing.T) {
	driver := fakeMergiraf(t)
	withMergiraf(t, driver)
	main := initRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitRun(t, main, "worktree", "add", "-q", "-b", "feature", linked)

	s := Spec{Primary: linked, Root: t.TempDir(), SessionID: "ses_linked", Base: "feature"}
	ensure(t, s)

	if got := attributes(t, main); got != "* merge=mergiraf\n" {
		t.Errorf("attributes in the main checkout's .git = %q", got)
	}
	if got := mergedriver.Registered(context.Background(), main); got != mergedriver.Command(driver) {
		t.Errorf("driver seen from the main checkout = %q", got)
	}
	bogus := filepath.Join(linked, main, ".git", "info")
	if _, err := os.Stat(bogus); !os.IsNotExist(err) {
		t.Errorf("attributes went to a double-joined path: %s", bogus)
	}
}

// Without mergiraf, ensure writes nothing: today's behaviour exactly.
func TestEnsureWithoutMergirafRegistersNothing(t *testing.T) {
	s := newSpec(t)
	ensure(t, s)
	if got := mergedriver.Registered(context.Background(), s.Primary); got != "" {
		t.Errorf("driver = %q, want none", got)
	}
	if got := attributes(t, s.Primary); got != "" {
		t.Errorf("attributes = %q, want none", got)
	}
}

// conflictingWork leaves the session and the base each changing the same
// line of shared.txt: plain git conflicts, the fake driver keeps both.
func conflictingWork(t *testing.T, s *isolated) {
	t.Helper()
	commitFile(t, s.primary, "shared.txt", "one\n")
	gitRun(t, s.folder, "merge", "-q", "main")
	s.work(t, map[string]string{"shared.txt": "session\n"})
	s.commit(t, contracts.CommitRequest{Message: "Session edit"})
	commitFile(t, s.primary, "shared.txt", "base\n")
}

// The dry run names exactly the files plain git would leave conflicted,
// whatever the registered driver would do with them, and touches nothing.
func TestPlainConflicts(t *testing.T) {
	withMergiraf(t, fakeMergiraf(t))
	s := newIsolated(t)
	conflictingWork(t, s)
	commitFile(t, s.primary, "other.txt", "clean\n")
	before := cleanTree(t, s.folder)

	files, ok := plainConflicts(context.Background(), s.folder, "main")
	if !ok || !slices.Equal(files, []string{"shared.txt"}) {
		t.Fatalf("plainConflicts = %v, %v; want [shared.txt]", files, ok)
	}
	if cleanTree(t, s.folder) != before {
		t.Error("the dry run changed the session folder")
	}

	gitRun(t, s.folder, "merge", "-q", "main") // the fake driver resolves it
	if files, ok := plainConflicts(context.Background(), s.folder, "main"); !ok || len(files) != 0 {
		t.Errorf("after merging, plainConflicts = %v, %v; want none", files, ok)
	}
}

// Land stops after step 1 when mergiraf resolved files plain git would
// have conflicted on: the merge is committed in the session folder, the
// user's checkout is untouched, and the next Land lands it.
func TestLandStopsOnAutoResolved(t *testing.T) {
	withMergiraf(t, fakeMergiraf(t))
	s := newIsolated(t)
	conflictingWork(t, s)
	primaryBefore := cleanTree(t, s.primary)

	res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.LandAutoResolved || !slices.Equal(res.Files, []string{"shared.txt"}) {
		t.Fatalf("result = %+v", res)
	}
	if res.MergeCommit != head(t, s.folder, "HEAD") {
		t.Errorf("merge commit %s isn't the session folder's HEAD", res.MergeCommit)
	}
	if parents := strings.Fields(gitOut(t, s.folder, "log", "-1", "--format=%P")); len(parents) != 2 || parents[1] != head(t, s.primary, "main") {
		t.Errorf("step-1 merge has parents %v, want the session tip and main", parents)
	}
	if cleanTree(t, s.primary) != primaryBefore {
		t.Error("an auto-resolved Land touched the user's checkout")
	}
	if got := strings.Join(s.store.eventTypes(), ","); !strings.HasSuffix(got, "merge.attempted,merge.auto_resolved") {
		t.Errorf("events = %s", got)
	}
	p := s.store.lastEvent(t, contracts.MergeAutoResolved)
	if p["kind"] != "land" || p["base"] != "main" || p["merge_commit"] != res.MergeCommit {
		t.Errorf("merge.auto_resolved = %v", p)
	}
	if files, _ := p["files"].([]any); len(files) != 1 || files[0] != "shared.txt" {
		t.Errorf("merge.auto_resolved files = %v", p["files"])
	}
	last := s.store.reports[len(s.store.reports)-1]
	if last.Source != contracts.CommitSourceLandBase || last.Hash != res.MergeCommit || last.Message != "Merge main into mads/session/ses_test" {
		t.Errorf("recorded %+v", last)
	}
	if _, l := s.store.lastCounts(t); l != 2 {
		t.Errorf("unlanded = %d, want 2 (the session commit and the merge)", l)
	}

	// The second click finds the base already merged and lands.
	res, err = s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.LandLanded || res.MergeCommit != head(t, s.primary, "main") {
		t.Fatalf("second Land = %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(s.primary, "shared.txt"))
	if string(got) != "session\nbase\n" && string(got) != "base\nsession\n" {
		t.Errorf("landed shared.txt = %q, want the resolved merge", got)
	}
}

// A base that merges cleanly under plain git still lands in one click with
// mergiraf registered.
func TestLandPlainCleanMergeWithDriverIsOneClick(t *testing.T) {
	withMergiraf(t, fakeMergiraf(t))
	s := newIsolated(t)
	s.work(t, map[string]string{"feature.txt": "f\n"})
	s.commit(t, contracts.CommitRequest{Message: "Feature"})
	commitFile(t, s.primary, "elsewhere.txt", "base moved on\n")

	res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.LandLanded {
		t.Fatalf("result = %+v, want landed in one click", res)
	}
	if got := strings.Join(s.store.eventTypes(), ","); got != "merge.attempted,merge.completed" {
		t.Errorf("events = %s", got)
	}
}

// A real conflict stays a conflict when the driver can't resolve it; here a
// driver that always fails stands in for mergiraf giving up.
func TestLandConflictWithDriverStillConflicts(t *testing.T) {
	failing := filepath.Join(t.TempDir(), "mergiraf")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withMergiraf(t, failing)
	s := newIsolated(t)
	conflictingWork(t, s)

	res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.LandConflicted || !slices.Equal(res.Files, []string{"shared.txt"}) {
		t.Fatalf("result = %+v, want conflicted", res)
	}
}

// A driver registered by a mergiraf that has since been uninstalled would
// fail every file both sides changed; Land merges those with plain git.
func TestLandDanglingDriverFallsBackToPlainGit(t *testing.T) {
	s := newIsolated(t)
	if err := mergedriver.Register(context.Background(), s.primary, filepath.Join(t.TempDir(), "gone", "mergiraf")); err != nil {
		t.Fatal(err)
	}
	commitFile(t, s.primary, "shared.txt", "a\nb\nc\nd\ne\nf\n")
	gitRun(t, s.folder, "-c", "merge.mergiraf.driver=git merge-file %A %O %B", "merge", "-q", "main")
	s.work(t, map[string]string{"shared.txt": "A\nb\nc\nd\ne\nf\n"})
	s.commit(t, contracts.CommitRequest{Message: "Session edit"})
	commitFile(t, s.primary, "shared.txt", "a\nb\nc\nd\ne\nF\n")

	res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.LandLanded {
		t.Fatalf("result = %+v, want plain git to land it", res)
	}
	if got, _ := os.ReadFile(filepath.Join(s.primary, "shared.txt")); string(got) != "A\nb\nc\nd\ne\nF\n" {
		t.Errorf("landed shared.txt = %q", got)
	}
}

const (
	adjacentGo       = "package sample\n\nfunc Sample() {\n\tx := 1\n\ty := 2\n\t_ = x\n\t_ = y\n}\n"
	adjacentGoBase   = "package sample\n\nfunc Sample() {\n\tx := 100\n\ty := 2\n\t_ = x\n\t_ = y\n}\n"
	adjacentGoSess   = "package sample\n\nfunc Sample() {\n\tx := 1\n\ty := 200\n\t_ = x\n\t_ = y\n}\n"
	adjacentGoMerged = "package sample\n\nfunc Sample() {\n\tx := 100\n\ty := 200\n\t_ = x\n\t_ = y\n}\n"
)

// The real thing: adjacent edits to one Go file conflict under plain git,
// mergiraf resolves them, Land stops to show it, then lands on the second
// click with both edits.
func TestLandAutoResolvedWithRealMergiraf(t *testing.T) {
	withMergiraf(t, realMergirafOrSkip(t))
	s := newIsolated(t)
	commitFile(t, s.primary, "sample.go", adjacentGo)
	gitRun(t, s.folder, "merge", "-q", "main")
	s.work(t, map[string]string{"sample.go": adjacentGoSess})
	s.commit(t, contracts.CommitRequest{Message: "Session edit"})
	commitFile(t, s.primary, "sample.go", adjacentGoBase)
	primaryBefore := cleanTree(t, s.primary)

	res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.LandAutoResolved || !slices.Equal(res.Files, []string{"sample.go"}) {
		t.Fatalf("result = %+v, want auto_resolved [sample.go]", res)
	}
	if got, _ := os.ReadFile(filepath.Join(s.folder, "sample.go")); string(got) != adjacentGoMerged {
		t.Errorf("session folder sample.go = %q, want %q", got, adjacentGoMerged)
	}
	if cleanTree(t, s.primary) != primaryBefore {
		t.Error("an auto-resolved Land touched the user's checkout")
	}

	res, err = s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.LandLanded {
		t.Fatalf("second Land = %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(s.primary, "sample.go")); string(got) != adjacentGoMerged {
		t.Errorf("landed sample.go = %q", got)
	}
}

// Real mergiraf doesn't paper over a genuine conflict: the same line
// changed both ways still comes back conflicted.
func TestLandOverlappingEditsConflictWithRealMergiraf(t *testing.T) {
	withMergiraf(t, realMergirafOrSkip(t))
	s := newIsolated(t)
	commitFile(t, s.primary, "sample.go", adjacentGo)
	gitRun(t, s.folder, "merge", "-q", "main")
	s.work(t, map[string]string{"sample.go": strings.Replace(adjacentGo, "x := 1", "x := 7", 1)})
	s.commit(t, contracts.CommitRequest{Message: "Session edit"})
	commitFile(t, s.primary, "sample.go", strings.Replace(adjacentGo, "x := 1", "x := 9", 1))

	res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contracts.LandConflicted || !slices.Equal(res.Files, []string{"sample.go"}) {
		t.Fatalf("result = %+v, want conflicted", res)
	}
}
