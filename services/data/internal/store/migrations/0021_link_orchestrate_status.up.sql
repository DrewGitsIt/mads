-- Reports whether a link's orchestrate grant has materialized as live MCP
-- tools yet (pkg/contracts.OrchestrateStatus). Empty ('') means the link
-- carries no orchestrate grant; the store keeps this column in sync with
-- Grants on every write.
ALTER TABLE links ADD COLUMN orchestrate_status TEXT NOT NULL DEFAULT '';
