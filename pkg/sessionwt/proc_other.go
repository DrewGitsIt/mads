//go:build !unix

package sessionwt

import "os/exec"

// ownGroup is a no-op on non-unix hosts (Windows), where there are no
// process groups to leave; the child there is stopped by its own process,
// not a group signal (see proc_unix.go).
func ownGroup(cmd *exec.Cmd) {}
