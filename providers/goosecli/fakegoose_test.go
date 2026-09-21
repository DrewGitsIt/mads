package goosecli

// The scripted stand-in goose: when MADS_FAKE_GOOSE=1, the test binary
// re-execs as a mock goose agent (the standard os/exec testing trick;
// pattern: providers/acprpc/fakeacp_test.go faMain, itself credited to the
// original fakegoose). Where acprpc's fake profiles are deliberately
// adversarial (optionIds that never mirror kinds, withheld capabilities),
// this fake's job is GOOSE SHAPE FIDELITY: it serves the captured goose
// 1.48.0 wire shapes (testdata/captures/acp-transcript.jsonl) — the
// initialize capability set, the session/new modes + configOptions body
// (including the category-"mode" option, so mode changes ride
// session/set_config_option), -32602 on a missing mcpServers field,
// bare _meta.goose.toolCall names, permission optionIds that mirror the
// kind names (goose's real coincidence — kind-selection robustness is
// acprpc's job, not repeated here), and the capture's prompt-result usage
// numbers. It also asserts its own argv (`acp --with-builtin developer`),
// proving the Builtins knob reaches the spawn. A gated tool only
// "executes" (tool_call_update completed) when the client selected one of
// the allow options; a reject produces a failed update — denial is
// observable behavior, not inference.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("MADS_FAKE_GOOSE") == "1" {
		fgMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fgMain() {
	// The argv assertion: the binding must spawn `goose acp --with-builtin
	// developer` (the captured spawn shape) — this is what proves the
	// Builtins knob reaches the child.
	if len(os.Args) < 4 || os.Args[1] != "acp" || os.Args[2] != "--with-builtin" || os.Args[3] != "developer" {
		fmt.Fprintf(os.Stderr, "fakegoose: argv = %v, want [acp --with-builtin developer]\n", os.Args[1:])
		os.Exit(1)
	}
	fgServe()
}

// fgAttempt is one gated-tool scenario, goose-shaped: bare _meta toolName
// with the extension in the separate extensionName field, humanized title.
type fgAttempt struct {
	ext, name, title string
	input            map[string]any
}

var fgAttempts = map[string]fgAttempt{
	"attempt-shell":   {ext: "developer", name: "shell", title: "shell · echo hi", input: map[string]any{"command": "echo hi"}},
	"attempt-view":    {ext: "developer", name: "text_editor", title: "text_editor view", input: map[string]any{"command": "view", "path": "/tmp/fake.txt"}},
	"attempt-write":   {ext: "developer", name: "text_editor", title: "text_editor write", input: map[string]any{"command": "write", "path": "/tmp/fake.txt", "file_text": "x"}},
	"attempt-unknown": {ext: "mystery", name: "mystery_tool", title: "mystery tool", input: map[string]any{}},
}

func fgSend(m fgMessage) {
	raw, _ := json.Marshal(m)
	fmt.Println(string(raw))
}

// fgMessage mirrors the wire's JSON-RPC line shape locally — this package
// deliberately doesn't reach into acprpc's unexported wire types.
type fgMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *fgError        `json:"error,omitempty"`
}

