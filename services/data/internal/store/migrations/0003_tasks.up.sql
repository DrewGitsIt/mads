-- Tasks (spec §3): orchestrator fields drive scheduling, core fields carry
-- the work. Every field is mutable after creation; drafts are synthesized
-- tasks awaiting confirmation (spec §4).
CREATE TABLE tasks (
    id                  TEXT PRIMARY KEY,
    session_id          TEXT NOT NULL,
    status              TEXT NOT NULL,
    retry_count         INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    acceptance_criteria TEXT NOT NULL DEFAULT '',
    scope               TEXT NOT NULL DEFAULT '',
    draft               INTEGER NOT NULL DEFAULT 0,
    source              TEXT NOT NULL,
    labels              TEXT NOT NULL DEFAULT '[]' -- JSON array of strings
);

CREATE INDEX idx_tasks_session ON tasks (session_id, status);

-- Dependency edges: task_id waits on depends_on. Cycles may exist at rest;
-- they block pipeline launch (spec §3.6), checked by the cycles query.
CREATE TABLE task_deps (
    task_id    TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    depends_on TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, depends_on)
);

CREATE INDEX idx_task_deps_reverse ON task_deps (depends_on);

-- Handoffs: append-only context passed between pipeline stages (spec §3, §6).
CREATE TABLE task_handoffs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    from_agent TEXT NOT NULL,
    content    TEXT NOT NULL,
    timestamp  TEXT NOT NULL
);

CREATE INDEX idx_task_handoffs_task ON task_handoffs (task_id, id);

-- Typed notes (spec §4); synthesized notes start as drafts.
CREATE TABLE task_notes (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id   TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    type      TEXT NOT NULL,
    content   TEXT NOT NULL,
    source    TEXT NOT NULL,
    status    TEXT NOT NULL,
    timestamp TEXT NOT NULL
);

CREATE INDEX idx_task_notes_task ON task_notes (task_id, id);
