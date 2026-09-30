//go:build !unix

package engine

// acquireWorkspaceFlock is a no-op on non-unix hosts (Windows), where
// syscall.Flock does not exist; every unix, including darwin, takes the
// real flock(2) path in workspacelock_unix.go. A CLI engine and a
// Router-spawned pipeline child both targeting the same workspace from a
// non-unix host lose the cross-process guarantee; the in-process landQueue
// still applies.
func acquireWorkspaceFlock(path string) (release func() error, err error) {
	return func() error { return nil }, nil
}
