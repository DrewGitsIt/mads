package claudecode

// The scripted stand-in binary: when MADS_FAKE_CLAUDE=1, the test binary
// re-execs as a mock claude speaking the same stream-json wire protocol
// (the standard os/exec testing trick). Every adapter path — process spawn,
// protocol writer/reader, resume flags, transcript files — gets real
// subprocess coverage in CI with no claude installed.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("MADS_FAKE_CLAUDE") == "1" {
		fakeClaudeMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fcArgs is a loose parse of the claude flags the adapter uses.
type fcArgs struct {
	print             bool
	prompt            string
	outputFormat      string
	inputFormat       string
	resume            string
	forkSession       bool
	sessionID         string
	model             string
	disallowedTools   []string
	permissionMode    string
	permissionPrompt  string // --permission-prompt-tool
	mcpConfigPath     string
	strictMCPConfig   bool
	settingsPath      string
	systemPrompt      string
	safeMode          bool
	settingSources    string
	settingSourcesSet bool
	tools             string
	toolsSet          bool
}

func parseFCArgs(argv []string) fcArgs {
	var a fcArgs
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "-p", "--print":
			a.print = true
		case "--output-format":
			i++
			a.outputFormat = argv[i]
		case "--input-format":
			i++
			a.inputFormat = argv[i]
		case "--resume":
			i++
			a.resume = argv[i]
		case "--fork-session":
			a.forkSession = true
		case "--session-id":
			i++
			a.sessionID = argv[i]
		case "--model":
			i++
			a.model = argv[i]
		case "--disallowedTools":
			i++
			a.disallowedTools = strings.Split(argv[i], ",")
		case "--permission-mode":
			i++
			a.permissionMode = argv[i]
		case "--permission-prompt-tool":
			i++
			a.permissionPrompt = argv[i]
		case "--mcp-config":
			i++
			a.mcpConfigPath = argv[i]
		case "--strict-mcp-config":
			a.strictMCPConfig = true
		case "--settings":
			i++
			a.settingsPath = argv[i]
		case "--system-prompt":
			i++
			a.systemPrompt = argv[i]
		case "--safe-mode":
			a.safeMode = true
		case "--setting-sources":
			i++
			a.settingSources = argv[i]
			a.settingSourcesSet = true
		case "--tools":
			i++
			a.tools = argv[i]
			a.toolsSet = true
		case "--verbose", "--dangerously-skip-permissions":
		case "--append-system-prompt":
			i++
		default:
			if a.print && a.prompt == "" && !strings.HasPrefix(argv[i], "-") {
				a.prompt = argv[i]
			}
		}
	}
	return a
}

// fcMCPEcho reads back the --mcp-config file's contents and the
// --strict-mcp-config flag, so tests can assert the exact config the
// adapter wrote/passed reached the launched process — the same proof for
// both transports since fcStream and fcTerminal both call this.
func fcMCPEcho(a fcArgs) string {
	raw, err := os.ReadFile(a.mcpConfigPath)
	return fmt.Sprintf("mcp_config_err=%v strict=%v mcp_config=%s", err, a.strictMCPConfig, string(raw))
}

// fcPermissionEcho reports the permission flags the adapter passed (empty
// when a flag was never sent), so tests can assert each trust level's mode,
// prompt channel and removed tools reached the launched process on both
// transports.
func fcPermissionEcho(a fcArgs) string {
	return fmt.Sprintf("permission_mode=%s permission_prompt_tool=%s disallowed=%s",
		a.permissionMode, a.permissionPrompt, strings.Join(a.disallowedTools, ","))
}

// fcSettingsEcho reads back the --settings file's raw contents ("none" when
// the flag was never passed), so tests can assert the path-scope hook's
// settings JSON reached the launched process, and that a session without a
// persona allowlist never passes --settings at all.
func fcSettingsEcho(a fcArgs) string {
	if a.settingsPath == "" {
		return "settings=none"
	}
	raw, err := os.ReadFile(a.settingsPath)
	if err != nil {
		return fmt.Sprintf("settings_err=%v", err)
	}
	return "settings=" + string(raw)
}

// fcToolDenied reports whether name is in the parsed --disallowedTools list.
func fcToolDenied(name string, disallowed []string) bool {
	for _, d := range disallowed {
		if d == name {
			return true
		}
	}
	return false
}

