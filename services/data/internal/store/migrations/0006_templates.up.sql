-- Pipeline templates (spec §6, M9): stored as YAML source — the single
-- artifact behind both editor views. Validation lives in pkg/template and is
-- shared with the Pipeline Engine, so the editor's Validate runs the same
-- checks as launch. In-flight runs freeze their own copy; edits here never
-- disturb them.
CREATE TABLE templates (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    system     INTEGER NOT NULL DEFAULT 0,
    data       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- The system default (M9): ships editable, like system agents, but not
-- deletable — it is the fallback every workspace can bind to.
INSERT INTO templates (id, name, system, data, created_at, updated_at) VALUES
('tpl_system_default', 'default-implementation', 1,
 '{"id":"tpl_system_default","name":"default-implementation","description":"The standard implementation pipeline: design, implement, build, review, merge, verify \u2014 with review and build failures looping back to the executor.","source":"name: default-implementation\nstages:\n  - name: architect\n    agent: architect\n    when: ''!contains(labels, \"trivial\")''\n  - name: executor\n    agent: executor\n  - name: build\n    command: make build && make test\n    on_fail: { loop_back: executor, max_loops: 2 }\n  - name: code_review\n    agent: code-reviewer\n    on_fail: { loop_back: executor, max_loops: 2, then: block }\n  - name: merge\n    command: git merge --no-ff\n  - name: verify\n    command: make test\n    on_fail: { then: block }\n","system":true,"created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"}',
 '2026-07-12T00:00:00Z', '2026-07-12T00:00:00Z');
