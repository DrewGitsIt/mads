package orchestratemcp

// toolDefs is the tools/list response body — exactly the three tools this
// server implements, each mapping 1:1 onto one Router orchestrate route.
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
}