func fakeClaudeMain() {
	a := parseFCArgs(os.Args[1:])
	switch {
	case a.print && a.outputFormat == "json":
		fcOneShot(a)
	case a.print && a.outputFormat == "stream-json" && a.inputFormat == "":
		fcOneShotStream(a)
	case a.print && a.inputFormat == "stream-json":
		fcStream(a)
	default:
		fcTerminal(a)
	}
}

// fcReply is the dialect shared with provider-fake's built-ins.
func fcReply(prompt string, history []string) (string, bool /*crash*/) {
	switch {
	case strings.HasPrefix(prompt, "echo "):
		return "you said " + strings.TrimPrefix(prompt, "echo "), false
	case prompt == "recall":
		var parts []string
		for _, h := range history {
			if strings.HasPrefix(h, "echo ") {
				parts = append(parts, strings.TrimPrefix(h, "echo "))
			}
		}
		return "earlier you said: " + strings.Join(parts, ", "), false
	case prompt == "crash":
		return "", true
	default:
		return "ok", false
	}
}

// fcOneShotTurn is the conversation half both one-shot output formats
// share: the session id (the resumed one, kept in place as real claude
// does, else a fresh one), the reply given that session's history, and the
// transcript append, so a later `-p --resume <id>` can recall it.
func fcOneShotTurn(a fcArgs) (id, reply string, crash bool) {
	id = fcOneShotID(a)
	dir := os.Getenv("MADS_FAKE_CLAUDE_DIR")
	var history []string
	if dir != "" && a.resume != "" {
		history = fcLoadHistory(filepath.Join(dir, "proj", a.resume+".jsonl"))
	}
	switch a.prompt {
	case "complete-echo":
		reply = fcCompleteEcho(a)
	case "fork-echo":
		reply = fcForkEcho(a)
	default:
		if reply, crash = fcReply(a.prompt, history); crash {
			return id, "", true
		}
	}
	if dir != "" {
		transcript := filepath.Join(dir, "proj", id+".jsonl")
		fcAppendTranscript(transcript, "user", a.prompt, "")
		fcAppendTranscript(transcript, "assistant", reply, "")
	}
	return id, reply, false
}

// fcOneShotID is a one-shot's session id: the resumed one, else fresh.
func fcOneShotID(a fcArgs) string {
	if a.resume != "" {
		return a.resume
	}
	return fmt.Sprintf("fc-oneshot-%d", os.Getpid())
}

func fcOneShot(a fcArgs) {
	if a.prompt == "ratelimit" {
		fmt.Fprintln(os.Stderr, "Rate limit reached. Try again at 5pm.")
		os.Exit(1)
	}
	id, reply, crash := fcOneShotTurn(a)
	if crash {
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(1)
	}
	out := map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": reply, "session_id": id,
		"usage":      map[string]any{"input_tokens": 7, "output_tokens": 11},
		"modelUsage": map[string]any{"fake-sonnet": map[string]any{}},
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}

// fcCompleteEcho reports every flag Complete (complete.go) is supposed to
// pass, so a unit test can assert the adapter actually built and forwarded
// them — a stand-in for the arg-building coverage the real hermeticity
// smoke test (complete_test.go, MADS_CLAUDE_SMOKE=1) can't give in CI.
func fcCompleteEcho(a fcArgs) string {
	settingsRaw := "none"
	if a.settingsPath != "" {
		if raw, err := os.ReadFile(a.settingsPath); err == nil {
			settingsRaw = string(raw)
		} else {
			settingsRaw = "err:" + err.Error()
		}
	}
	return fmt.Sprintf(
		"system=%s model=%s safe_mode=%v setting_sources=%q setting_sources_set=%v tools=%q tools_set=%v strict_mcp=%v mcp_config=%s settings=%s",
		a.systemPrompt, a.model, a.safeMode, a.settingSources, a.settingSourcesSet, a.tools, a.toolsSet, a.strictMCPConfig, a.mcpConfigPath, settingsRaw,
	)
}

