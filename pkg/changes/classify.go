package changes

import (
	"bytes"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Scratch rules, named as ChangedFile.ScratchRule reports them.
const (
	RuleOutside    = "outside session folder"
	RuleGitignored = "gitignored"
	RuleScratchDir = "scratch dir"
	RuleName       = "name pattern"
)

// FileFacts is everything Classify looks at for one file — gathered by
// Build (paths, git attributes, a header peek) so the rules stay pure.
type FileFacts struct {
	// Rel is the slash-separated path relative to the file's repo root
	// (else the session folder, else just the base name). "At the repo
	// root" means Rel has no slash.
	Rel string
	// Created: the file did not exist when the session started.
	Created bool
	// Outside: the file is outside the session folder.
	Outside bool
	// Ignored: the user repo's ignore rules match it.
	Ignored bool
	// LinguistGenerated: `git check-attr linguist-generated` is set.
	LinguistGenerated bool
	// GeneratedHeader: a Go file opening with a "// Code generated … DO
	// NOT EDIT." line.
	GeneratedHeader bool
}

var (
	scratchPrefixRe = regexp.MustCompile(`^(tmp|temp|scratch|debug|repro)[_.-]`)
	scratchSuffixRe = regexp.MustCompile(`[_.-](debug|scratch|tmp)\.[^.]+$`)
	rootTestRe      = regexp.MustCompile(`^test_.*\.(py|sh|js|ts)$`)
	pyTestRe        = regexp.MustCompile(`^test_.*\.py$|_test\.py$`)
	goGeneratedRe   = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)
)

var lockfiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"go.sum": true, "Cargo.lock": true, "poetry.lock": true,
	"Gemfile.lock": true, "composer.lock": true, "uv.lock": true,
}

// Classify assigns a tier (and, for scratch, the rule that fired). First
// match wins in tier-rule order: scratch, generated, tests, config, source.
func Classify(f FileFacts) (tier, scratchRule string) {
	if rule := scratchRuleFor(f); rule != "" {
		return contracts.TierScratch, rule
	}
	switch {
	case isGenerated(f):
		return contracts.TierGenerated, ""
	case isTest(f.Rel):
		return contracts.TierTests, ""
	case isConfig(f.Rel):
		return contracts.TierConfig, ""
	}
	return contracts.TierSource, ""
}

// dirSegments are the directory names of a slash path (not the base name).
func dirSegments(rel string) []string {
	dir := path.Dir(rel)
	if dir == "." || dir == "/" {
		return nil
	}
	return strings.Split(strings.Trim(dir, "/"), "/")
}

func hasDirSegment(rel string, names ...string) bool {
	for _, seg := range dirSegments(rel) {
		for _, n := range names {
			if seg == n {
				return true
			}
		}
	}
	return false
}

func atRoot(rel string) bool { return !strings.Contains(rel, "/") }

func scratchRuleFor(f FileFacts) string {
	if !f.Created {
		return ""
	}
	base := path.Base(f.Rel)
	ext := path.Ext(base)
	switch {
	case f.Outside:
		return RuleOutside
	case f.Ignored:
		return RuleGitignored
	case hasDirSegment(f.Rel, "scratch", "tmp", "temp"):
		return RuleScratchDir
	case scratchPrefixRe.MatchString(base),
		scratchSuffixRe.MatchString(base),
		atRoot(f.Rel) && rootTestRe.MatchString(base),
		atRoot(f.Rel) && (ext == ".log" || ext == ".out" || ext == ".prof"):
		return RuleName
	}
	return ""
}

func isGenerated(f FileFacts) bool {
	if f.LinguistGenerated || f.GeneratedHeader {
		return true
	}
	base := path.Base(f.Rel)
	switch {
	case lockfiles[base],
		strings.HasSuffix(base, ".snap"),
		strings.Contains(base, ".gen."),
		strings.HasSuffix(base, "_gen.go"),
		strings.HasSuffix(base, ".pb.go"),
		strings.Contains(base, "_generated."),
		hasDirSegment(f.Rel, "__snapshots__", "mocks", "generated", "dist", "build"):
		return true
	}
	return false
}

func isTest(rel string) bool {
	base := path.Base(rel)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.Contains(base, ".test."),
		strings.Contains(base, ".spec."),
		pyTestRe.MatchString(base),
		hasDirSegment(rel, "test", "tests", "__tests__", "testdata", "fixtures", "e2e"):
		return true
	}
	return false
}

func isConfig(rel string) bool {
	base := path.Base(rel)
	ext := path.Ext(base)
	segs := dirSegments(rel)
	switch {
	case base == "go.mod", base == "package.json", base == "Makefile",
		base == ".gitlab-ci.yml",
		strings.HasPrefix(base, "tsconfig") && ext == ".json",
		strings.HasPrefix(base, "Dockerfile"),
		strings.HasPrefix(base, "docker-compose"),
		strings.HasPrefix(base, ".env"),
		ext == ".toml", ext == ".ini", ext == ".cfg",
		len(segs) > 0 && segs[0] == ".github":
		return true
	case ext == ".yaml" || ext == ".yml":
		return atRoot(rel) || hasDirSegment(rel, ".github", "deploy", "config", "k8s")
	}
	return false
}

// HasGeneratedHeader reports whether the Go file at p opens with the
// standard "// Code generated … DO NOT EDIT." line. Reads at most 512
// bytes; files over 1 MB, non-Go files and unreadable files answer false.
func HasGeneratedHeader(p string) bool {
	if !strings.HasSuffix(p, ".go") {
		return false
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return false
	}
	f, err := os.Open(p) //nolint:gosec // path is from the session's change set
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	line := buf[:n]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return goGeneratedRe.Match(bytes.TrimRight(line, "\r"))
}
