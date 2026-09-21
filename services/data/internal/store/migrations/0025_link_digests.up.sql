-- Mailbox for on_signal links (docs/LINKED-SESSIONS-DESIGN.md decision 4):
-- per-turn digests accumulate here instead of forcing a monitor turn, and
-- flush as context on the link's next signal wake or on-demand drain.
-- Capped per link in the store (oldest dropped first); rows are cleared
-- when their link is deleted.
CREATE TABLE link_digests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  link_id TEXT NOT NULL,
  text TEXT NOT NULL,
  from_turn INTEGER NOT NULL DEFAULT 0,
  to_turn INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE INDEX idx_link_digests_link ON link_digests (link_id, id);