// fcOneShotStream mimics `-p --output-format stream-json --verbose`: the
// reply streams as several assistant text lines (so tests can observe
// OnEvent firing more than once), closed by the same result line shape
// fcOneShot produces — the concatenation of the streamed pieces must equal
// the result line's "result" field.
func fcOneShotStream(a fcArgs) {
	if a.prompt == "ratelimit" {
		fmt.Fprintln(os.Stderr, "Rate limit reached. Try again at 5pm.")
		os.Exit(1)
	}
	id := fcOneShotID(a)
	if strings.HasPrefix(a.prompt, "edit ") {
		path := strings.TrimPrefix(a.prompt, "edit ")
		fcEmit(map[string]any{
			"type": "assistant", "session_id": id,
			"message": map[string]any{
				"role": "assistant",
				"content": []map[string]any{{
					"type": "tool_use", "name": "Edit",
					"input": map[string]any{"file_path": path, "old_string": "a", "new_string": "b"},
				}},
			},
		})
	}
	_, reply, crash := fcOneShotTurn(a)
	if crash {
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(1)
	}
	words := strings.Fields(reply)
	if len(words) == 0 {
		words = []string{reply}
	}
	for i, w := range words {
		text := w
		if i < len(words)-1 {
			text += " "
		}
		fcEmit(fcAssistantText(id, text))
	}
	fcEmit(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": reply, "session_id": id,
		"usage":      map[string]any{"input_tokens": 7, "output_tokens": 11},
		"modelUsage": map[string]any{"fake-sonnet": map[string]any{}},
	})
}

func fcEmit(v map[string]any) {
	raw, _ := json.Marshal(v)
	fmt.Println(string(raw))
}

func fcAssistantText(id, text string) map[string]any {
	return map[string]any{
		"type":       "assistant",
		"session_id": id,
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	}
}

// fcLine is a stdin line as the fake reads it: the adapter's own wireLine
// (which models the OUTPUT stream) plus the two control_request fields that
// only ever appear on input, so the fake can answer a request by id.
type fcLine struct {
	wireLine
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype string                     `json:"subtype"`
		Servers map[string]json.RawMessage `json:"servers"`
	} `json:"request"`
	// Response is a control_response the adapter wrote for one of the
	// fake's own inbound requests (can_use_tool, or an unknown subtype).
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Error     string `json:"error"`
		Response  struct {
			Behavior     string          `json:"behavior"`
			Message      string          `json:"message"`
			UpdatedInput json.RawMessage `json:"updatedInput"`
		} `json:"response"`
	} `json:"response"`
}

// fcAskTool runs the host-permission-channel dialect ("ask", "ask-subagent",
// "ask-cancel"): the shapes real claude 2.1.285 produces in manual mode for a
// write outside its working directory (testdata/stream-can-use-tool.jsonl).
// The call is announced, then — only when the adapter passed
// --permission-prompt-tool stdio — asked over a can_use_tool control_request
// whose answer decides the tool_result. Without the flag it is refused the
// way real claude refuses it. "ask-cancel" never gets an answer: it waits
// for the interrupt, withdraws the ask with control_cancel_request, and
// refuses the call the way claude does after a withdrawn prompt.
func fcAskTool(id string, a fcArgs, prompt string, lines <-chan fcLine) string {
	parent := ""
	agentID := ""
	if prompt == "ask-subagent" {
		parent, agentID = "toolu_agent", "agent-1"
		fcEmit(map[string]any{
			"type": "system", "subtype": "task_started", "session_id": id,
			"task_id": agentID, "tool_use_id": parent, "task_type": "local_agent",
			"description": "write a file",
		})
	}
	input := map[string]any{"file_path": "/outside/x.txt", "content": "hi"}
	call := map[string]any{
		"type": "assistant", "session_id": id,
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "tool_use", "id": "toolu_ask", "name": "Write", "input": input}},
		},
	}
	if parent != "" {
		call["parent_tool_use_id"] = parent
	}
	fcEmit(call)
	toolResult := func(text string, isError bool) {
		line := map[string]any{
			"type": "user", "session_id": id,
			"message": map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type": "tool_result", "tool_use_id": "toolu_ask", "content": text, "is_error": isError,
				}},
			},
		}
		if parent != "" {
			line["parent_tool_use_id"] = parent
		}
		fcEmit(line)
	}
	if a.permissionPrompt != "stdio" {
		toolResult("Claude requested permissions to write to /outside/x.txt, but you haven't granted it yet.", true)
		return "refused"
	}
	req := map[string]any{
		"subtype": "can_use_tool", "tool_name": "Write", "display_name": "Write",
		"input": input, "tool_use_id": "toolu_ask",
		"decision_reason": "Path is outside allowed working directories", "decision_reason_type": "workingDir",
	}
	if agentID != "" {
		req["agent_id"] = agentID
	}
	fcEmit(map[string]any{"type": "control_request", "request_id": "req-1", "request": req})
	for l := range lines {
		if prompt == "ask-cancel" && l.Type == "control_request" && l.Request.Subtype == "interrupt" {
			fcEmit(map[string]any{"type": "control_cancel_request", "request_id": "req-1"})
			toolResult("The user doesn't want to proceed with this tool use. The tool use was rejected.", true)
			return "cancelled"
		}
		if l.Type != "control_response" || l.Response.RequestID != "req-1" {
			continue
		}
		r := l.Response.Response
		if r.Behavior == "allow" {
			toolResult("File created successfully at: /outside/x.txt", false)
			return "decision=allow updated_input=" + string(r.UpdatedInput)
		}
		toolResult(r.Message, true)
		return "decision=" + r.Behavior + " message=" + r.Message
	}
	return "stdin closed"
}

