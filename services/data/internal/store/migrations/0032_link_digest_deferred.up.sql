-- Delivery to sleeping sessions (docs/LINKED-SESSIONS-DESIGN.md, 2026-10-20):
-- the mailbox also holds pushes deferred because their recipient's child
-- wasn't running (per-turn digests, done signals, the grant notice). They
-- are told apart from on_signal backlog by `deferred`, which must keep
-- waiting for check_mailbox, and replay on the recipient's next start
-- exactly as pushed: `attribution` is the full Attribution JSON, `noop`
-- says the push may close with the NOOP offer.
ALTER TABLE link_digests ADD COLUMN deferred INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_digests ADD COLUMN noop INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_digests ADD COLUMN attribution TEXT NOT NULL DEFAULT '';
