-- Lease identity (reap-on-launch): recorded at claim time so a future
-- engine launch can prove this exact holder died — this machine, this
-- boot, this pid, this pid's start time. Nullable: old rows and
-- non-Linux writers have no identity and stay unreclaimable by proof,
-- same as before this migration (the TTL reaper is their only path).
ALTER TABLE task_leases ADD COLUMN machine_id TEXT;
ALTER TABLE task_leases ADD COLUMN boot_id TEXT;
ALTER TABLE task_leases ADD COLUMN pid INTEGER;
ALTER TABLE task_leases ADD COLUMN pid_start_ticks INTEGER;
