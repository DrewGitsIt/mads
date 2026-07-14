package inhibit

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// No D-Bus reachable at all (containers, CI, headless): Acquire must not
// fail, block, or panic on Release — it degrades to a no-op with a logged
// warning.
func TestAcquireDegradesWithoutBus(t *testing.T) {
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path=/nonexistent/mads-inhibit-test-system")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/mads-inhibit-test-session")

	h := NewLocker().Acquire("no bus available")
	if h == nil {
		t.Fatal("Acquire returned a nil Handle")
	}
	h.Release() // must not panic even though nothing was held
}

// fakeScreenSaver is a minimal org.freedesktop.ScreenSaver implementation
// exported on a private test bus, standing in for a real desktop's
// idle-suspend owner (cosmic-idle, gnome-session, etc).
type fakeScreenSaver struct {
	mu        sync.Mutex
	next      uint32
	inhibited map[uint32]string
}

func newFakeScreenSaver() *fakeScreenSaver {
	return &fakeScreenSaver{inhibited: map[uint32]string{}}
}

func (s *fakeScreenSaver) Inhibit(appName, reason string) (uint32, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	s.inhibited[s.next] = appName + ": " + reason
	return s.next, nil
}

func (s *fakeScreenSaver) UnInhibit(cookie uint32) *dbus.Error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inhibited, cookie)
	return nil
}

func (s *fakeScreenSaver) held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inhibited)
}

// startPrivateBus spins up a throwaway dbus-daemon for the test and returns
// its address; the process is killed on cleanup. Callers skip when
// dbus-daemon isn't on PATH.
func startPrivateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("no dbus-daemon on PATH")
	}
	const cfg = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:tmpdir=/tmp</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
    <allow user="*"/>
  </policy>
</busconfig>`
	confPath := t.TempDir() + "/mads-inhibit-test.conf"
	if err := os.WriteFile(confPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("dbus-daemon", "--config-file="+confPath, "--print-address")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	sc := bufio.NewScanner(stdout)
	if !sc.Scan() {
		t.Fatalf("dbus-daemon printed no address: %v", sc.Err())
	}
	return strings.TrimSpace(sc.Text())
}

// The real ScreenSaver code path end to end against a fake bus standing in
// for a desktop's idle-suspend owner, without needing a real desktop
// session. The same private bus stands in for the system bus too, so the
// logind half deterministically hits its "service unavailable" degrade path
// (no login1 object there) instead of ever touching the real system bus.
func TestAcquireHoldsScreenSaverOnFakeBus(t *testing.T) {
	addr := startPrivateBus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", addr)

	srvConn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srvConn.Close() }()
	ss := newFakeScreenSaver()
	if err := srvConn.Export(ss, "/org/freedesktop/ScreenSaver", "org.freedesktop.ScreenSaver"); err != nil {
		t.Fatal(err)
	}
	if _, err := srvConn.RequestName("org.freedesktop.ScreenSaver", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}

	h := NewLocker().Acquire("pipeline run ses_test")
	if n := ss.held(); n != 1 {
		t.Fatalf("ScreenSaver cookies held after Acquire = %d, want 1", n)
	}

	h.Release()
	deadline := time.Now().Add(time.Second)
	for ss.held() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := ss.held(); n != 0 {
		t.Fatalf("ScreenSaver cookies held after Release = %d, want 0", n)
	}
}
