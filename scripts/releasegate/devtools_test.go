package releasegate

// Not tagged: this runs in every `go test ./...` (and CI), so a release
// program that starts linking dev-only code fails long before a release.
// scripts/release-gate.sh makes the same check before it builds anything.

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const module = "github.com/drewkidwell/mads"

// devOnlyTrees are package trees no shipped program may link: the perf
// harness's tools (tools/perf, behind the `perf` build tag), its seeder, and
// anything under the UI's end-to-end suites. Keep in step with
// DEV_ONLY_PKGS in scripts/release-gate.sh. A tree is the package and
// everything under it.
var devOnlyTrees = []string{
	module + "/tools",
	module + "/services/data/cmd/perfseed",
	module + "/ui/e2e",
}

// extraPrograms are built by `make build` but not by the release gate's
// matrix; checked anyway, so nothing built from this repo links dev code.
var extraPrograms = []string{"services/mesh"}

func devOnly(pkg string) bool {
	for _, tree := range devOnlyTrees {
		if pkg == tree || strings.HasPrefix(pkg, tree+"/") {
			return true
		}
	}
	return false
}

// releasePrograms reads the release gate's own program list (its
// `RELEASE_PKGS="..."` line), so this test checks exactly what ships.
func releasePrograms(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "scripts", "release-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^RELEASE_PKGS="([^"]+)"`).FindSubmatch(b)
	if m == nil {
		t.Fatal(`scripts/release-gate.sh has no RELEASE_PKGS="..." line`)
	}
	return append(strings.Fields(string(m[1])), extraPrograms...)
}

func TestDevOnlyMatcher(t *testing.T) {
	for pkg, want := range map[string]bool{
		module + "/tools/perf/gitshim":            true,
		module + "/tools":                         true,
		module + "/services/data/cmd/perfseed":    true,
		module + "/ui/e2e/perf":                   true,
		module + "/services/data":                 false,
		module + "/services/data/internal/store":  false,
		module + "/toolsbox":                      false,
		"github.com/someone/tools/x":              false,
		module + "/services/data/cmd/perfseedish": false,
	} {
		if got := devOnly(pkg); got != want {
			t.Errorf("devOnly(%q) = %v, want %v", pkg, got, want)
		}
	}
}

// TestReleaseProgramsLinkNoDevTools fails if any release program's
// dependency graph (as a release builds it: no build tags) contains a
// dev-only package.
func TestReleaseProgramsLinkNoDevTools(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, prog := range releasePrograms(t, root) {
		cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./"+prog)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			stderr := ""
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = string(ee.Stderr)
			}
			t.Fatalf("go list -deps ./%s: %v\n%s", prog, err, stderr)
		}
		for _, pkg := range strings.Fields(string(out)) {
			if devOnly(pkg) {
				t.Errorf("release program %s links dev-only package %s (tools/, perfseed and ui/e2e must never ship)", prog, pkg)
			}
		}
	}
}

// TestSourceArchiveOmitsDevTools: `git archive` of HEAD (with this
// checkout's .gitattributes) leaves the export-ignore'd dev trees out.
// Skipped outside a git checkout (e.g. a build from a source tarball).
func TestSourceArchiveOmitsDevTools(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "HEAD").Run(); err != nil {
		t.Skip("not a git checkout with a HEAD")
	}
	archive := exec.Command("git", "-C", root, "archive", "--worktree-attributes", "--format=tar", "HEAD")
	out, err := archive.Output()
	if err != nil {
		t.Fatalf("git archive: %v", err)
	}
	tr := tar.NewReader(bytes.NewReader(out))
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n++
		for _, dir := range []string{"tools/", "ui/e2e/perf/"} {
			if strings.HasPrefix(h.Name, dir) {
				t.Errorf("git archive HEAD contains %s (export-ignore'd in .gitattributes)", h.Name)
			}
		}
	}
	if n == 0 {
		t.Fatal("git archive HEAD listed no files")
	}
}

// TestPerfCodeIsTagged: every Go file of the perf tools, and the fake
// provider's perf-only dialects (providers/fake/perf*.go), is behind
// //go:build perf, so no untagged build (every release) compiles it.
func TestPerfCodeIsTagged(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, dir := range []string{"tools", filepath.Join("services", "data", "cmd", "perfseed")} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(p, ".go") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	fakePerf, _ := filepath.Glob(filepath.Join(root, "providers", "fake", "perf*.go"))
	files = append(files, fakePerf...)
	if len(fakePerf) == 0 {
		t.Error("no providers/fake/perf*.go: the fake's perf dialects moved; update this test")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		first, _, _ := strings.Cut(string(b), "\n")
		if strings.TrimSpace(first) != "//go:build perf" {
			rel, _ := filepath.Rel(root, f)
			t.Errorf("%s: first line %q, want //go:build perf (perf code must never ship)", rel, first)
		}
	}
}
