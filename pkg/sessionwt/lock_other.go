//go:build !unix

package sessionwt

import "context"

// AcquireFlock is a no-op on non-unix hosts (Windows), where syscall.Flock
// does not exist; every unix, darwin included, takes the real flock(2) path
// in lock_unix.go. Processes on such a host lose the cross-process guarantee;
// the pipeline engine's in-process land queue still applies.
func AcquireFlock(ctx context.Context, path string) (release func() error, err error) {
	return func() error { return nil }, nil
}
