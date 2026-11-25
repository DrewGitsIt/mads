-- Each device's newest event (last used, docs/MESH-AUTH-DESIGN.md D8) read
-- off an index in actor order, without a sort. A partial index like 0036's;
-- IF NOT EXISTS, so a database that ran a build with it in 0036 gets it too.
CREATE INDEX IF NOT EXISTS idx_events_device_actor_last ON events (actor, seq) WHERE substr(actor, 1, 7) = 'device:';
