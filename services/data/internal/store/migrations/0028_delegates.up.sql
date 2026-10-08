-- Delegates (docs/DELEGATES-DESIGN.md): harness-run subagents, one row per
-- (session, harness id). Owned by their session — Sessions.Delete removes
-- them in the same transaction (no FK, same as tasks: the cascade is
-- explicit). The record is a projection the session runner writes; the
-- harness's own lifecycle lines stay in the event log as
-- session.subagent_task.
CREATE TABLE delegates (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL,
    harness_id      TEXT NOT NULL,
    state           TEXT NOT NULL,
    started_at      TEXT NOT NULL,
    data            TEXT NOT NULL, -- JSON contracts.Delegate (the full record)
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE (session_id, harness_id)
);

CREATE INDEX idx_delegates_state ON delegates (state);
