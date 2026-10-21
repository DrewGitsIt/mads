package codexcli

import (
	"encoding/json"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// threadItem is the union of the app-server ThreadItem variants this
// adapter reads (schema: ThreadItem in codex_app_server_protocol.v2;
// shapes pinned by testdata/captures). One struct, fields by variant —
// the same item arrives live (item/started, item/completed) and from
// thread/items/list with the same id, which is what lets the live stream
// and the transcript agree.
type threadItem struct {
	Type string `json:"type"`
	ID   string `json:"id"`

	// userMessage
	ClientID string      `json:"clientId,omitempty"`
	Content  []userInput `json:"content,omitempty"`

	// agentMessage ("commentary" or "final_answer"), plan
	Text  string `json:"text,omitempty"`
	Phase string `json:"phase,omitempty"`

	// reasoning: summaries only — raw reasoning never reaches a client
	Summary []string `json:"summary,omitempty"`

	// commandExecution
	Command          string          `json:"command,omitempty"`
	Cwd              string          `json:"cwd,omitempty"`
	Status           string          `json:"status,omitempty"`
	CommandActions   []commandAction `json:"commandActions,omitempty"`
	AggregatedOutput *string         `json:"aggregatedOutput,omitempty"`
	ExitCode         *int            `json:"exitCode,omitempty"`

	// fileChange
	Changes []fileChange `json:"changes,omitempty"`

	// mcpToolCall / dynamicToolCall
	Server    string          `json:"server,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Result    *mcpResult      `json:"result,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Success *bool `json:"success,omitempty"`

	// webSearch
	Query   string          `json:"query,omitempty"`
	Action  json.RawMessage `json:"action,omitempty"`
	Results json.RawMessage `json:"results,omitempty"`

	// imageView
	Path string `json:"path,omitempty"`
}

type userInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
}

type commandAction struct {
	Type    string `json:"type"` // read, listFiles, search, unknown
	Command string `json:"command"`
	Path    string `json:"path,omitempty"`
}

type fileChange struct {
	Path string `json:"path"`
	Kind struct {
		Type string `json:"type"` // add, delete, update
	} `json:"kind"`
	Diff string `json:"diff"`
}

type mcpResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// itemKey is the item's conversation key (provider.Event.Key /
// provider.Message.Key): codex's own item id — the same on item/started,
// its deltas, item/completed and thread/items/list — for every item kind.
// A tool's call and result share it. Never the model-level call_…/ctc_…
// id, which only the rollout carries. A user message is keyed by the id
// mads sent it with (clientUserMessageId, stored as clientId) when there is
// one, so the key is mads' own; codex's item id otherwise (a message sent
// by another client, or before mads sent ids).
func itemKey(it *threadItem) string {
	if it.Type == "userMessage" {
		if it.ClientID != "" {
			return "user:" + it.ClientID
		}
		return "user:" + it.ID
	}
	return it.ID
}

