//go:build unix

package sessionwt

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// lockPoll is how often a waiting AcquireFlock retries. A blocking flock
// can't be interrupted, and the holder may be a pipeline land running its
// whole gate (minutes), so waiting is a non-blocking retry loop that a
// cancelled context can leave.
const lockPoll = 50 * time.Millisecond

// AcquireFlock takes an exclusive flock(2) on path, creating the file if
// needed, waiting until it is free or ctx ends. The returned func closes the
// file, which releases the lock — safe from any goroutine, since the lock
// lives on the open file description, not on the fd number or the thread.
func AcquireFlock(ctx context.Context, path string) (release func() error, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f.Close, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
