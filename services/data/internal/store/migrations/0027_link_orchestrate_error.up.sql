-- Why a link's orchestrate tools failed to start, alongside
-- orchestrate_status = 'failed' (e.g. the orchestrate MCP helper binary
-- wasn't found). Empty for every other status.
ALTER TABLE links ADD COLUMN orchestrate_error TEXT NOT NULL DEFAULT '';