type fgError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func fgServe() {
	var id, mode, sysPrompt string
	model := "qwen3.8"
	mode = "auto"

	msgs := make(chan fgMessage)
	go func() {
		defer close(msgs)
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var m fgMessage
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				msgs <- m
			}
		}
	}()

	respond := func(reqID json.RawMessage, result any) {
		raw, _ := json.Marshal(result)
		fgSend(fgMessage{JSONRPC: "2.0", ID: reqID, Result: raw})
	}
	respondErr := func(reqID json.RawMessage, code int, message string, data any) {
		e := &fgError{Code: code, Message: message}
		if data != nil {
			e.Data, _ = json.Marshal(data)
		}
		fgSend(fgMessage{JSONRPC: "2.0", ID: reqID, Error: e})
	}
	notifyUpdate := func(fields map[string]any) {
		params, _ := json.Marshal(map[string]any{"sessionId": id, "update": fields})
		fgSend(fgMessage{JSONRPC: "2.0", Method: "session/update", Params: params})
	}
	chunk := func(text string) {
		notifyUpdate(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
	}
	toolCall := func(callID string, a fgAttempt) {
		notifyUpdate(map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": callID, "title": a.title, "rawInput": a.input,
			// The capture's _meta shape (acp-transcript.jsonl line 43): bare
			// toolName, extension in extensionName, vendor noise alongside.
			"_meta": map[string]any{"goose": map[string]any{
				"toolCall":  map[string]any{"toolName": a.name, "extensionName": a.ext},
				"created":   1788032677,
				"messageId": "chatcmpl-194",
			}},
		})
	}
	toolDone := func(callID, status, text string) {
		notifyUpdate(map[string]any{
			"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": status,
			"content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": text}}},
		})
	}
	// configOptions serves the captured session/new configOptions body
	// (acp-transcript.jsonl line 4, trimmed): a category-"mode" option (so
	// the engine's transport detection picks set_config_option) and the
	// select-typed model option whose currentValue marks the default.
	configOptions := func() []map[string]any {
		return []map[string]any{
			{"id": "provider", "name": "Provider", "type": "select", "currentValue": "ollama"},
			{"id": "mode", "name": "Mode", "category": "mode", "type": "select", "currentValue": mode},
			{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": model, "options": []map[string]any{
				{"value": "qwen2.5", "name": "Qwen 2.5"},
				{"value": "qwen3.8", "name": "Qwen 3.8"},
			}},
		}
	}

	permSeq := 0
	// requestPermission mirrors goose's real request: string id, no _meta,
	// and optionIds that MIRROR the kind names (goose's coincidence). Only
	// the allow ids count as approval.
	requestPermission := func(callID string, a fgAttempt) (allowed bool, outcome string) {
		permSeq++
		reqID := json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("perm-%d", permSeq)))
		options := []map[string]any{
			{"optionId": "allow_once", "name": "Allow once", "kind": "allow_once"},
			{"optionId": "allow_always", "name": "Always allow", "kind": "allow_always"},
			{"optionId": "reject_once", "name": "Reject once", "kind": "reject_once"},
			{"optionId": "reject_always", "name": "Always reject", "kind": "reject_always"},
		}
		params, _ := json.Marshal(map[string]any{
			"sessionId": id,
			"toolCall":  map[string]any{"toolCallId": callID, "status": "pending", "title": a.title, "rawInput": a.input},
			"options":   options,
		})
		fgSend(fgMessage{JSONRPC: "2.0", ID: reqID, Method: "session/request_permission", Params: params})
		resp, ok := <-msgs
		if !ok {
			return false, "eof"
		}
		var res struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}
		_ = json.Unmarshal(resp.Result, &res)
		if res.Outcome.Outcome != "selected" {
			return false, res.Outcome.Outcome
		}
		sel := res.Outcome.OptionID
		return sel == "allow_once" || sel == "allow_always", sel
	}

	// attemptTool runs the gated-tool dance: announce the tool_call, ask
	// permission when in approve mode (in auto mode goose asks nothing),
	// then complete or fail the call.
	attemptTool := func(a fgAttempt) string {
		permSeq++
		callID := fmt.Sprintf("call_%d", permSeq)
		toolCall(callID, a)
		allowed, outcome := true, "unasked"
		if mode == "approve" {
			allowed, outcome = requestPermission(callID, a)
		}
		if allowed {
			toolDone(callID, "completed", "ran "+a.title)
			return "used " + a.title
		}
		toolDone(callID, "failed", "The user has declined to run this tool")
		return "denied via " + outcome
	}

	hasMCPServers := func(params json.RawMessage) bool {
		var probe map[string]json.RawMessage
		_ = json.Unmarshal(params, &probe)
		_, ok := probe["mcpServers"]
		return ok
	}

	for m := range msgs {
		switch m.Method {
		case "initialize":
			// The captured goose 1.48.0 capability set (acp-transcript.jsonl
			// line 2): loadSession, presence-keyed sessionCapabilities
			// including close, http-but-not-sse MCP.
			respond(m.ID, map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"loadSession":         true,
					"promptCapabilities":  map[string]any{"image": true, "audio": false, "embeddedContext": true},
					"mcpCapabilities":     map[string]any{"http": true, "sse": false},
					"sessionCapabilities": map[string]any{"list": map[string]any{}, "delete": map[string]any{}, "close": map[string]any{}},
				},
			})

		case "session/new":
			if !hasMCPServers(m.Params) {
				respondErr(m.ID, -32602, "Invalid params", map[string]any{"error": "missing field `mcpServers`"})
				continue
			}
			id = fmt.Sprintf("20260921_%d", os.Getpid()%1000)
			modes := []map[string]any{}
			for _, mo := range []string{"auto", "approve", "smart_approve", "chat"} {
				modes = append(modes, map[string]any{"id": mo, "name": mo})
			}
			respond(m.ID, map[string]any{
				"sessionId":     id,
				"modes":         map[string]any{"currentModeId": mode, "availableModes": modes},
				"configOptions": configOptions(),
			})

		case "session/set_config_option":
			var p struct {
				ConfigID string `json:"configId"`
				Value    string `json:"value"`
			}
			_ = json.Unmarshal(m.Params, &p)
			switch p.ConfigID {
			case "mode":
				mode = p.Value
			case "model":
				model = p.Value
			}
			// Real goose replies with the refreshed configOptions body
			// (acp-transcript.jsonl line 12).
			respond(m.ID, map[string]any{"configOptions": configOptions()})

		case "session/set_mode":
			// Real goose supports this surface too; the engine's transport
			// detection picks the configOption, but accept both faithfully.
			var p struct {
				ModeID string `json:"modeId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			mode = p.ModeID
			respond(m.ID, map[string]any{})

		case "_goose/unstable/session/system-prompt/set":
			var p struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(m.Params, &p)
			sysPrompt = p.Text
			respond(m.ID, map[string]any{})

		case "session/prompt":
			var p struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(m.Params, &p)
			var prompt string
			for _, b := range p.Prompt {
				prompt += b.Text
			}

			reply := "ok"
			switch {
			case strings.HasPrefix(prompt, "echo "):
				reply = "you said " + strings.TrimPrefix(prompt, "echo ")
			case strings.HasSuffix(prompt, "sysprompt"):
				reply = "sysprompt: " + sysPrompt
			case strings.HasSuffix(prompt, "ratelimit"):
				// What escapes real goose after its internal retries: a
				// generic -32603 stream-error string (no structured data).
				respondErr(m.ID, -32603, "Error in agent response stream: 429 rate limit exceeded, please slow down", nil)
				continue
			case strings.HasSuffix(prompt, "fail-tool"):
				permSeq++
				callID := fmt.Sprintf("call_%d", permSeq)
				toolCall(callID, fgAttempts["attempt-shell"])
				toolDone(callID, "failed", "boom: exit 1")
				reply = "the tool failed"
			default:
				if a, ok := fgAttempts[lastWord(prompt)]; ok {
					reply = attemptTool(a)
				}
			}

			chunk(reply)
			// The capture's usage shape and numbers (acp-transcript.jsonl
			// line 85) ride the prompt result.
			raw, _ := json.Marshal(map[string]any{
				"stopReason": "end_turn",
				"usage":      map[string]any{"totalTokens": 8436, "inputTokens": 8415, "outputTokens": 21},
			})
			fgSend(fgMessage{JSONRPC: "2.0", ID: m.ID, Result: raw})

		case "session/cancel":
			// notification; nothing in flight to stop in these scenarios

		case "session/close":
			respond(m.ID, map[string]any{})

		default:
			if len(m.ID) > 0 {
				respondErr(m.ID, -32601, "Method not found", nil)
			}
		}
	}
}

func lastWord(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// testProvider wires the binding to the stand-in binary. Builtins stays
// defaulted so the spawn argv is the production shape (the fake asserts
// it); Env rides engineConfig → the engine's BaseEnv/Env seams → child.
func testProvider(t *testing.T, extraEnv ...string) *Provider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Bin: exe, Env: append([]string{"MADS_FAKE_GOOSE=1"}, extraEnv...)})
}
