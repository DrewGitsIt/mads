package goosecli

// The scripted stand-in binary: when MADS_FAKE_GOOSE=1, the test binary
// re-execs as a mock `goose acp` speaking the captured 1.48.0 wire shapes
// (the standard os/exec testing trick; pattern:
// providers/kirocli/fakekiro_test.go). Every adapter path — process spawn,
// request/response correlation, session/load replay, permission gating,
// interrupt — gets real subprocess coverage in CI with no goose installed.
//
// Deliberate divergences from goose that sharpen the tests:
//   - permission optionIds do NOT mirror the kind names (opt-a/opt-o/
//     opt-r/opt-x): an adapter that hardcoded goose's observed
//     "allow_once" id instead of selecting by kind fails every allow test.
//   - the fake only executes a gated tool when the reply selects one of
//     its allow ids, so denial is observable behavior, not inference.
// Everything else follows the captures: _meta.goose.toolCall on tool_call
// updates, four-option permission requests, usage on the session/prompt
// result, string UUID-ish ids on server→client requests, mcpServers
// required on session/new and session/load (-32602 without it), and
// noise variants (session_info_update, usage_update, agent_thought_chunk)
// sprinkled through turns to prove the decoder skips them.

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
	if os.Getenv("MADS_FAKE_GOOSE") == "1" {
		fakeGooseMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeGooseMain() {
	if len(os.Args) < 2 || os.Args[1] != "acp" {
		fmt.Fprintln(os.Stderr, "fakegoose: only `goose acp` is scripted in this task")
		os.Exit(1)
	}
	fgACP()
}

func fgSend(m rpcMessage) {
	raw, _ := json.Marshal(m)
	fmt.Println(string(raw))
}

// fgTranscriptEntry is one persisted turn — the cross-process memory a
// scripted, otherwise-stateless fake process needs to serve session/load
// replay and post-resume recall after a later process reattaches.
type fgTranscriptEntry struct {
	Role string `json:"role"` // "user" or "agent"
	Text string `json:"text"`
}

func fgAppendTranscript(path, role, text string) {
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_ = json.NewEncoder(f).Encode(fgTranscriptEntry{Role: role, Text: text})
}

func fgLoadTranscript(path string) []fgTranscriptEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var entries []fgTranscriptEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e fgTranscriptEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			entries = append(entries, e)
		}
	}
	return entries
}

