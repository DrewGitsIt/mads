//go:build !linux

package engine

// acquireWorkspaceFlock is a no-op off Linux, mirroring pkg/procid's own
// linux/!linux split: flock(2)-based cross-process serialization is a
// Linux guarantee. A CLI engine and a Router-spawned pipeline child both
// targeting the same workspace from a non-Linux host lose the
// cross-process guarantee; the in-process landQueue still applies.
func acquireWorkspaceFlock(path string) (release func() error, err error) {
	return func() error { return nil }, nil
}
