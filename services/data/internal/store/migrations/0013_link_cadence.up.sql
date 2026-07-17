-- Monitor cadence knob (docs/LINKED-SESSIONS-DESIGN.md decision 4): whether
-- a `read` grant's per-turn digest push is active for this link. Defaults
-- every existing row to per_turn, the spec's default.
ALTER TABLE links ADD COLUMN cadence TEXT NOT NULL DEFAULT 'per_turn';
