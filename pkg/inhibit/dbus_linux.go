package inhibit

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// acquireTimeout bounds each D-Bus call so a hung bus never stalls a caller.
const acquireTimeout = 2 * time.Second

// who identifies mads to logind's and the desktop's inhibitor listings.
const who = "mads"

// dbusLocker is the real, stateless D-Bus-backed Locker. It holds two
// independent inhibitors, acquired concurrently:
//
//   - a logind "sleep:idle" block inhibitor on the system bus, covering
//     manual/lid/server suspend paths;
//   - an org.freedesktop.ScreenSaver inhibit on the session bus, covering
//     desktop automatic idle-suspend (some desktops, e.g. COSMIC's
//     cosmic-idle, honor only this one and ignore the logind inhibitor).
//
// Either half is free to fail (no bus, no service, refused request); a
// warning is logged and that half simply holds nothing.
type dbusLocker struct{}

// NewLocker returns the real D-Bus-backed Locker.
func NewLocker() Locker { return dbusLocker{} }

func (dbusLocker) Acquire(why string) Handle {
	var wg sync.WaitGroup
	wg.Add(2)

	var logindFD *os.File
	var ssConn *dbus.Conn
	var ssCookie uint32

	go func() {
		defer wg.Done()
		logindFD = acquireLogind(why)
	}()
	go func() {
		defer wg.Done()
		ssConn, ssCookie = acquireScreenSaver(why)
	}()
	wg.Wait()

	return HandleFunc(func() {
		if logindFD != nil {
			_ = logindFD.Close()
		}
		if ssConn != nil {
			obj := ssConn.Object("org.freedesktop.ScreenSaver", dbus.ObjectPath("/org/freedesktop/ScreenSaver"))
			ctx, cancel := context.WithTimeout(context.Background(), acquireTimeout)
			_ = obj.CallWithContext(ctx, "org.freedesktop.ScreenSaver.UnInhibit", 0, ssCookie).Err
			cancel()
			_ = ssConn.Close()
		}
	})
}

// acquireLogind requests a "sleep:idle" block inhibitor over the system bus.
// The returned fd holds the lock independent of the bus connection — per
// logind's own semantics, the connection used to request it can close
// immediately without releasing the fd.
func acquireLogind(why string) *os.File {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		log.Printf("inhibit: logind unavailable: %v", err)
		return nil
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), acquireTimeout)
	defer cancel()
	obj := conn.Object("org.freedesktop.login1", dbus.ObjectPath("/org/freedesktop/login1"))
	var fd dbus.UnixFD
	call := obj.CallWithContext(ctx, "org.freedesktop.login1.Manager.Inhibit", 0,
		"sleep:idle", who, why, "block")
	if err := call.Store(&fd); err != nil {
		log.Printf("inhibit: logind unavailable: %v", err)
		return nil
	}
	return os.NewFile(uintptr(fd), "logind-inhibit")
}

// acquireScreenSaver requests an org.freedesktop.ScreenSaver inhibit cookie
// over the session bus. Unlike the logind fd, the ScreenSaver inhibit's
// lifetime is tied to the D-Bus connection, so the connection is kept open
// and returned alongside the cookie for Release to close later.
func acquireScreenSaver(why string) (*dbus.Conn, uint32) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Printf("inhibit: screensaver unavailable: %v", err)
		return nil, 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), acquireTimeout)
	defer cancel()
	obj := conn.Object("org.freedesktop.ScreenSaver", dbus.ObjectPath("/org/freedesktop/ScreenSaver"))
	var cookie uint32
	call := obj.CallWithContext(ctx, "org.freedesktop.ScreenSaver.Inhibit", 0, who, why)
	if err := call.Store(&cookie); err != nil {
		log.Printf("inhibit: screensaver unavailable: %v", err)
		_ = conn.Close()
		return nil, 0
	}
	return conn, cookie
}
