-- Session last_turn_at (docs/session-ux-v2-tasks.yaml, phase 1): the rail's
-- "last reply" stamp and recency sort key. Sessions store the whole record as
-- JSON in data, so no new column is needed — the field just has to exist in
-- that blob. Pre-existing rows predate it entirely and would read back as
-- "never had a turn", blanking the stamp for every session that already has
-- history.
--
-- Backfill only rows carrying a last_usage, which is the one piece of
-- evidence in the record that a turn actually completed: a session created
-- but never sent to has no turn to date, and inventing one would be worse
-- than leaving it empty. updated_at is the closest available approximation
-- of when that turn ended — it is exactly the conflation last_turn_at exists
-- to end (renames and summary writes bump it too), so it is an estimate for
-- historical rows only; every turn from here on stamps the real time.
UPDATE sessions
SET data = json_set(data, '$.last_turn_at', updated_at)
WHERE json_extract(data, '$.last_usage') IS NOT NULL
  AND json_extract(data, '$.last_turn_at') IS NULL;
