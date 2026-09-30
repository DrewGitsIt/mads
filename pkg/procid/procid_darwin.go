//go:build darwin

package procid

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// Default is the real darwin backend (no cgo).
var Default Reader = &darwinReader{}

type darwinReader struct {
	machineOnce sync.Once
	machineID   string
	machineErr  error
}

// MachineID is the hardware IOPlatformUUID. It is stable across boots and
// OS reinstalls, so it is read once and cached.
func (r *darwinReader) MachineID() (string, error) {
	r.machineOnce.Do(func() {
		out, err := exec.Command("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err != nil {
			r.machineErr = fmt.Errorf("procid: ioreg: %w", err)
			return
		}
		r.machineID, r.machineErr = parseIOPlatformUUID(out)
	})
	return r.machineID, r.machineErr
}

// BootID is kern.bootsessionuuid, new every boot — read fresh every call.
func (*darwinReader) BootID() (string, error) {
	s, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return "", fmt.Errorf("procid: sysctl kern.bootsessionuuid: %w", err)
	}
	return strings.TrimSpace(s), nil
}

// PIDStartTicks is kinfo_proc's p_starttime in microseconds since the
// epoch. The slice variant is used because the single-struct
// SysctlKinfoProc reports a missing pid (sysctl success, zero length) as
// EIO, which would be indistinguishable from a real read failure.
func (*darwinReader) PIDStartTicks(pid int) (int64, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return 0, fmt.Errorf("procid: pid %d: %w", pid, os.ErrNotExist)
		}
		return 0, fmt.Errorf("procid: sysctl kern.proc.pid %d: %w", pid, err)
	}
	if len(procs) == 0 || procs[0].Proc.P_pid == 0 {
		return 0, fmt.Errorf("procid: pid %d: %w", pid, os.ErrNotExist)
	}
	st := procs[0].Proc.P_starttime
	us := timevalMicros(st.Sec, int64(st.Usec))
	if us == 0 {
		return 0, fmt.Errorf("procid: pid %d: %w", pid, os.ErrNotExist)
	}
	return us, nil
}