// fcControlBogus sends the adapter an inbound control_request it can't
// know, and reports the control_response it got back — claude blocks on
// every inbound request, so the adapter must always answer.
func fcControlBogus(lines <-chan fcLine) string {
	fcEmit(map[string]any{"type": "control_request", "request_id": "req-bogus", "request": map[string]any{"subtype": "bogus"}})
	for l := range lines {
		if l.Type == "control_response" && l.Response.RequestID == "req-bogus" {
			return "reply=" + l.Response.Subtype + " error=" + l.Response.Error
		}
	}
	return "stdin closed"
}

// fcEmitListModels answers the model-discovery probe (discover.go). The
// shape mirrors the real catalog: floating aliases and concrete ids side by
// side, a "default" entry, and displayName carrying the label. Names are
// deliberately fake so a test asserting on them can never be satisfied by
// an accidentally-real binary.
func fcEmitListModels(requestID string) {
	fcEmit(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response": map[string]any{
				"models": []map[string]any{
					{"value": "default", "resolvedModel": "fake-sonnet-9", "displayName": "Default (recommended)"},
					{"value": "opus", "resolvedModel": "fake-opus-9", "displayName": "Fake Opus 9"},
					{"value": "fake-sonnet-9", "resolvedModel": "fake-sonnet-9", "displayName": "Fake Sonnet 9"},
				},
			},
		},
	})
}

