package procid

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fakeReader lets every proof branch be tested without real dead/live
// processes or root-owned files.
type fakeReader struct {
	machineID    string
	machineIDErr error
	bootID       string
	bootIDErr    error
	live         map[int]int64 // pid -> start ticks, for pids that "exist"
	statErr      error         // if set, PIDStartTicks always returns this instead of consulting live
}

func (f fakeReader) MachineID() (string, error) {
	if f.machineIDErr != nil {
		return "", f.machineIDErr
	}
	return f.machineID, nil
}

func (f fakeReader) BootID() (string, error) {
	if f.bootIDErr != nil {
		return "", f.bootIDErr
	}
	return f.bootID, nil
}

func (f fakeReader) PIDStartTicks(pid int) (int64, error) {
	if f.statErr != nil {
		return 0, f.statErr
	}
	ticks, ok := f.live[pid]
	if !ok {
		return 0, fmt.Errorf("pid %d: %w", pid, os.ErrNotExist)
	}
	return ticks, nil
}

func TestEvaluateReboot(t *testing.T) {
	own := Identity{MachineID: "m1", BootID: "boot-new", PID: 123, PIDStartTicks: 999}
	lease := Identity{MachineID: "m1", BootID: "boot-old", PID: 123, PIDStartTicks: 999}
	r := fakeReader{machineID: "m1", bootID: "boot-new"}

	v, provable := Evaluate(own, lease, r)
	if !provable || !v.Dead {
		t.Fatalf("Evaluate = %+v, provable=%v; want dead+provable", v, provable)
	}
	if !strings.Contains(v.Reason, "boot_id") {
		t.Errorf("reason %q should mention boot_id", v.Reason)
	}
}

func TestEvaluateDeadPID(t *testing.T) {
	own := Identity{MachineID: "m1", BootID: "boot1", PID: 1, PIDStartTicks: 1}
	lease := Identity{MachineID: "m1", BootID: "boot1", PID: 42, PIDStartTicks: 999}
	r := fakeReader{machineID: "m1", bootID: "boot1", live: map[int]int64{}}

	v, provable := Evaluate(own, lease, r)
	if !provable || !v.Dead {
		t.Fatalf("Evaluate = %+v, provable=%v; want dead+provable", v, provable)
	}
	if !strings.Contains(v.Reason, "not found") {
		t.Errorf("reason %q should mention 'not found'", v.Reason)
	}
}

func TestEvaluatePIDReuse(t *testing.T) {
	own := Identity{MachineID: "m1", BootID: "boot1", PID: 1, PIDStartTicks: 1}
	lease := Identity{MachineID: "m1", BootID: "boot1", PID: 42, PIDStartTicks: 999}
	r := fakeReader{machineID: "m1", bootID: "boot1", live: map[int]int64{42: 1000}}

	v, provable := Evaluate(own, lease, r)
	if !provable || !v.Dead {
		t.Fatalf("Evaluate = %+v, provable=%v; want dead+provable", v, provable)
	}
	if !strings.Contains(v.Reason, "reused") {
		t.Errorf("reason %q should mention 'reused'", v.Reason)
	}
}

func TestEvaluateLiveSibling(t *testing.T) {
	own := Identity{MachineID: "m1", BootID: "boot1", PID: 1, PIDStartTicks: 1}
	lease := Identity{MachineID: "m1", BootID: "boot1", PID: 42, PIDStartTicks: 999}
	r := fakeReader{machineID: "m1", bootID: "boot1", live: map[int]int64{42: 999}}

	v, provable := Evaluate(own, lease, r)
	if !provable || v.Dead {
		t.Fatalf("Evaluate = %+v, provable=%v; want alive+provable (live sibling must not be reclaimed)", v, provable)
	}
}

func TestEvaluateForeignMachineID(t *testing.T) {
	own := Identity{MachineID: "m1", BootID: "boot1", PID: 1, PIDStartTicks: 1}
	lease := Identity{MachineID: "m2", BootID: "boot1", PID: 42, PIDStartTicks: 999}
	r := fakeReader{machineID: "m1", bootID: "boot1"}

	_, provable := Evaluate(own, lease, r)
	if provable {
		t.Fatal("Evaluate: foreign machine_id must not be provable")
	}
}

func TestEvaluateMissingIdentity(t *testing.T) {
	own := Identity{MachineID: "m1", BootID: "boot1", PID: 1, PIDStartTicks: 1}
	r := fakeReader{machineID: "m1", bootID: "boot1"}

	cases := []struct {
		name  string
		lease Identity
	}{
		{"empty boot_id", Identity{MachineID: "m1", BootID: "", PID: 42, PIDStartTicks: 999}},
		{"zero pid", Identity{MachineID: "m1", BootID: "boot1", PID: 0, PIDStartTicks: 999}},
		{"zero start ticks", Identity{MachineID: "m1", BootID: "boot1", PID: 42, PIDStartTicks: 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, provable := Evaluate(own, c.lease, r)
			if provable {
				t.Fatalf("Evaluate: %s must not be provable", c.name)
			}
		})
	}
}

func TestEvaluateProcReadError(t *testing.T) {
	own := Identity{MachineID: "m1", BootID: "boot1", PID: 1, PIDStartTicks: 1}
	lease := Identity{MachineID: "m1", BootID: "boot1", PID: 42, PIDStartTicks: 999}
	r := fakeReader{machineID: "m1", bootID: "boot1", statErr: errors.New("permission denied")}

	v, provable := Evaluate(own, lease, r)
	if provable {
		t.Fatal("Evaluate: a non-not-exist /proc read error must not be treated as proof")
	}
	if v.Dead {
		t.Errorf("Evaluate: /proc read error must not report Dead=true")
	}
}

func TestCurrentOK(t *testing.T) {
	r := fakeReader{machineID: "m1", bootID: "boot1", live: map[int]int64{os.Getpid(): 555}}
	id, ok := Current(r)
	if !ok {
		t.Fatal("Current: want ok=true")
	}
	if id.MachineID != "m1" || id.BootID != "boot1" || id.PID != os.Getpid() || id.PIDStartTicks != 555 {
		t.Errorf("Current = %+v", id)
	}
}

func TestCurrentErrors(t *testing.T) {
	cases := []struct {
		name string
		r    fakeReader
	}{
		{"machine id error", fakeReader{machineIDErr: errors.New("fail"), bootID: "boot1", live: map[int]int64{os.Getpid(): 1}}},
		{"boot id error", fakeReader{machineID: "m1", bootIDErr: errors.New("fail"), live: map[int]int64{os.Getpid(): 1}}},
		{"pid stat error", fakeReader{machineID: "m1", bootID: "boot1", live: map[int]int64{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ok := Current(c.r)
			if ok {
				t.Fatalf("Current: want ok=false")
			}
		})
	}
}
