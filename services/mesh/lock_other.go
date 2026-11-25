//go:build !unix

package main

// lockInstance is a no-op on non-unix hosts (Windows), where syscall.Flock
// does not exist: two Mesh Services for one mads home aren't refused there.
func lockInstance(madsHome string) error { return nil }
