// Package inhibit holds OS sleep/idle inhibitor locks so the machine doesn't
// suspend while something important is in progress. NewLocker picks the
// backend per OS: logind + org.freedesktop.ScreenSaver over D-Bus on linux
// (dbus_linux.go), a `caffeinate -i -s -w <pid>` child on darwin
// (caffeinate_darwin.go), and a no-op elsewhere (noop_other.go). Acquiring
// never fails or blocks meaningfully on any backend: a missing/refused
// service or a failed spawn degrades to a no-op with a logged warning.
package inhibit

// Locker acquires inhibitor lock(s) and returns a Handle to release them.
type Locker interface {
	// Acquire holds the lock(s), naming why (e.g. "pipeline run ses_x").
	// Never returns an error — failures degrade to a no-op Handle plus a
	// logged warning. Bounded internally (~2s) so a hung bus never stalls
	// the caller.
	Acquire(why string) Handle
}

// Handle releases whatever Acquire held. Release is safe to call once; a
// no-op Handle's Release is a no-op.
type Handle interface {
	Release()
}

// HandleFunc adapts a plain func to Handle — used by the real Locker for its
// no-op degraded case, and by fakes in tests.
type HandleFunc func()

// Release calls f.
func (f HandleFunc) Release() { f() }