// fcStream mimics the persistent stream-json session: init line, then one
// turn per user message, closed by a result line. A control_request
// interrupt lands mid-"slow"-turn. Its stdout and JSONL take the shapes
// claude 2.1.285 writes (fcConv), ids included, so the adapter's keys are
// exercised exactly as on the real binary.
func fcStream(a fcArgs) {
	dir := os.Getenv("MADS_FAKE_CLAUDE_DIR")
	id := fcSessionID(a)
	transcript := ""
	var history []string
	if dir != "" {
		transcript = filepath.Join(dir, "proj", id+".jsonl")
		history = fcLoadHistory(filepath.Join(dir, "proj", a.resume+".jsonl"))
	}
	f := &fcConv{id: id, transcript: transcript}

	fcEmit(map[string]any{
		"type": "system", "subtype": "init", "session_id": id, "model": "fake-sonnet",
	})

	// stdin lines feed a channel so interrupts can land mid-turn.
	lines := make(chan fcLine)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var l fcLine
			if json.Unmarshal(sc.Bytes(), &l) == nil {
				lines <- l
			}
		}
	}()

	result := func(text string) {
		fcEmit(map[string]any{
			"type": "result", "subtype": "success", "is_error": false,
			"result": text, "session_id": id,
			"usage": map[string]any{"input_tokens": 7, "output_tokens": len(text)/4 + 1},
		})
	}

	// dynamic mirrors claude's mcp_set_servers-managed server set.
	dynamic := map[string]json.RawMessage{}
	for l := range lines {
		if l.Type == "control_request" && l.Request.Subtype == "list_models" {
			fcEmitListModels(l.RequestID)
			continue
		}
		if l.Type == "control_request" && l.Request.Subtype == "mcp_set_servers" {
			added, removed := []string{}, []string{}
			for name := range l.Request.Servers {
				if _, ok := dynamic[name]; !ok {
					added = append(added, name)
				}
			}
			for name := range dynamic {
				if _, ok := l.Request.Servers[name]; !ok {
					removed = append(removed, name)
				}
			}
			dynamic = l.Request.Servers
			fcEmit(map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype": "success", "request_id": l.RequestID,
					"response": map[string]any{"added": added, "removed": removed, "errors": map[string]string{}},
				},
			})
			continue
		}
		if l.Type != "user" {
			continue
		}
		var prompt string
		for _, b := range l.Message.blocks() {
			prompt += b.Text
		}
		history = append(history, prompt)
		fcForkSeed(dir, a)
		f.user(prompt, l.UUID)

		if prompt == "fork-echo" {
			reply := fcForkEcho(a)
			f.say(reply)
			result(reply)
			continue
		}
		if prompt == "mcp-echo" {
			reply := fcMCPEcho(a)
			if len(dynamic) > 0 {
				raw, _ := json.Marshal(dynamic)
				reply += " dynamic=" + string(raw)
			}
			f.say(reply)
			result(reply)
			continue
		}
		if prompt == "permission-echo" {
			reply := fcPermissionEcho(a)
			f.say(reply)
			result(reply)
			continue
		}
		if prompt == "ask" || prompt == "ask-subagent" || prompt == "ask-cancel" {
			reply := fcAskTool(id, a, prompt, lines)
			f.say(reply)
			result(reply)
			continue
		}
		if prompt == "control-bogus" {
			reply := fcControlBogus(lines)
			f.say(reply)
			result(reply)
			continue
		}
		if prompt == "settings-echo" {
			reply := fcSettingsEcho(a)
			f.say(reply)
			result(reply)
			continue
		}
		// The 2026-09-30 wire shape: the user's turn ends normally, then a
		// backgrounded task finishes and the CLI wakes the model on its own.
		// That unprompted turn produces main-agent output and closes with a
		// result stamped origin "task-notification" — the line the adapter
		// correctly suppresses INSIDE a turn, and which outside one is the
		// only end that turn will ever get.
		if prompt == "unprompted" {
			reply := "ok"
			f.say(reply)
			result(reply)

			late := "the background task finished"
			f.say(late)
			fcEmit(map[string]any{
				"type": "result", "subtype": "success", "is_error": false,
				"result": late, "session_id": id,
				"origin": map[string]any{"kind": "task-notification"},
				"usage":  map[string]any{"input_tokens": 3, "output_tokens": 5},
			})
			continue
		}
		if prompt == "slow" {
			f.slow(lines)
			continue
		}
		// The stable_keys probe's text, tool, text turn: two API messages
		// with a Bash call and its result between them.
		if prompt == "tool-text" {
			f.message(fcText("Looking at the directory."), fcToolUse(f.next("toolu_fc"), "Bash", map[string]any{"command": "ls"}))
			f.toolResult("a.txt\nb.txt")
			f.say("Two files.")
			result("Two files.")
			continue
		}
		if prompt == "attempt-write" || prompt == "attempt-command" {
			name := "Write"
			if prompt == "attempt-command" {
				name = "Bash"
			}
			if fcToolDenied(name, a.disallowedTools) {
				reply := fmt.Sprintf("cannot use %s: disallowed by --disallowedTools", name)
				f.say(reply)
				result(reply)
				continue
			}
			fcEmit(map[string]any{
				"type": "assistant", "session_id": id,
				"message": map[string]any{
					"role": "assistant",
					"content": []map[string]any{{
						"type": "tool_use", "name": name, "input": map[string]any{},
					}},
				},
			})
			reply := "used " + name
			f.say(reply)
			result(reply)
			continue
		}
		if strings.HasPrefix(prompt, "edit ") {
			path := strings.TrimPrefix(prompt, "edit ")
			fcEmit(map[string]any{
				"type": "assistant", "session_id": id,
				"message": map[string]any{
					"role": "assistant",
					"content": []map[string]any{{
						"type": "tool_use", "name": "Edit",
						"input": map[string]any{"file_path": path, "old_string": "a", "new_string": "b"},
					}},
				},
			})
			fcAppendTranscript(transcript, "assistant", "", path)
		}
		reply, crash := fcReply(prompt, history[:len(history)-1])
		if crash {
			f.start()
			f.textDeltas(1, "uh o") // dies mid-turn: no message_stop, no result line
			os.Exit(1)
		}
		f.say(reply)
		result(reply)
	}
}

