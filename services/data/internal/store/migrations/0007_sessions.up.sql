-- Sessions (spec §1): the workspace-of-record for one conversation-with-work.
-- Entity JSON in data (source of truth), activity extracted for the rail's
-- Active/Recent split.
CREATE TABLE sessions (
    id         TEXT PRIMARY KEY,
    activity   TEXT NOT NULL,
    data       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- File tracking (spec §5): every noticed change, attributed or not.
CREATE TABLE file_changes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    task_id    TEXT,
    repo       TEXT NOT NULL DEFAULT '',
    path       TEXT NOT NULL,
    change     TEXT NOT NULL,
    origin     TEXT NOT NULL,
    attributed INTEGER NOT NULL,
    actor      TEXT NOT NULL DEFAULT '',
    timestamp  TEXT NOT NULL
);

CREATE INDEX idx_file_changes_session ON file_changes (session_id, id);
CREATE INDEX idx_file_changes_task ON file_changes (task_id);

-- Commit linkage (spec §3, §5): every commit links to a task through the
-- resolution chain or is flagged orphan — never silently dropped.
CREATE TABLE commits (
    hash       TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    task_id    TEXT,
    repo       TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL,
    author     TEXT NOT NULL DEFAULT '',
    linked_via TEXT NOT NULL DEFAULT '',
    orphan     INTEGER NOT NULL DEFAULT 0,
    timestamp  TEXT NOT NULL
);

CREATE INDEX idx_commits_session ON commits (session_id);
CREATE INDEX idx_commits_task ON commits (task_id);
