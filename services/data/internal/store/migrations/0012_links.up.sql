-- Linked sessions (docs/LINKED-SESSIONS-DESIGN.md): a directed grant set
-- from one session to another. Deliberately NO foreign keys on the session
-- id columns: session deletion must never cascade to links (REBUILD-SPEC
-- §1.12 / design doc "Unchanged from §1.12") — a link outliving its
-- session is a dangling reference by design, not a constraint violation.
-- (foreign_keys pragma is ON for this DB — see store.Open — so an FK here
-- would either block the session delete or force an explicit cascade,
-- both wrong.)
CREATE TABLE links (
    id                  TEXT PRIMARY KEY,
    granting_session_id TEXT NOT NULL,
    subject_session_id  TEXT NOT NULL,
    grants              TEXT NOT NULL, -- JSON array, e.g. ["read","inject"]
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE INDEX idx_links_granting ON links (granting_session_id);
CREATE INDEX idx_links_subject ON links (subject_session_id);