// fcConv writes one stream-json conversation the way claude 2.1.285 does
// (the 2026-10-20 survey captures): each API message opens with a
// message_start carrying its msg_ id; each content block streams between
// content_block_start and content_block_stop under its index, with the
// whole-block "assistant" line (its own uuid, the message's id) just
// before the stop; the message closes with message_delta and
// message_stop, and only then are its JSONL lines written, one block per
// line, the same uuids. Every message opens with a signature-only thinking
// block, as haiku's do by default. User lines keep the stdin "uuid" when
// one was sent.
type fcConv struct {
	id, transcript string
	seq            int
	msgID          string
	pending        []map[string]any // the open message's JSONL lines
	written        []map[string]any // every line stored so far
}

// next is a fresh id with the given prefix.
func (f *fcConv) next(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s_%d_%d", prefix, os.Getpid(), f.seq)
}

func (f *fcConv) uuid() string {
	f.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%06d%06d", os.Getpid()%1000000, f.seq)
}

// fcBlock is one content block of an assistant message.
type fcBlock map[string]any

func fcText(text string) fcBlock { return fcBlock{"type": "text", "text": text} }

func fcToolUse(id, name string, input map[string]any) fcBlock {
	return fcBlock{"type": "tool_use", "id": id, "name": name, "input": input}
}

func (f *fcConv) stream(event map[string]any) {
	fcEmit(map[string]any{"type": "stream_event", "session_id": f.id, "parent_tool_use_id": nil, "event": event})
}

// user records a user message (JSONL, then the --replay-user-messages
// echo), under uuid when the stdin line carried one.
func (f *fcConv) user(text, uuid string) {
	if uuid == "" {
		uuid = f.uuid()
	}
	line := map[string]any{
		"type": "user", "uuid": uuid,
		"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": text}}},
	}
	f.store(line)
	echo := map[string]any{"session_id": f.id, "parent_tool_use_id": nil, "isReplay": true}
	for k, v := range line {
		echo[k] = v
	}
	fcEmit(echo)
}

// start opens a message with its thinking block.
func (f *fcConv) start() {
	f.msgID = f.next("msg_fc")
	f.stream(map[string]any{"type": "message_start", "message": map[string]any{
		"id": f.msgID, "type": "message", "role": "assistant", "model": "fake-sonnet", "content": []any{},
	}})
	f.stream(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}})
	f.stream(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": ""}})
	f.stream(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "signature_delta", "signature": "c2ln"}})
	f.block(fcBlock{"type": "thinking", "thinking": "", "signature": "c2ln"})
	f.stream(map[string]any{"type": "content_block_stop", "index": 0})
}

// block writes one finished block's assistant line and queues its JSONL
// line.
func (f *fcConv) block(b fcBlock) {
	line := map[string]any{
		"type": "assistant", "uuid": f.uuid(),
		"message": map[string]any{
			"id": f.msgID, "type": "message", "role": "assistant", "model": "fake-sonnet",
			"content": []fcBlock{b},
		},
	}
	f.pending = append(f.pending, line)
	out := map[string]any{"session_id": f.id, "parent_tool_use_id": nil}
	for k, v := range line {
		out[k] = v
	}
	fcEmit(out)
}

// textDeltas streams text at index word by word.
func (f *fcConv) textDeltas(index int, text string) {
	f.stream(map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "text", "text": ""}})
	for _, chunk := range fcChunks(text) {
		f.stream(map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": chunk}})
	}
}

// fcChunks splits text into word-sized deltas that join back exactly.
func fcChunks(text string) []string {
	var out []string
	for text != "" {
		i := strings.IndexByte(text[1:], ' ')
		if i < 0 {
			return append(out, text)
		}
		out = append(out, text[:i+1])
		text = text[i+1:]
	}
	return out
}

