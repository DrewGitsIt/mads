-- Lease tokens (docs/PIPELINE-LEASES-DESIGN.md A2): a claim returns a
-- random token and the store keeps only its SHA-256 (hex), so reading this
-- file yields nothing that can renew, release or write under a lease.
-- There is no legacy tokenless path (A5): every existing row was claimed by
-- a pre-token engine, so they all go. Their tasks keep their status.
DELETE FROM task_leases;
ALTER TABLE task_leases ADD COLUMN token_hash TEXT;
