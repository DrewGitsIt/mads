-- Held link updates (docs/HELD-UPDATES-DESIGN.md decision 9): discarding a
-- held update is a soft delete. A discarded row keeps its place in the
-- mailbox (so Undo can restore it) but is left out of counts, drains and
-- flushes; '' means not discarded, else an RFC 3339 timestamp. A deferred
-- drain removes the link's discarded rows, and discarding purges rows
-- discarded more than 30 days ago.
ALTER TABLE link_digests ADD COLUMN discarded_at TEXT NOT NULL DEFAULT '';