// message streams one whole API message: thinking, then blocks.
func (f *fcConv) message(blocks ...fcBlock) {
	f.start()
	for i, b := range blocks {
		index := i + 1
		if b["type"] == "text" {
			f.textDeltas(index, b["text"].(string))
		} else {
			f.stream(map[string]any{"type": "content_block_start", "index": index, "content_block": b})
		}
		f.block(b)
		f.stream(map[string]any{"type": "content_block_stop", "index": index})
	}
	f.stop()
}

// stop closes the open message and writes its JSONL lines.
func (f *fcConv) stop() {
	f.stream(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}})
	f.stream(map[string]any{"type": "message_stop"})
	f.flush()
}

func (f *fcConv) flush() {
	for _, line := range f.pending {
		f.store(line)
	}
	f.pending = nil
}

// say is a one-text-block reply.
func (f *fcConv) say(text string) { f.message(fcText(text)) }

// toolResult answers the last message's tool call, live and stored.
func (f *fcConv) toolResult(text string) {
	var toolID string
	for _, l := range f.lastStored(1) {
		for _, b := range l["message"].(map[string]any)["content"].([]fcBlock) {
			if id, ok := b["id"].(string); ok {
				toolID = id
			}
		}
	}
	line := map[string]any{
		"type": "user", "uuid": f.uuid(),
		"message": map[string]any{"role": "user", "content": []map[string]any{{
			"type": "tool_result", "tool_use_id": toolID, "content": text, "is_error": false,
		}}},
	}
	out := map[string]any{"session_id": f.id, "parent_tool_use_id": nil}
	for k, v := range line {
		out[k] = v
	}
	fcEmit(out)
	f.store(line)
}

