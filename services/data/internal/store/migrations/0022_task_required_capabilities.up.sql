-- Task-local capability requirement deltas (owner decision: additive on top
-- of the workspace's Capabilities baseline — see contracts.Workspace).
ALTER TABLE tasks ADD COLUMN required_capabilities TEXT NOT NULL DEFAULT '[]'; -- JSON array of strings
