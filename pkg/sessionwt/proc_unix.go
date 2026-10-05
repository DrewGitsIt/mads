//go:build unix

package sessionwt

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own. The Router stops a
// session child by signalling the child's whole group (SIGTERM, then
// SIGKILL after a grace), and a git merge in the user's checkout killed
// half-way leaves it mid-merge; in its own group, git finishes.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
