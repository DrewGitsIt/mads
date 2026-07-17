-- Marks a link the system created without user action
-- (docs/LINKED-SESSIONS-DESIGN.md decision 6) — the self-referential
-- hand-raiser grant on a session's own pipeline run. Defaults every
-- existing row to 0 (not implicit).
ALTER TABLE links ADD COLUMN implicit INTEGER NOT NULL DEFAULT 0;
