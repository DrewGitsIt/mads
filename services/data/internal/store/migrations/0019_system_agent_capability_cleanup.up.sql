-- The six system agents (0005) declared skill names with no matching
-- capability registry record (the registry ships empty) — every session
-- start for a system agent logged "runner: load capability X: not found"
-- (services/session/internal/runner/capability.go). agentgen and the write
-- side now only ever persist registry-grounded capability refs; strip the
-- phantom refs already on disk the same way. System agents remain fully
-- usable with an empty capability set until real capability records exist
-- for these skills.
UPDATE agents
SET data = REPLACE(data, '"capabilities":{"skills":["conversation-analysis","task-schema-compliance"]}', '"capabilities":{}')
WHERE id = 'agt_system_synthesizer';

UPDATE agents
SET data = REPLACE(data, '"capabilities":{"skills":["persona-design","capability-matching","prompt-engineering"]}', '"capabilities":{}')
WHERE id = 'agt_system_agent_builder';

UPDATE agents
SET data = REPLACE(data, '"capabilities":{"skills":["codebase-navigation","file-localization","design-note-structure"]}', '"capabilities":{}')
WHERE id = 'agt_system_architect';

UPDATE agents
SET data = REPLACE(data, '"capabilities":{"skills":["minimal-edit-discipline","structured-output-formats"]}', '"capabilities":{}')
WHERE id = 'agt_system_executor';

UPDATE agents
SET data = REPLACE(data, '"capabilities":{"skills":["acceptance-criteria-evaluation","diff-analysis"]}', '"capabilities":{}')
WHERE id = 'agt_system_code_reviewer';

UPDATE agents
SET data = REPLACE(data, '"capabilities":{"skills":["test-execution","integration-checking"]}', '"capabilities":{}')
WHERE id = 'agt_system_verifier';
