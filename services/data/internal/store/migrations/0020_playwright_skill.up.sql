-- Attach the shipped playwright-ui-testing skill (assets/skills/, spec §12)
-- to the two system agents that verify UI work: the executor (which should
-- prove its own UI changes before handing off) and the verifier (whose job
-- is exactly this). The capability record itself is registered separately
-- at Data Service startup (store.SeedShippedCapabilities) rather than here,
-- since its Config.Path is a resolved filesystem path, not something a
-- migration file can hardcode — this migration only edits the two existing
-- agent rows' declared skill lists, same json1 technique as 0008.
UPDATE agents
SET data = json_insert(data, '$.capabilities.skills[#]', 'playwright-ui-testing')
WHERE id = 'agt_system_executor';

UPDATE agents
SET data = json_insert(data, '$.capabilities.skills[#]', 'playwright-ui-testing')
WHERE id = 'agt_system_verifier';
