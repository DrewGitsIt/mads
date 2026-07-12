-- The event log: the control plane and the metrics store (spec §15, §8).
-- Append-only; nothing updates or deletes rows. seq is the global cursor —
-- AUTOINCREMENT guarantees it is monotone and never reused, so subscribers
-- can resume from any cursor and reason about gaps.
CREATE TABLE events (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp  TEXT NOT NULL,
    type       TEXT NOT NULL,
    session_id TEXT,
    task_id    TEXT,
    actor      TEXT NOT NULL,
    payload    TEXT
);

-- The two subscription access paths (spec §15): per-session streams and
-- type-filtered streams, both ordered by seq.
CREATE INDEX idx_events_session ON events (session_id, seq);
CREATE INDEX idx_events_type ON events (type, seq);
