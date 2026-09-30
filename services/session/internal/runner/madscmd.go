package runner

import (
	"os"
	"os/exec"
	"path/filepath"
)

// ResolveMadsCmd picks the mads CLI binary for the orchestrate capability,
// in order:
//
//  1. explicit (the -mads-cmd flag), when set;
//  2. a "mads" executable beside this session binary, which is the layout
//     `make build` produces (bin/mads next to bin/session). The Router
//     finds -session-cmd the same way (services/router's defaultChildCmd);
//  3. "mads" on $PATH, resolved to its full path when found;
//  4. the bare "mads", which fails at launch with exec's own "not found in
//     $PATH" error — the one the link then reports as failed.
//
// The lookup used to be only step 4, so on a machine where mads isn't
// installed on PATH every leader link failed to get its tools.
func ResolveMadsCmd(explicit string) string {
	exe, _ := os.Executable()
	return resolveMadsCmd(explicit, exe, isExecutable, exec.LookPath)
}

func resolveMadsCmd(explicit, selfExe string, isExec func(string) bool, lookPath func(string) (string, error)) string {
	if explicit != "" {
		return explicit
	}
	if selfExe != "" {
		if sibling := filepath.Join(filepath.Dir(selfExe), "mads"); isExec(sibling) {
			return sibling
		}
	}
	if found, err := lookPath("mads"); err == nil {
		return found
	}
	return "mads"
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
