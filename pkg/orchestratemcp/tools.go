package orchestratemcp

import "github.com/drewkidwell/mads/pkg/contracts"

// sessionParam is the `session` argument every per-session tool takes: the
// linked session's name, resolved per call against list_linked_sessions
// (resolve.go). Names over link or session IDs (2026-10-19): a name is what
// the agent reads in its conversation and what the user calls the session,
// so it's the handle the agent reaches for.
var sessionParam = map[string]any{
	"type":        "string",
	"description": "The linked session's name, as list_linked_sessions shows it. Its session ID also works.",
}

// toolDefs is the full tools/list response body for a session holding the
// orchestrate grant — every tool this server implements, each mapping onto
// one Router orchestrate route. A session holding only the read grant gets
// readOnlyToolDefs() instead: everything except message_session, which
// stays orchestrate-only (item 1, "pull tools for read grants").
//
// Fixed verbs named for what the agent wants done, with descriptions that
// say what happens on the other side (2026-10-19): a leader that can't tell
// what these tools do won't pick them to reach its linked sessions.
var toolDefs = []map[string]any{
	{
		"name": "list_linked_sessions",
		"description": "List the sessions the user has linked to you, and how you can reach each: its name (pass it as `session` to the other tools), " +
			"session ID, grants (orchestrate lets you message it; read lets you read its summary and turns), " +
			"what it's doing right now, its cadence (how its progress comes back to you), " +
			"and turns (how many turns its conversation has, numbered from 1, for get_session_turns).",
		"inputSchema": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	},
	{
		"name": "message_session",
		"description": "Send a message to a linked session. It appears in that session's conversation, attributed to you; " +
			"if the session is in the middle of a turn, the message queues until that turn ends. " +
			"The session's reply comes back to you as a digest. Set wait_ms to wait briefly for the reply and get it in this result.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": sessionParam,
				"text":    map[string]any{"type": "string", "description": "The message, as you want the session to read it."},
				"wait_ms": map[string]any{"type": "integer", "description": "How long to wait for the reply before returning, in milliseconds, up to 30000. Omit or 0 to return as soon as the message is delivered."},
			},
			"required":             []string{"session", "text"},
			"additionalProperties": false,
		},
	},
	{
		"name": "get_session_summary",
		"description": "Read a compressed account of what a linked session has said and done, focused on its replies. " +
			"Cheap in tokens, so use it first, then read the exact record with get_session_turns, e.g. count: 3 for the last three turns. " +
			"It also says how many turns the session has.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": sessionParam,
			},
			"required":             []string{"session"},
			"additionalProperties": false,
		},
	},
	{
		"name": "get_session_turns",
		"description": "Read a linked session's conversation exactly, turn by turn: the message that started each turn " +
			"(from the user, or from you or another linked session), its words verbatim, and every tool call it made " +
			"with the call's input and output (long ones truncated). " +
			"Use it to audit what the session actually did, or to read exactly what it said. " +
			"A turn starts at each message the session is sent; turns are numbered from 1, and list_linked_sessions says how many there are. " +
			"Pass count alone for the last count turns (the default is the last one), or from_turn with count to read from a given turn.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":   sessionParam,
				"count":     map[string]any{"type": "integer", "minimum": 1, "description": "How many turns to read. Defaults to 1."},
				"from_turn": map[string]any{"type": "integer", "minimum": 1, "description": "The turn to start at, counting from 1. Omit to read the last count turns."},
			},
			"required":             []string{"session"},
			"additionalProperties": false,
		},
	},
	{
		"name": "check_mailbox",
		"description": "Collect the digests waiting from a linked session. On an on_signal link, the session's turn digests wait here " +
			"instead of coming to you after each turn. This returns everything that arrived since you were last woken " +
			"or last checked, oldest first, and clears it.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": sessionParam,
			},
			"required":             []string{"session"},
			"additionalProperties": false,
		},
	},
	{
		"name": "set_attention",
		"description": "Choose how a linked session's progress reaches you. per_turn: a digest each time it finishes a turn. " +
			"on_signal: you're woken only when it needs attention (a permission request, a crash, a task done or blocked), " +
			"and other digests wait for check_mailbox. manual_only: nothing is sent; you catch up with get_session_summary " +
			"and get_session_turns when you choose.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": sessionParam,
				"cadence": map[string]any{"type": "string", "enum": cadenceEnum()},
			},
			"required":             []string{"session", "cadence"},
			"additionalProperties": false,
		},
	},
}

// cadenceEnum derives set_attention's cadence enum from the contracts
// registry, so a new cadence value can never silently miss this schema.
func cadenceEnum() []string {
	vals := make([]string, len(contracts.LinkCadences))
	for i, c := range contracts.LinkCadences {
		vals[i] = string(c)
	}
	return vals
}

// readOnlyToolDefs is toolDefs minus message_session — the slice spawned
// for a session that holds only the read grant (item 1).
func readOnlyToolDefs() []map[string]any {
	out := make([]map[string]any, 0, len(toolDefs))
	for _, t := range toolDefs {
		if t["name"] == "message_session" {
			continue
		}
		out = append(out, t)
	}
	return out
}

// instructions is the initialize result's server instructions: a harness
// shows them to the model alongside the tools, so this is where the server
// says what the tools are for as a set (2026-10-19). Deliberately names no
// sessions — the server starts once per process while links come and go
// under it — and no other way of reaching a session: it says what these
// tools do, not what to avoid.
const instructions = "The user has linked you to other MADS sessions and suggests you communicate with them through these tools: " +
	"list_linked_sessions to see who they are, message_session to send them instructions or answers, " +
	"get_session_summary to read their replies, get_session_turns to audit their exact tool calls. " +
	"Messages you send appear in the linked session's conversation, attributed to you; its replies come back to you as digests."

// readOnlyInstructions is instructions for a read-grant-only server, which
// has no message_session: the user linked these sessions for watching.
const readOnlyInstructions = "The user has linked you to other MADS sessions so you can follow their work through these tools: " +
	"list_linked_sessions to see who they are, get_session_summary to read what they said and did, " +
	"get_session_turns to audit their exact tool calls. " +
	"Their progress also comes back to you as digests, as each link's cadence sets (see set_attention)."
