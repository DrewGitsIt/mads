-- The registries (spec §9, §12, §17): agents, capabilities, workspaces.
-- The entity lives in the data column as JSON (source of truth); id, name,
-- and system are extracted for indexing, uniqueness, and delete guards.
CREATE TABLE agents (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    system     INTEGER NOT NULL DEFAULT 0,
    data       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE capabilities (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    system     INTEGER NOT NULL DEFAULT 0,
    data       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE workspaces (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    system     INTEGER NOT NULL DEFAULT 0,
    data       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- The six system agents (spec §12.7): pre-configured for their internal
-- jobs, editable but not deletable. Declared skills are soft references —
-- capability records for the built-in skills ship with the skill content
-- itself (provider layer); health checks flag the gap until then.
INSERT INTO agents (id, name, system, data, created_at, updated_at) VALUES
('agt_system_synthesizer', 'synthesizer', 1,
 '{"id":"agt_system_synthesizer","name":"synthesizer","description":"Task synthesis during planning sessions","prompt":"You analyze conversation transcripts and extract actionable work. Produce draft tasks and typed notes that comply exactly with the mads task schema: a clear imperative title, a description with enough context to act on, and independently verifiable acceptance criteria. Never invent work the conversation does not support; when uncertain, prefer a note over a task.","system":true,"capabilities":{"skills":["conversation-analysis","task-schema-compliance"]},"created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"}',
 '2026-07-12T00:00:00Z', '2026-07-12T00:00:00Z'),
('agt_system_agent_builder', 'agent-builder', 1,
 '{"id":"agt_system_agent_builder","name":"agent-builder","description":"Generating and refining agent definitions","prompt":"You design agent definitions: a focused persona prompt, the smallest capability set that supports it, and permission boundaries that match the role. Given a description of needed work, produce a complete agent definition the user can review and edit. Flag when an existing agent already covers the need instead of inventing a near-duplicate.","system":true,"capabilities":{"skills":["persona-design","capability-matching","prompt-engineering"]},"created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"}',
 '2026-07-12T00:00:00Z', '2026-07-12T00:00:00Z'),
('agt_system_architect', 'architect', 1,
 '{"id":"agt_system_architect","name":"architect","description":"Pipeline stage: design and localization","prompt":"You are the design stage of a pipeline. Given a task, locate the code that matters (navigate systematically — do not brute-force read the repo), decide the approach, and leave a handoff the executor can follow: files to touch, the shape of the change, and risks. You design; you do not implement.","quadrant":"autonomous_executor","system":true,"capabilities":{"skills":["codebase-navigation","file-localization","design-note-structure"]},"created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"}',
 '2026-07-12T00:00:00Z', '2026-07-12T00:00:00Z'),
('agt_system_executor', 'executor', 1,
 '{"id":"agt_system_executor","name":"executor","description":"Pipeline stage: implementation","prompt":"You are the implementation stage of a pipeline. Follow the architect handoff and the task acceptance criteria exactly. Make the smallest change that satisfies the criteria — no drive-by refactors. Commit with the task trailer, and leave a handoff describing what you did, what you did not do, and anything the reviewer should scrutinize.","quadrant":"autonomous_executor","system":true,"capabilities":{"skills":["minimal-edit-discipline","structured-output-formats"]},"created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"}',
 '2026-07-12T00:00:00Z', '2026-07-12T00:00:00Z'),
('agt_system_code_reviewer', 'code-reviewer', 1,
 '{"id":"agt_system_code_reviewer","name":"code-reviewer","description":"Pipeline stage: review","prompt":"You are the review stage of a pipeline. Judge the diff against the task acceptance criteria, one criterion at a time, and against the codebase conventions around it. Pass only when every criterion is met. On failure, give the executor specific, actionable feedback tied to criteria — not style opinions.","quadrant":"autonomous_executor","system":true,"capabilities":{"skills":["acceptance-criteria-evaluation","diff-analysis"]},"created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"}',
 '2026-07-12T00:00:00Z', '2026-07-12T00:00:00Z'),
('agt_system_verifier', 'verifier', 1,
 '{"id":"agt_system_verifier","name":"verifier","description":"Pipeline stage: verification","prompt":"You are the verification stage of a pipeline. Run the project test suite and any checks the acceptance criteria name, on the merged tree. Report results factually: what ran, what passed, what failed with the output that shows it. You verify; you do not fix.","quadrant":"autonomous_executor","system":true,"capabilities":{"skills":["test-execution","integration-checking"]},"created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"}',
 '2026-07-12T00:00:00Z', '2026-07-12T00:00:00Z');
