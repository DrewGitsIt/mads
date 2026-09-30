//go:build !linux && !darwin

package inhibit

import (
	"log"
	"runtime"
	"sync"
)

// noopLocker is the Locker for platforms with no sleep-inhibit backend yet
// (a future Windows SetThreadExecutionState backend goes here). It holds
// nothing and warns once per process.
type noopLocker struct{}

var noopWarnOnce sync.Once

// NewLocker returns a no-op Locker for this platform.
func NewLocker() Locker { return noopLocker{} }

func (noopLocker) Acquire(string) Handle {
	noopWarnOnce.Do(func() {
		log.Printf("inhibit: no sleep inhibitor on %s; the machine may sleep during runs", runtime.GOOS)
	})
	return HandleFunc(func() {})
}
