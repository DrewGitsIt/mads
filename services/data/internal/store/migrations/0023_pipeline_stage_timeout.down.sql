UPDATE config
SET data = json_remove(data, '$.pipeline_stage_timeout_minutes')
WHERE id = 1;
