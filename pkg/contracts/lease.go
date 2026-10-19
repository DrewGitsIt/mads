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
	// pre-migration rows and writers on platforms other than Linux and
	// darwin — never provable.
	Identity *LeaseIdentity `json:"identity,omitempty"`
	// Token is the lease's proof of possession, returned once by a
	// successful claim and never again (the store keeps only its hash).
	// Renew, release and writes to the leased task present it in the
	// X-Mads-Lease header. Never log it or write it anywhere.
	Token string `json:"token,omitempty"`
}

// LeaseIdentity pins a lease's holder to one exact OS process (reap-on-
// launch proof — never a heuristic): this machine, this boot, this pid,
// this pid's start time.
type LeaseIdentity struct {
	MachineID string `json:"machine_id"`
	BootID    string `json:"boot_id"`
	PID       int    `json:"pid"`
	// PIDStartTicks is platform-specific: clock ticks since boot on Linux,
	// microseconds since the epoch on darwin. The units never mix because
	// procid.Evaluate only compares identities whose MachineID matches.
	PIDStartTicks int64 `json:"pid_start_ticks"`
}
