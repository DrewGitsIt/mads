package inhibit

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// inhibitorEntry is one row of logind's ListInhibitors reply
// (a(ssssuu): what, who, why, mode, uid, pid).
type inhibitorEntry struct {
	what, who, why, mode string
}

func listInhibitors(obj dbus.BusObject) ([]inhibitorEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), acquireTimeout)
	defer cancel()
	var raw [][]any
	if err := obj.CallWithContext(ctx, "org.freedesktop.login1.Manager.ListInhibitors", 0).Store(&raw); err != nil {
		return nil, err
	}
	entries := make([]inhibitorEntry, 0, len(raw))
	for _, row := range raw {
		if len(row) < 4 {
			continue
		}
		what, _ := row[0].(string)
		who, _ := row[1].(string)
		why, _ := row[2].(string)
		mode, _ := row[3].(string)
		entries = append(entries, inhibitorEntry{what, who, why, mode})
	}
	return entries, nil
}

func hasInhibitor(entries []inhibitorEntry, why string) bool {
	for _, e := range entries {
		if e.who == who && e.why == why {
			return true
		}
	}
	return false
}

// TestLogindIntegration proves a real logind sleep:idle block inhibitor is
// actually held while Acquire's Handle is live and gone once Release runs —
// the acceptance criterion this task is named for. Self-skips wherever
// logind isn't reachable (containers, CI, headless).
func TestLogindIntegration(t *testing.T) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		t.Skip("no system bus: " + err.Error())
	}
	defer func() { _ = conn.Close() }()
	obj := conn.Object("org.freedesktop.login1", dbus.ObjectPath("/org/freedesktop/login1"))
	if _, err := listInhibitors(obj); err != nil {
		t.Skip("no logind: " + err.Error())
	}

	why := "inhibit integration test " + strconv.FormatInt(time.Now().UnixNano(), 10)
	h := NewLocker().Acquire(why)

	entries, err := listInhibitors(obj)
	if err != nil {
		t.Fatal(err)
	}
	if !hasInhibitor(entries, why) {
		t.Fatal("logind inhibitor not listed while held")
	}

	h.Release()

	// fd close is synchronous but logind's own bookkeeping may lag a beat.
	deadline := time.Now().Add(2 * time.Second)
	held := true
	for time.Now().Before(deadline) {
		entries, err = listInhibitors(obj)
		if err != nil {
			t.Fatal(err)
		}
		held = hasInhibitor(entries, why)
		if !held {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if held {
		t.Fatal("logind inhibitor still listed after Release")
	}
}

// TestScreenSaverIntegration proves the ScreenSaver half's real code path
// against whatever owns org.freedesktop.ScreenSaver on this session bus.
// Self-skips wherever there's no such owner (headless/CI/a container without
// a desktop session).
func TestScreenSaverIntegration(t *testing.T) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Skip("no session bus: " + err.Error())
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), acquireTimeout)
	var hasOwner bool
	err = conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0,
		"org.freedesktop.ScreenSaver").Store(&hasOwner)
	cancel()
	if err != nil || !hasOwner {
		t.Skip("no org.freedesktop.ScreenSaver owner on the session bus")
	}

	ssConn, cookie := acquireScreenSaver("inhibit integration test")
	if ssConn == nil {
		t.Fatal("acquireScreenSaver failed despite a live ScreenSaver owner")
	}
	defer func() { _ = ssConn.Close() }()
	if cookie == 0 {
		t.Fatal("acquireScreenSaver returned a zero cookie")
	}

	obj := ssConn.Object("org.freedesktop.ScreenSaver", dbus.ObjectPath("/org/freedesktop/ScreenSaver"))
	uctx, ucancel := context.WithTimeout(context.Background(), acquireTimeout)
	defer ucancel()
	if err := obj.CallWithContext(uctx, "org.freedesktop.ScreenSaver.UnInhibit", 0, cookie).Err; err != nil {
		t.Fatalf("UnInhibit: %v", err)
	}
}
