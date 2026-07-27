UPDATE agents
SET data = json_set(data, '$.capabilities.skills', json('["minimal-edit-discipline","structured-output-formats"]'))
WHERE id = 'agt_system_executor';

UPDATE agents
SET data = json_set(data, '$.capabilities.skills', json('["test-execution","integration-checking"]'))
WHERE id = 'agt_system_verifier';
