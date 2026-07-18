package contracts

import "time"

// Lease is the current claim on a task (spec §15).
type Lease struct {
	TaskID    string    `json:"task_id"`
	Holder    string    `json:"holder"`
	TTL       int64     `json:"ttl_ms"`
	ClaimedAt time.Time `json:"claimed_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Identity is set only when the claiming engine could establish its
	// own process identity (pkg/procid) at claim time. nil for
	// pre-migration rows and non-Linux writers — never provable.
	Identity *LeaseIdentity `json:"identity,omitempty"`
}

// LeaseIdentity pins a lease's holder to one exact OS process (reap-on-
// launch proof — never a heuristic): this machine, this boot, this pid,
// this pid's start time.
type LeaseIdentity struct {
	MachineID     string `json:"machine_id"`
	BootID        string `json:"boot_id"`
	PID           int    `json:"pid"`
	PIDStartTicks int64  `json:"pid_start_ticks"`
}
