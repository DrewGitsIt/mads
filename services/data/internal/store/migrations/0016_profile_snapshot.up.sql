-- Profiles as full-config snapshots (profiles-v2): the old ProfileSettings
-- overlay becomes a per-field snapshot. Field names are unchanged, so this
-- is a pure JSON-key rename ($.settings -> $.snapshot).
UPDATE profiles
SET data = json_set(json_remove(data, '$.settings'), '$.snapshot', json_extract(data, '$.settings'));
