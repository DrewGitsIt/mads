-- Pin the system agents to Sonnet (spec §9: model is a preference). The
-- pipeline's results come from task content and structured handoffs, not
-- reasoning depth, and without a preference every one-shot inherits the
-- provider CLI's default model — Wave 4 silently ran one task on a
-- different model when that default shifted mid-run.
UPDATE agents
SET data = json_set(data, '$.preferences.model', 'sonnet')
WHERE system = 1;
