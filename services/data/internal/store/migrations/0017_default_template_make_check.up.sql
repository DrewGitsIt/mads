-- Close the lint-red-trunk gap (672a6bc): make check (lint+test) is the
-- land-gate bar, but the seeded default template's build/verify stages only
-- ran make test, so an in-diff lint error could clear a task's own gate and
-- still land. Route both stages through make check.
UPDATE templates
SET data = REPLACE(data, 'command: make build && make test', 'command: make build && make check')
WHERE id = 'tpl_system_default';

UPDATE templates
SET data = REPLACE(data, 'command: make test\n', 'command: make check\n')
WHERE id = 'tpl_system_default';
