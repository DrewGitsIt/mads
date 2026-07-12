-- Write leases (spec §15): shared task mutation without contention. A lease
-- is a parking meter — it renews on every write by its holder and expires on
-- its own if the holder stops renewing. No poller asks "are you alive?";
-- expiry is checked at write time and by the sweep.
CREATE TABLE task_leases (
    task_id     TEXT PRIMARY KEY REFERENCES tasks (id) ON DELETE CASCADE,
    holder      TEXT NOT NULL,
    ttl_ms      INTEGER NOT NULL,
    claimed_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL
);

-- Supervision path: release everything a crashed child held (spec §15).
CREATE INDEX idx_task_leases_holder ON task_leases (holder);
-- Sweep path: find expired leases.
CREATE INDEX idx_task_leases_expiry ON task_leases (expires_at);