// lastStored is the last n assistant lines written, newest last.
func (f *fcConv) lastStored(n int) []map[string]any {
	var out []map[string]any
	for _, l := range f.written {
		if l["type"] == "assistant" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// slow streams "tick N " deltas until an interrupt arrives, then closes
// the way claude does: the partial block's assistant line, no
// message_stop, the interrupt marker, and an error_during_execution result
// with no text.
func (f *fcConv) slow(lines <-chan fcLine) {
	f.start()
	f.stream(map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text", "text": ""}})
	var text strings.Builder
	interrupted := false
	for i := 0; i < 400 && !interrupted; i++ {
		chunk := fmt.Sprintf("tick %d ", i)
		f.stream(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": chunk}})
		text.WriteString(chunk)
		select {
		case l, ok := <-lines:
			if ok && l.Type == "control_request" {
				interrupted = true
			}
		case <-time.After(5 * time.Millisecond):
		}
	}
	f.block(fcText(text.String()))
	if !interrupted {
		f.stream(map[string]any{"type": "content_block_stop", "index": 1})
		f.stop()
		fcEmit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": text.String(), "session_id": f.id})
		return
	}
	f.flush()
	marker := map[string]any{
		"type": "user", "uuid": f.uuid(),
		"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "[Request interrupted by user]"}}},
	}
	out := map[string]any{"session_id": f.id, "parent_tool_use_id": nil}
	for k, v := range marker {
		out[k] = v
	}
	fcEmit(out)
	f.store(marker)
	fcEmit(map[string]any{
		"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": f.id,
		"terminal_reason": "aborted_streaming", "usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
	})
}

// store appends line to the session's JSONL (stamped with cwd, as
// discoverRef's candidate filter reads it back) and remembers it.
func (f *fcConv) store(line map[string]any) {
	f.written = append(f.written, line)
	if f.transcript == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(f.transcript), 0o755)
	fh, err := os.OpenFile(f.transcript, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = fh.Close() }()
	cwd, _ := os.Getwd()
	stored := map[string]any{"cwd": cwd, "sessionId": f.id, "isSidechain": false}
	for k, v := range line {
		stored[k] = v
	}
	_ = json.NewEncoder(fh).Encode(stored)
}

// fcTerminal mimics the interactive UI for PTY probes: prompt, echo, prompt.
func fcTerminal(a fcArgs) {
	fmt.Print("> ")
	// Mirrors real claude-code PTY behavior: the transcript file appears
	// only once the first prompt is submitted, never at process spawn — a
	// fresh id since the terminal transport never announces one to resume.
	dir := os.Getenv("MADS_FAKE_CLAUDE_DIR")
	id := fcSessionID(a)
	transcript := ""
	if dir != "" {
		transcript = filepath.Join(dir, "proj", id+".jsonl")
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		text := strings.TrimSpace(sc.Text())
		if text == "fork-echo" {
			fcForkSeed(dir, a)
			fcAppendTranscript(transcript, "user", text, "")
			fmt.Printf("%s\r\n> ", fcForkEcho(a))
			continue
		}
		if text == "mcp-echo" {
			fmt.Printf("%s\r\n> ", fcMCPEcho(a))
			continue
		}
		if text == "permission-echo" {
			fmt.Printf("%s\r\n> ", fcPermissionEcho(a))
			continue
		}
		reply, crash := fcReply(text, nil)
		fcAppendTranscript(transcript, "user", text, "")
		if crash {
			os.Exit(1)
		}
		fcAppendTranscript(transcript, "assistant", reply, "")
		fmt.Printf("%s\r\n> ", reply)
	}
}

// fcSessionID is the id the stand-in session runs under: the pinned
// --session-id for a fork, else the resumed id (same file, appended to in
// place), else a fresh one.
func fcSessionID(a fcArgs) string {
	switch {
	case a.forkSession && a.sessionID != "":
		return a.sessionID
	case a.resume != "":
		return a.resume
	}
	return fmt.Sprintf("fc-%d", os.Getpid())
}

// fcForkSeed mirrors what a real fork does on the first prompt: the new
// session's transcript is created as a copy of the original's, which is left
// untouched. A no-op unless --fork-session was passed.
func fcForkSeed(dir string, a fcArgs) {
	if !a.forkSession || dir == "" {
		return
	}
	dst := filepath.Join(dir, "proj", fcSessionID(a)+".jsonl")
	if _, err := os.Stat(dst); err == nil {
		return
	}
	raw, err := os.ReadFile(filepath.Join(dir, "proj", a.resume+".jsonl"))
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	_ = os.WriteFile(dst, raw, 0o644)
}

// fcForkEcho reports the resume/fork flags the adapter passed.
func fcForkEcho(a fcArgs) string {
	return fmt.Sprintf("resume=%s fork=%v session_id=%s", a.resume, a.forkSession, a.sessionID)
}

// fcAppendTranscript writes claude's on-disk transcript format — string
// content for user lines, block content for assistant lines. Every line
// carries cwd (the subprocess's own working directory, set via cmd.Dir by
// the real adapter) — discoverRef's candidate filter reads exactly this
// field back.
func fcAppendTranscript(path, role, text, toolPath string) {
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	cwd, _ := os.Getwd()
	var line map[string]any
	switch {
	case role == "user":
		line = map[string]any{"type": "user", "cwd": cwd, "message": map[string]any{"role": "user", "content": text}}
	case toolPath != "":
		line = map[string]any{"type": "assistant", "cwd": cwd, "message": map[string]any{
			"role": "assistant",
			"content": []map[string]any{{
				"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": toolPath},
			}},
		}}
	default:
		line = map[string]any{"type": "assistant", "cwd": cwd, "message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": text}},
		}}
	}
	_ = json.NewEncoder(f).Encode(line)
}

func fcLoadHistory(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var history []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l transcriptLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Type != "user" || l.Message == nil {
			continue
		}
		for _, b := range l.Message.blocks() {
			if b.Type == "text" {
				history = append(history, b.Text)
			}
		}
	}
	return history
}

// testProvider wires the adapter to the stand-in binary with an isolated
// transcript dir.
func testProvider(t *testing.T) *Provider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return New(Config{
		Bin:           exe,
		Env:           []string{"MADS_FAKE_CLAUDE=1", "MADS_FAKE_CLAUDE_DIR=" + dir},
		TranscriptDir: dir,
		// Isolated and, by default, nonexistent — a test provider must never
		// read the real machine's ~/.claude/history.jsonl.
		HistoryFile: filepath.Join(dir, "history.jsonl"),
	})
}
