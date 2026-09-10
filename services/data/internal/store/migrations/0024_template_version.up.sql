-- Template version stamping (spec §6, W1.5): an audit trail for which
-- template revision a historical pipeline run actually executed. This is
-- not a correctness fix — in-flight runs already hold their own frozen,
-- parsed copy of the template (services/pipeline/engine reads it once at
-- launch), so a save was never able to disturb them — it's purely so a
-- pipeline.started event can name the exact revision it launched from.
-- The templates table stores the whole record as JSON in data, so no new
-- column is needed: contracts.Template.Version just needs to exist in that
-- blob. Pre-existing rows predate the field entirely, so they'd otherwise
-- read back as the zero value (0) instead of a genuine first version;
-- backfill them to 1 so every row's version starts truthfully at "first
-- save" rather than "no save happened yet".
UPDATE templates
SET data = json_set(data, '$.version', 1)
WHERE json_extract(data, '$.version') IS NULL;
