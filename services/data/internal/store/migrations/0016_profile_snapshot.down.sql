UPDATE profiles
SET data = json_set(json_remove(data, '$.snapshot'), '$.settings', json_extract(data, '$.snapshot'));
