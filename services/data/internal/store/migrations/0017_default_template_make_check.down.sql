UPDATE templates
SET data = REPLACE(data, 'command: make check\n', 'command: make test\n')
WHERE id = 'tpl_system_default';

UPDATE templates
SET data = REPLACE(data, 'command: make build && make check', 'command: make build && make test')
WHERE id = 'tpl_system_default';
