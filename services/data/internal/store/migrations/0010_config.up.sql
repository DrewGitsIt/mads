-- Global application configuration (MADS-UX §3.7 Settings): one row, the
-- whole Config struct stored as JSON, same shape as the registries. Seeded
-- with contracts.DefaultConfig() so a fresh install always has a row to
-- read — the first-run wizard flips onboarding_complete once it finishes.
CREATE TABLE config (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    data       TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO config (id, data, updated_at) VALUES (1,
 '{"synthesis_mode":"light","agent_selection_mode":"auto","default_model_tier":"standard","default_trust":"full","display_mode":"comfortable","keybindings":"default","theme":"dark","palette":"clay","concurrency_limit":3,"synthesis_debounce_ms":2000,"log_retention_days":30,"feature_flags":{"multi_repo":false,"linked_sessions":false,"remote_registry":false},"onboarding_complete":false}',
 '2026-07-12T00:00:00Z');
