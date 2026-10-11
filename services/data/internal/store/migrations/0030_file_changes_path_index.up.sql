-- Shell-detected changes (POST /sessions/{id}/files/shell) skip any path
-- another session's tools recorded in the same window, looked up by path on
-- every scan that found something. The ledger only had a per-session index,
-- so that lookup was a full scan of every session's changes.
CREATE INDEX IF NOT EXISTS idx_file_changes_path ON file_changes (path);
