-- Device activity (docs/MESH-AUTH-DESIGN.md D8, batch 3): events whose actor
-- is a paired device ("device:<name>/<short fingerprint>"), newest first,
-- and each device's newest. A partial index, so it holds only those rows;
-- queries must use the same expression to use it.
CREATE INDEX idx_events_device_actor ON events (seq) WHERE substr(actor, 1, 7) = 'device:';