// fgACP is the core JSON-RPC server loop. stdin lines feed a channel so a
// session/cancel notification (or a concurrent session/prompt) can be
// detected mid-turn.
func fgACP() {
	dir := os.Getenv("MADS_FAKE_GOOSE_DIR")
	noSysPrompt := os.Getenv("MADS_FAKE_GOOSE_NO_SYSPROMPT") == "1"
	var id, transcript, mode, model, sysPrompt string
	var history []string
	model = "fake-goose-default"

	msgs := make(chan rpcMessage)
	go func() {
		defer close(msgs)
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var m rpcMessage
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				msgs <- m
			}
		}
	}()

	respond := func(reqID json.RawMessage, result any) {
		raw, _ := json.Marshal(result)
		fgSend(rpcMessage{JSONRPC: "2.0", ID: reqID, Result: raw})
	}
	respondErr := func(reqID json.RawMessage, code int, message string, data any) {
		e := &rpcError{Code: code, Message: message}
		if data != nil {
			e.Data, _ = json.Marshal(data)
		}
		fgSend(rpcMessage{JSONRPC: "2.0", ID: reqID, Error: e})
	}
	notify := func(method string, params any) {
		raw, _ := json.Marshal(params)
		fgSend(rpcMessage{JSONRPC: "2.0", Method: method, Params: raw})
	}
	// update emits one session/update; fields is the update object itself,
	// built as a map so the fake can speak shapes (plan entries,
	// _meta.goose.toolCall) the adapter's own decode types don't marshal.
	update := func(fields map[string]any) {
		raw, _ := json.Marshal(fields)
		notify("session/update", sessionUpdateParams{SessionID: id, Update: raw})
	}
	chunk := func(variant, text string) {
		update(map[string]any{"sessionUpdate": variant, "content": map[string]any{"type": "text", "text": text}})
	}
	toolCall := func(callID, extension, tool, title string, rawInput any) {
		update(map[string]any{
			"sessionUpdate": "tool_call", "toolCallId": callID, "title": title, "rawInput": rawInput,
			"_meta": map[string]any{"goose": map[string]any{"toolCall": map[string]any{"toolName": tool, "extensionName": extension}}},
		})
	}
	toolDone := func(callID, status, text string) {
		update(map[string]any{
			"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": status,
			"content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": text}}},
		})
	}
	usage := &wireUsage{TotalTokens: 30, InputTokens: 20, OutputTokens: 10}

	permSeq := 0
	// requestPermission sends a real session/request_permission (string id,
	// per the captures) with optionIds that deliberately do NOT mirror the
	// kind names, and blocks for the client's reply — safe because the
	// client answers synchronously from within handleLine before reading
	// its next line. Only opt-o (allow_once) or opt-a (allow_always) count
	// as approval.
	requestPermission := func(callID, title string, rawInput any) bool {
		permSeq++
		reqID := json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("perm-%d", permSeq)))
		params, _ := json.Marshal(map[string]any{
			"sessionId": id,
			"toolCall":  map[string]any{"toolCallId": callID, "kind": "other", "status": "pending", "title": title, "rawInput": rawInput},
			"options": []map[string]any{
				{"optionId": "opt-a", "name": "Always allow", "kind": "allow_always"},
				{"optionId": "opt-o", "name": "Allow once", "kind": "allow_once"},
				{"optionId": "opt-r", "name": "Reject once", "kind": "reject_once"},
				{"optionId": "opt-x", "name": "Reject always", "kind": "reject_always"},
			},
		})
		fgSend(rpcMessage{JSONRPC: "2.0", ID: reqID, Method: "session/request_permission", Params: params})
		resp, ok := <-msgs
		if !ok {
			return false
		}
		var pr permissionResult
		_ = json.Unmarshal(resp.Result, &pr)
		return pr.Outcome.Outcome == "selected" && (pr.Outcome.OptionID == "opt-o" || pr.Outcome.OptionID == "opt-a")
	}

	// attemptTool runs the shared gated-tool dance: announce the tool_call
	// (goose emits it before the permission request), ask permission when
	// in approve mode, then either complete or fail the call.
	attemptTool := func(extension, tool, title string, rawInput any) string {
		permSeq++
		callID := fmt.Sprintf("call_%d", permSeq)
		toolCall(callID, extension, tool, title, rawInput)
		allowed := mode != "approve" || requestPermission(callID, title, rawInput)
		if allowed {
			toolDone(callID, "completed", "ran "+title)
			return "used " + title
		}
		toolDone(callID, "failed", "permission denied")
		return "cannot use " + title
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
			respond(m.ID, map[string]any{
				"protocolVersion":   1,
				"agentCapabilities": map[string]any{"loadSession": true},
				"agentInfo":         map[string]any{"name": "goose", "version": "1.48.0"},
			})

		case "session/new":
			if !hasMCPServers(m.Params) {
				respondErr(m.ID, -32602, "Invalid params", map[string]any{"error": "missing field `mcpServers`"})
				continue
			}
			id = fmt.Sprintf("20260908_%d", os.Getpid())
			if dir != "" {
				transcript = filepath.Join(dir, id+".jsonl")
			}
			respond(m.ID, newSessionResult{SessionID: id})

		case "session/load":
			if !hasMCPServers(m.Params) {
				respondErr(m.ID, -32602, "Invalid params", map[string]any{"error": "missing field `mcpServers`"})
				continue
			}
			var p loadSessionParams
			_ = json.Unmarshal(m.Params, &p)
			id = p.SessionID
			if dir != "" {
				transcript = filepath.Join(dir, id+".jsonl")
				entries := fgLoadTranscript(transcript)
				if len(entries) == 0 {
					respondErr(m.ID, -32603, "Session not found: "+id, nil)
					continue
				}
				for _, e := range entries {
					switch e.Role {
					case "user":
						history = append(history, e.Text)
						chunk("user_message_chunk", e.Text)
					case "agent":
						chunk("agent_message_chunk", e.Text)
					}
				}
			}
			respond(m.ID, map[string]any{})

		case "session/set_mode":
			var p setModeParams
			_ = json.Unmarshal(m.Params, &p)
			mode = p.ModeID
			update(map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": mode})
			respond(m.ID, map[string]any{})

		case "session/set_config_option":
			var p setConfigOptionParams
			_ = json.Unmarshal(m.Params, &p)
			if p.ConfigID == "model" {
				if p.Value == "bad-model" {
					respondErr(m.ID, -32602, "unknown model bad-model", nil)
					continue
				}
				model = p.Value
			}
			respond(m.ID, map[string]any{})

		case "_goose/unstable/session/system-prompt/set":
			if noSysPrompt {
				respondErr(m.ID, -32601, "Method not found", nil)
				continue
			}
			var p systemPromptParams
			_ = json.Unmarshal(m.Params, &p)
			sysPrompt = p.Text
			respond(m.ID, map[string]any{})

		case "session/prompt":
			var p promptParams
			_ = json.Unmarshal(m.Params, &p)
			var prompt string
			for _, b := range p.Prompt {
				prompt += b.Text
			}
			history = append(history, prompt)
			fgAppendTranscript(transcript, "user", prompt)

			// Real turns open with noise the decoder must skip.
			update(map[string]any{"sessionUpdate": "session_info_update", "_meta": map[string]any{"goose": map[string]any{"activeRunId": "run_fake"}}})
			chunk("agent_thought_chunk", "thinking...")

			reply := "ok"
			stopReason := "end_turn"
			switch {
			case strings.HasPrefix(prompt, "echo "):
				reply = "you said " + strings.TrimPrefix(prompt, "echo ")
			case strings.HasSuffix(prompt, "recall"):
				var parts []string
				for _, h := range history[:len(history)-1] {
					if strings.HasPrefix(h, "echo ") {
						parts = append(parts, strings.TrimPrefix(h, "echo "))
					}
				}
				reply = "earlier you said: " + strings.Join(parts, ", ")
			case strings.Contains(prompt, "reflect"):
				reply = prompt
			case strings.HasSuffix(prompt, "mode"):
				reply = "mode: " + mode
			case strings.HasSuffix(prompt, "model"):
				reply = "model: " + model
			case strings.HasSuffix(prompt, "sysprompt"):
				reply = "sysprompt: " + sysPrompt
			case strings.HasSuffix(prompt, "crash"):
				chunk("agent_message_chunk", "uh o")
				os.Exit(1) // dies mid-turn, no response
			case strings.HasSuffix(prompt, "auth"):
				respondErr(m.ID, -32000, "auth_required", nil)
				continue
			case strings.HasSuffix(prompt, "credits"):
				respondErr(m.ID, -32603, "Error in agent response stream",
					map[string]any{"reason": "credits_exhausted", "url": "https://example.com/topup"})
				continue
			case strings.HasSuffix(prompt, "refuse"):
				stopReason = "refusal"
				reply = ""
			case strings.HasSuffix(prompt, "cancelled-stop"):
				stopReason = "cancelled"
				reply = ""
			case strings.HasSuffix(prompt, "weird"):
				// An official v1 variant goose never emits, plus a stop
				// reason from the future — both must pass through cleanly.
				update(map[string]any{"sessionUpdate": "plan", "entries": []map[string]any{{"content": "step one", "priority": "high", "status": "pending"}}})
				stopReason = "some_future_stop"
				reply = "survived the plan"
			case strings.HasSuffix(prompt, "fail-tool"):
				permSeq++
				callID := fmt.Sprintf("call_%d", permSeq)
				toolCall(callID, "developer", "shell", "shell · false", map[string]any{"command": "false"})
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": "in_progress",
					"_meta": map[string]any{"toolNotification": map[string]any{"type": "live_output"}}})
				toolDone(callID, "failed", "boom: exit 1")
				reply = "the tool failed"
			case strings.HasSuffix(prompt, "attempt-shell"):
				reply = attemptTool("developer", "shell", "shell", map[string]any{"command": "echo hi"})
			case strings.HasSuffix(prompt, "attempt-view"):
				reply = attemptTool("developer", "text_editor", "text_editor view", map[string]any{"command": "view", "path": "/tmp/fake.txt"})
			case strings.HasSuffix(prompt, "attempt-write"):
				reply = attemptTool("developer", "text_editor", "text_editor write", map[string]any{"command": "write", "path": "/tmp/fake.txt", "file_text": "x"})
			case strings.HasSuffix(prompt, "attempt-unknown"):
				reply = attemptTool("mystery", "mystery_tool", "mystery_tool", map[string]any{})
			case strings.HasSuffix(prompt, "slow"):
				interrupted := false
				for i := 0; i < 400 && !interrupted; i++ {
					chunk("agent_message_chunk", fmt.Sprintf("tick %d ", i))
					select {
					case mm, ok := <-msgs:
						if !ok {
							return
						}
						switch mm.Method {
						case "session/cancel":
							interrupted = true
						case "session/prompt":
							respondErr(mm.ID, -32602, "session already has active run run_fake for session "+id, nil)
						}
					case <-time.After(5 * time.Millisecond):
					}
				}
				// Captured goose behavior: cancelled runs still resolve
				// stopReason end_turn — the client's own flag must carry
				// the interruption signal.
				respond(m.ID, promptResult{StopReason: "end_turn", Usage: usage})
				continue
			}

			if reply != "" {
				chunk("agent_message_chunk", reply)
				fgAppendTranscript(transcript, "agent", reply)
			}
			update(map[string]any{"sessionUpdate": "usage_update", "used": 30, "size": 128000})
			respond(m.ID, promptResult{StopReason: stopReason, Usage: usage})

		case "session/cancel":
			// No turn in flight to interrupt outside the slow branch above.

		case "session/close":
			respond(m.ID, map[string]any{})
		}
	}
}

// testProvider wires the adapter to the stand-in binary with an isolated
// cross-process session directory.
//
//nolint:unused // forward-built for the adapter's conformance tests (goose-adapter-tasks.yaml), which land next
func testProvider(t *testing.T, extraEnv ...string) *Provider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := append([]string{"MADS_FAKE_GOOSE=1", "MADS_FAKE_GOOSE_DIR=" + t.TempDir()}, extraEnv...)
	return New(Config{Bin: exe, Env: env})
}
