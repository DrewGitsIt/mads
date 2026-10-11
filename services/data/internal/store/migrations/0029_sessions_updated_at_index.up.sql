-- The rail's list endpoint is `SELECT data FROM sessions ORDER BY updated_at
-- DESC` — a full ordered scan, which is fine for dozens of sessions but
-- slows toward a sort over the whole table once the count grows. The index
-- makes that an ordered index walk regardless of table size.
CREATE INDEX IF NOT EXISTS idx_sessions_updated_at ON sessions (updated_at DESC);
