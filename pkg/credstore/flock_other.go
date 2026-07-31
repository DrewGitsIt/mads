//go:build !linux

package credstore

// acquireFlock is a no-op off Linux (the owner's target platform is
// Pop!_OS/COSMIC), mirroring services/pipeline/engine's own linux/!linux
// split for flock(2)-based cross-process serialization: a non-Linux host
// loses the cross-process migration guarantee but a single process's
// in-memory Store.mu still serializes its own concurrent Open calls.
func acquireFlock(path string) (release func() error, err error) {
	return func() error { return nil }, nil
}
