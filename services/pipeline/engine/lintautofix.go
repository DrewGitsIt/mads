package engine

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// lintFixableLinters is the whitelist of formatter-class linters the land
// queue's mechanical autofix pass is trusted to run unattended: purely
// syntactic, semantics-preserving reformatting. Never errcheck/staticcheck/
// unused/etc — "unused after merge" can mean the merge dropped a caller, and
// that's information for a human, not noise (Drew, approved after 672a6bc:
// trunk went lint-red silently because the land gate ran build+test but not
// lint).
var lintFixableLinters = map[string]bool{
	"gofmt":     true,
	"gofumpt":   true,
	"goimports": true,
}

// gateLintIssueRe matches a golangci-lint-shaped issue line: "path:line[:col]:
// message (linter)". Captures the linter name.
var gateLintIssueRe = regexp.MustCompile(`(?m)^\S+:\d+(?::\d+)?:.*\(([\w-]+)\)\s*$`)

// classifyLintFailure inspects a gate command's failure output for
// golangci-lint-shaped issue lines and reports whether it's a lint failure at
// all (ok), and if so whether every distinct linter named is in the
// mechanical-autofix whitelist (fixable). ok is false when the output isn't
// lint-shaped (a test/build failure, a UI suite, anything else) — callers
// fall back to the existing flake-triage path for that case, unchanged.
func classifyLintFailure(output string) (fixable, ok bool, linters []string) {
	matches := gateLintIssueRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return false, false, nil
	}
	seen := map[string]bool{}
	fixable = true
	for _, m := range matches {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		linters = append(linters, name)
		if !lintFixableLinters[name] {
			fixable = false
		}
	}
	return fixable, true, linters
}

// lintAutofixCommands is the whitelisted, semantics-preserving fixer set the
// mechanical autofix pass runs, in order, best-effort — a binary not found on
// PATH is silently skipped (same "detect availability, fall back cleanly"
// pattern as mergiraf), never an error. Each fixer's own exit status is
// ignored: what gates the outcome is whether the tree changed, not whether a
// fixer reports full convergence.
var lintAutofixCommands = []struct {
	binary string
	args   []string
}{
	{"gofmt", []string{"-l", "-w", "."}},
	{"goimports", []string{"-w", "."}},
	{"golangci-lint", []string{"run", "--fix", "--disable-all", "--enable=gofmt,goimports,gofumpt", "./..."}},
}

// lintAutofixResult is the mechanical autofix pass's outcome: which of the
// whitelisted fixers actually ran (were found on PATH) and whether any of
// them changed the tree.
type lintAutofixResult struct {
	fixers  []string
	changed bool
	summary string
}

// runLintAutofix runs every available whitelisted fixer against dir,
// best-effort, then reports whether the tree changed. It never inspects WHAT
// changed for correctness — only gofmt/goimports/golangci-lint's own
// formatter-class fixers ran, so any diff is by construction
// semantics-preserving reformatting, never a behavior change.
func (e *engine) runLintAutofix(ctx context.Context, dir string) lintAutofixResult {
	var res lintAutofixResult
	for _, fx := range lintAutofixCommands {
		if _, err := exec.LookPath(fx.binary); err != nil {
			continue
		}
		res.fixers = append(res.fixers, fx.binary)
		cmd := exec.CommandContext(ctx, fx.binary, fx.args...)
		cmd.Dir = dir
		_, _ = cmd.CombinedOutput()
	}
	status, _ := e.wt.git(ctx, dir, "status", "--porcelain")
	if strings.TrimSpace(status) == "" {
		return res
	}
	res.changed = true
	diffStat, _ := e.wt.git(ctx, dir, "diff", "--stat")
	res.summary = strings.TrimSpace(diffStat)
	return res
}

// commitLintAutofix commits the mechanical autofix pass's changes as their
// own clearly-labeled commit, distinct from the task's own work (Drew:
// "land: mechanical lint fixes").
func (w *worktrees) commitLintAutofix(ctx context.Context, dir string, task contracts.Task, res lintAutofixResult) error {
	if _, err := w.git(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	msg := fmt.Sprintf("land: mechanical lint fixes\n\nTask: %s\n\nFixers: %s", task.ID, strings.Join(res.fixers, ", "))
	_, err := w.git(ctx, dir, "commit", "-m", msg)
	return err
}
