UPDATE agents
SET data = REPLACE(data, '"capabilities":{}', '"capabilities":{"skills":["conversation-analysis","task-schema-compliance"]}')
WHERE id = 'agt_system_synthesizer';

UPDATE agents
SET data = REPLACE(data, '"capabilities":{}', '"capabilities":{"skills":["persona-design","capability-matching","prompt-engineering"]}')
WHERE id = 'agt_system_agent_builder';

UPDATE agents
SET data = REPLACE(data, '"capabilities":{}', '"capabilities":{"skills":["codebase-navigation","file-localization","design-note-structure"]}')
WHERE id = 'agt_system_architect';

UPDATE agents
SET data = REPLACE(data, '"capabilities":{}', '"capabilities":{"skills":["minimal-edit-discipline","structured-output-formats"]}')
WHERE id = 'agt_system_executor';

UPDATE agents
SET data = REPLACE(data, '"capabilities":{}', '"capabilities":{"skills":["acceptance-criteria-evaluation","diff-analysis"]}')
WHERE id = 'agt_system_code_reviewer';

UPDATE agents
SET data = REPLACE(data, '"capabilities":{}', '"capabilities":{"skills":["test-execution","integration-checking"]}')
WHERE id = 'agt_system_verifier';
