CREATE TABLE proposals (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_proposals_session ON proposals (session_id);
