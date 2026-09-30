// Package madsbin locates the mads CLI binary for processes that spawn it
// or tell an agent to run it: the orchestrate MCP server a session's
// capability starts ("mads internal orchestrate-mcp"), and the shell
// commands a leader is given while its tools are pending. A bare "mads"
// only works if it's on PATH, which a Router-spawned session child and the
// provider it launches don't guarantee. A leader link stayed pending
// forever on exactly that ("exec: mads: executable file not found in
// $PATH").
package madsbin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Path returns the mads binary to run, first match wins:
//  1. $MADS_BIN, if set;
//  2. "mads" next to the running executable (bin/session, bin/router and
//     bin/mads are built side by side);
//  3. "mads" on PATH, made absolute;
//  4. plain "mads", leaving resolution to whoever execs it.
//
// Command strings are split on whitespace everywhere they're consumed
// (strings.Fields), so a candidate whose path contains whitespace is
// skipped in favor of the next one.
func Path() string {
	if p := os.Getenv("MADS_BIN"); usable(p) {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), binName())
		if usable(sibling) && isFile(sibling) {
			return sibling
		}
	}
	if p, err := exec.LookPath(binName()); err == nil {
		if abs, err := filepath.Abs(p); err == nil && usable(abs) {
			return abs
		}
	}
	return "mads"
}

func usable(p string) bool {
	return p != "" && !strings.ContainsAny(p, " \t\n")
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
