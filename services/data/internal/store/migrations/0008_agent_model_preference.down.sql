UPDATE agents
SET data = json_remove(data, '$.preferences.model')
WHERE system = 1;
