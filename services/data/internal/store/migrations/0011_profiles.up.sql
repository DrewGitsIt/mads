-- Settings profiles (MADS-UX §10.5): named, switchable bundles of setting
-- overrides. Same shape as the other registries.
CREATE TABLE profiles (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    system     INTEGER NOT NULL DEFAULT 0,
    data       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
