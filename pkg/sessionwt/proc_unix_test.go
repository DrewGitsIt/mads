//go:build unix

package sessionwt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Land's merge in the user's checkout runs in a process group of its own,
// so the Router killing the session child's group can't stop it mid-write.
// A pre-merge-commit hook, run by that merge, reports the group it is in.
func TestLandMergesInItsOwnProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("no ps on PATH")
	}
	s := newIsolated(t)
	s.work(t, map[string]string{"feature.txt": "f\n"})
	s.commit(t, contracts.CommitRequest{Message: "Feature"})
	commitFile(t, s.primary, "elsewhere.txt", "base moved on\n")
	report := filepath.Join(t.TempDir(), "pgid")
	top := strings.TrimSpace(gitOut(t, s.primary, "rev-parse", "--show-toplevel"))
	hooks := strings.TrimSpace(gitOut(t, s.primary, "rev-parse", "--git-path", "hooks"))
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(s.primary, hooks)
	}
	writeFile(t, filepath.Join(hooks, "pre-merge-commit"),
		"#!/bin/sh\n[ \"$(git rev-parse --show-toplevel)\" = \""+top+"\" ] || exit 0\nps -o pgid= -p $$ > "+report+"\n")
	if err := os.Chmod(filepath.Join(hooks, "pre-merge-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	var cmds []*exec.Cmd
	landCmd = func(c *exec.Cmd) { cmds = append(cmds, c) }
	t.Cleanup(func() { landCmd = nil })

	res, err := s.ops.Land(context.Background(), "ses_test", Gates{})
	if err != nil || res.Outcome != contracts.LandLanded {
		t.Fatalf("land = %+v, %v", res, err)
	}
	if len(cmds) != 1 || cmds[0].SysProcAttr == nil || !cmds[0].SysProcAttr.Setpgid {
		t.Fatalf("land commands = %d, want the one merge with Setpgid", len(cmds))
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("the hook never ran in the checkout: %v", err)
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("pgid %q: %v", raw, err)
	}
	if pgid == syscall.Getpgrp() {
		t.Errorf("merge ran in the caller's process group %d", pgid)
	}
}
