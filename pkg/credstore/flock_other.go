//go:build !unix

package credstore

// acquireFlock is a no-op on non-unix hosts (Windows), where syscall.Flock
// does not exist; every unix, including darwin, takes the real flock(2)
// path in flock_unix.go, mirroring services/pipeline/engine's own
// unix/!unix split. A non-unix host loses the cross-process migration
// guarantee but a single process's in-memory Store.mu still serializes its
// own concurrent Open calls.
func acquireFlock(path string) (release func() error, err error) {
	return func() error { return nil }, nil
}
