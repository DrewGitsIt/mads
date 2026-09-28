-- Drop the mirrored runtime fields back out of the session blob. agent_state
-- is included even though the up migration never backfills it: a live runner
-- will have written some, and rolling back to a binary that doesn't know the
-- field should not leave it in the record.
UPDATE sessions
SET data = json_remove(data, '$.last_turn_at', '$.agent_state');
