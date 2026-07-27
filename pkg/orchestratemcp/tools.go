package orchestratemcp

// toolDefs is the full tools/list response body for a session holding the
// orchestrate grant — every tool this server implements, each mapping 1:1
// onto one Router orchestrate route. A session holding only the read grant
// gets readOnlyToolDefs() instead: everything except send_message, which
// stays orchestrate-only (item 1, "pull tools for read grants").
var toolDefs = []map[string]any{
	{
		"name":        "list_links",
		"description": "List the sessions this session holds an orchestrate link to.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	},
	{
		"name":        "get_summary",
		"description": "Get a linked subject session's current summary, or its recent transcript if no summary exists yet.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"subject_session_id": map[string]any{"type": "string"},
			},
			"required":             []string{"subject_session_id"},
			"additionalProperties": false,
		},
	},
	{
		"name":        "get_turns",
		"description": "Get the raw turn deltas for a cited range — drill down into a get_summary digest's turn range.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"subject_session_id": map[string]any{"type": "string"},
				"from_turn":          map[string]any{"type": "integer"},
				"to_turn":            map[string]any{"type": "integer"},
			},
			"required":             []string{"subject_session_id", "from_turn", "to_turn"},
			"additionalProperties": false,
		},
	},
	{
		"name":        "send_message",
		"description": "Send a message into a linked subject session's conversation. Delivered asynchronously; optionally wait briefly for a reply before returning.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"link_id": map[string]any{"type": "string"},
				"text":    map[string]any{"type": "string"},
				"wait_ms": map[string]any{"type": "integer", "description": "milliseconds to wait for a reply before returning, capped at 30000; omit or 0 for immediate return"},
			},
			"required":             []string{"link_id", "text"},
			"additionalProperties": false,
		},
	},
	{
		"name":        "set_attention",
		"description": "Flip your own attention level on a link you hold: per_turn to keep receiving pushed digests as the subject's turns complete, or manual_only to stop the push and catch up on demand via get_summary/get_turns instead.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"link_id": map[string]any{"type": "string"},
				"cadence": map[string]any{"type": "string", "enum": []string{"per_turn", "manual_only"}},
			},
			"required":             []string{"link_id", "cadence"},
			"additionalProperties": false,
		},
	},
}

// readOnlyToolDefs is toolDefs minus send_message — the slice spawned for a
// session that holds only the read grant (item 1).
func readOnlyToolDefs() []map[string]any {
	out := make([]map[string]any, 0, len(toolDefs))
	for _, t := range toolDefs {
		if t["name"] == "send_message" {
			continue
		}
		out = append(out, t)
	}
	return out
}