// userText is a user message's text, parts joined. Images and other
// non-text input are skipped: provider.Message has no place for them.
func (it *threadItem) userText() string {
	var parts []string
	for _, c := range it.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// reasoningText is a reasoning item's summary, parts as paragraphs — the
// only thinking codex hands a client (encrypted content stays server-side).
func (it *threadItem) reasoningText() string {
	return strings.TrimSpace(strings.Join(it.Summary, "\n\n"))
}

// isTool reports whether the item is a tool call this adapter surfaces as
// EventToolUse/EventToolResult (and Role "tool"/"tool_result" rows).
func (it *threadItem) isTool() bool {
	switch it.Type {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch", "imageView":
		return true
	}
	return false
}

// shellCommand strips the login-shell wrapper codex puts around every
// command (`/bin/zsh -lc ls`, `/bin/zsh -lc 'touch a.txt'`) when the
// item's own commandActions carry the bare command; a single action is the
// common case and reads exactly as the model wrote it.
func (it *threadItem) shellCommand() string {
	if len(it.CommandActions) == 1 && it.CommandActions[0].Command != "" {
		return it.CommandActions[0].Command
	}
	return it.Command
}

// commandKind classifies a command by codex's own parse of it
// (commandActions): every action a read or listing → read, a search →
// search, anything else (or nothing parsed) → execute. Never guessed from
// the command text.
func (it *threadItem) commandKind() provider.ToolKind {
	if len(it.CommandActions) == 0 {
		return provider.ToolKindExecute
	}
	kind := provider.ToolKind("")
	for _, a := range it.CommandActions {
		var k provider.ToolKind
		switch a.Type {
		case "read", "listFiles":
			k = provider.ToolKindRead
		case "search":
			k = provider.ToolKindSearch
		default:
			return provider.ToolKindExecute
		}
		if kind != "" && kind != k {
			return provider.ToolKindExecute
		}
		kind = k
	}
	return kind
}

// toolUse is the item's normalized tool invocation. ID is codex's item id
// (exec-…, the same id approvals and the transcript use) — never the
// model-level call_…/ctc_… id, which no live event carries.
func (it *threadItem) toolUse() *provider.ToolUse {
	t := &provider.ToolUse{ID: it.ID}
	switch it.Type {
	case "commandExecution":
		cmd := it.shellCommand()
		t.Name = "shell"
		t.Kind = it.commandKind()
		t.Title = cmd
		t.Input = mustJSON(map[string]string{"command": cmd, "cwd": it.Cwd})
		for _, a := range it.CommandActions {
			if a.Path != "" {
				t.Locations = append(t.Locations, provider.ToolLocation{Path: a.Path})
				if t.Path == "" {
					t.Path = a.Path
				}
			}
		}
	case "fileChange":
		t.Name = "apply_patch"
		t.Kind = provider.ToolKindEdit
		t.Mutates = true
		allDeletes := len(it.Changes) > 0
		var paths []string
		for _, c := range it.Changes {
			paths = append(paths, c.Path)
			t.Locations = append(t.Locations, provider.ToolLocation{Path: c.Path})
			if c.Kind.Type != "delete" {
				allDeletes = false
			}
		}
		if allDeletes {
			t.Kind = provider.ToolKindDelete
		}
		if len(paths) > 0 {
			t.Path = paths[0]
		}
		t.Title = strings.Join(paths, ", ")
		t.Input = mustJSON(map[string]any{"changes": it.Changes})
	case "mcpToolCall":
		t.Name = it.Tool
		t.Kind = provider.ToolKindMCP
		t.Server = it.Server
		t.Title = it.Server + "." + it.Tool
		t.Input = it.Arguments
	case "dynamicToolCall":
		t.Name = it.Tool
		t.Kind = provider.ToolKindOther
		t.Title = it.Tool
		t.Input = it.Arguments
	case "webSearch":
		t.Name = "web_search"
		t.Kind = provider.ToolKindSearch
		t.Title = it.Query
		in := map[string]any{"query": it.Query}
		if len(it.Action) > 0 && string(it.Action) != "null" {
			in["action"] = it.Action
		}
		t.Input = mustJSON(in)
	case "imageView":
		t.Name = "view_image"
		t.Kind = provider.ToolKindRead
		t.Path = it.Path
		t.Title = it.Path
		t.Locations = []provider.ToolLocation{{Path: it.Path}}
		t.Input = mustJSON(map[string]string{"path": it.Path})
	}
	return t
}

// toolResult is a completed tool item's outcome: the text the call
// produced and whether it failed. streamed is the command output collected
// from item/commandExecution/outputDelta, used when the completed item
// carries no aggregatedOutput (it often carries null — testdata/captures).
// A declined call (the user, or nobody, refused it) is an error result with
// status blocked; codex reports that itself (status "declined").
func (it *threadItem) toolResult(streamed string) (text string, isError bool, status provider.ToolStatus) {
	switch it.Status {
	case "completed", "":
		status = provider.ToolStatusCompleted
	case "declined":
		status, isError = provider.ToolStatusBlocked, true
	case "inProgress":
		status = provider.ToolStatusInProgress
	default: // failed
		status, isError = provider.ToolStatusFailed, true
	}
	switch it.Type {
	case "commandExecution":
		if it.AggregatedOutput != nil {
			text = *it.AggregatedOutput
		} else {
			text = streamed
		}
		if it.ExitCode != nil && *it.ExitCode != 0 {
			isError = true
			if status == provider.ToolStatusCompleted {
				status = provider.ToolStatusFailed
			}
		}
		if text == "" && it.Status == "declined" {
			text = "declined"
		}
	case "fileChange":
		switch it.Status {
		case "completed":
			text = "applied"
		default:
			text = it.Status
		}
	case "mcpToolCall":
		if it.Error != nil {
			text, isError = it.Error.Message, true
		} else if it.Result != nil {
			var parts []string
			for _, c := range it.Result.Content {
				if c.Type == "text" {
					parts = append(parts, c.Text)
				}
			}
			text = strings.Join(parts, "\n")
		}
	case "dynamicToolCall":
		if it.Success != nil && !*it.Success {
			isError = true
		}
	case "webSearch":
		text = webResultsText(it.Results)
	}
	return text, isError, status
}

// webResultsText renders a webSearch item's results as "title — url" lines
// (the shape the survey captured: text_result entries with title/url).
func webResultsText(raw json.RawMessage) string {
	var results []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if json.Unmarshal(raw, &results) != nil {
		return ""
	}
	var lines []string
	for _, r := range results {
		switch {
		case r.Title != "" && r.URL != "":
			lines = append(lines, r.Title+" — "+r.URL)
		case r.URL != "":
			lines = append(lines, r.URL)
		case r.Title != "":
			lines = append(lines, r.Title)
		}
	}
	return strings.Join(lines, "\n")
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
