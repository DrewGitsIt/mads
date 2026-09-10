UPDATE templates
SET data = json_remove(data, '$.version');
