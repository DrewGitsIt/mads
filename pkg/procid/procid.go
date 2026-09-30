// Package procid establishes and proves OS process identity for the
// engine's lease reap-on-launch feature: "this exact process is provably
// dead," never a heuristic like wall-clock age, hostname, or process name.
package procid

import (
	"errors"
	"fmt"
	"os"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Identity is a lease's process identity (spec: reap-on-launch). Aliased
// to contracts.LeaseIdentity so engine/store/dataclient never need to
// convert between two structurally-identical types.
type Identity = contracts.LeaseIdentity

// Reader reads this host's current process-identity facts. Default is the
// real Linux or darwin backend (every method errors elsewhere); tests
// inject a fake so every proof branch — reboot, dead pid, pid reuse, live
// sibling, /proc error — is testable without real doomed processes or
// root-owned files.
//
//	            Linux                               darwin
//	MachineID   /etc/machine-id, trimmed            IOPlatformUUID from ioreg, read once and cached
//	BootID      /proc/sys/kernel/random/boot_id     sysctl kern.bootsessionuuid
//	StartTicks  field 22 of /proc/<pid>/stat        kinfo_proc p_starttime, µs since the epoch
//	            (clock ticks since boot)
//
// BootID is read fresh on every call, never cached. PIDStartTicks returns
// an error satisfying errors.Is(_, os.ErrNotExist) when pid doesn't exist
// (no /proc entry on Linux; ESRCH or an empty kinfo_proc on darwin).
type Reader interface {
	MachineID() (string, error)
	BootID() (string, error)
	PIDStartTicks(pid int) (int64, error)
}

// Current gathers THIS process's own identity via r. ok=false means some
// part couldn't be established (unsupported platform, unreadable /proc,
// etc.) — the caller must then record/compare no identity at all; a
// partial identity is exactly the ambiguity "provable" forbids.
func Current(r Reader) (Identity, bool) {
	machineID, err := r.MachineID()
	if err != nil || machineID == "" {
		return Identity{}, false
	}
	bootID, err := r.BootID()
	if err != nil || bootID == "" {
		return Identity{}, false
	}
	pid := os.Getpid()
	ticks, err := r.PIDStartTicks(pid)
	if err != nil || ticks == 0 {
		return Identity{}, false
	}
	return Identity{
		MachineID:     machineID,
		BootID:        bootID,
		PID:           pid,
		PIDStartTicks: ticks,
	}, true
}

// Verdict is one proof evaluation's outcome.
type Verdict struct {
	Dead   bool
	Reason string // "boot_id changed" / "pid <n> not found" / "pid <n> reused" — folded verbatim into the reclaim event's evidence text
}

// Evaluate applies the proof rules to lease (a lease's recorded identity),
// using own (the reclaiming engine's freshly-gathered identity) and r
// (this host's current state). provable=false whenever the rules can't be
// conclusively evaluated (foreign/missing machine_id, missing identity
// fields, any /proc read error, mismatched machine_id) — the caller must
// then leave the lease alone.
func Evaluate(own, lease Identity, r Reader) (v Verdict, provable bool) {
	if own.MachineID == "" || lease.MachineID == "" || own.MachineID != lease.MachineID {
		return Verdict{}, false
	}
	if lease.BootID == "" || lease.PID == 0 || lease.PIDStartTicks == 0 {
		return Verdict{}, false
	}
	if own.BootID != lease.BootID {
		return Verdict{Dead: true, Reason: "boot_id changed"}, true
	}
	ticks, err := r.PIDStartTicks(lease.PID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Verdict{Dead: true, Reason: fmt.Sprintf("pid %d not found", lease.PID)}, true
		}
		return Verdict{}, false
	}
	if ticks != lease.PIDStartTicks {
		return Verdict{Dead: true, Reason: fmt.Sprintf("pid %d reused", lease.PID)}, true
	}
	return Verdict{Dead: false}, true
}
