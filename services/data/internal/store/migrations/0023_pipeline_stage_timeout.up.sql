-- Configurable per-stage pipeline timeout (fixes UI/CLI divergence): the
-- Router-spawned pipeline child (services/pipeline/main.go) never set a
-- StageTimeout, silently falling through to the engine's own 10m internal
-- default, while cmd/mads/run.go's CLI path defaulted to 30m — identical
-- templates timed out twice as fast from the UI as from the CLI, with no
-- setting anywhere to change either. Backfill the existing config row
-- explicitly with the new field's default (30m, the safer of the two
-- pre-existing values) rather than relying solely on the Go-side
-- DefaultConfig() merge, so the stored JSON is self-describing from here on.
UPDATE config
SET data = json_set(data, '$.pipeline_stage_timeout_minutes', 30)
WHERE id = 1;
