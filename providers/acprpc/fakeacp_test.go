package acprpc

// The scripted stand-in agent: when MADS_FAKE_ACP=1, the test binary
// re-execs as a mock ACP agent (the standard os/exec testing trick;
// pattern: providers/kirocli/fakekiro_test.go, hoisted from the goose
// adapter's fakegoose). "Agent scripts as data" = one generic agent loop
// parameterized by a profile struct (capabilities advertised, adversarial
// optionId spellings per kind, mode ids and transport, cancel stop-reason,
// tool-identity emission style), selected by MADS_FAKE_ACP_PROFILE; the
// prompt-suffix scenario dispatch is shared across profiles. A third mode
// (MADS_FAKE_ACP_REPLAY=<capture path>) serves the committed goose 1.48.0
// captures byte-for-byte — see replay_test.go.
//
// Deliberate adversarial choices that sharpen the tests:
//   - permission optionIds NEVER mirror the kind names (opt-o/syn-yes/…):
//     an engine that hardcoded an observed "allow_once" id instead of
//     selecting by kind fails every allow test.
//   - the fake only executes a gated tool when the reply selects one of
//     its own allow ids, so denial is observable behavior, not inference.
//   - the synthetic profile withholds loadSession and close capabilities,
//     runs its mode through a category-"mode" configOption, resolves
//     cancels with stopReason "cancelled", and offers hermes-style
//     two-option and _always-only permission menus.

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
	if os.Getenv("MADS_FAKE_ACP") == "1" {
		faMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func faMain() {
	if len(os.Args) < 2 || os.Args[1] != "acp" {
		fmt.Fprintln(os.Stderr, "fakeacp: expected the `acp` spawn arg")
		os.Exit(1)
	}
	if path := os.Getenv("MADS_FAKE_ACP_REPLAY"); path != "" {
		faReplayMain(path)
		return
	}
	name := os.Getenv("MADS_FAKE_ACP_PROFILE")
	profile, ok := faProfiles[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "fakeacp: unknown profile %q\n", name)
		os.Exit(1)
	}
	faServe(profile)
}

// faProfile is one agent script's parameter set — the data expressing how
// a conforming agent differs from another within the spec.
type faProfile struct {
	loadSession bool
	closeCap    bool // advertise sessionCapabilities.close
	httpMCP     bool // advertise mcpCapabilities.http
	// metaNS non-empty → tool_call updates carry _meta.<ns>.toolCall
	// identity and no kind (goose's style); empty → the spec kind field is
	// set instead (codex/hermes style).
	metaNS string
	// modeConfigID non-empty → the session result advertises a
	// category-"mode" configOption with this id and mode changes must ride
	// session/set_config_option; empty → session/set_mode.
	modeConfigID    string
	availableModes  []string
	askMode         string // mode id in which gated tools request permission
	sysPromptMethod string // extension method served; "" = none (always -32601)
	cancelStop      string // stopReason a cancelled turn resolves with
	requireMCP      bool   // -32602 when session/new-or-load lacks mcpServers
	// requireAbsCwd → -32602 when session/new-or-load's cwd is not an
	// absolute path. goose 1.51 enforces this in the real binary; the fake
	// mirrors it so the engine's absolutization stays covered.
	requireAbsCwd bool
	optionID      map[string]string
}

var faProfiles = map[string]faProfile{
	"goose": {
		loadSession: true, closeCap: true, httpMCP: true,
		metaNS:         "goose",
		availableModes: []string{"auto", "approve"}, askMode: "approve",
		sysPromptMethod: "_goose/unstable/session/system-prompt/set",
		cancelStop:      "end_turn", requireMCP: true, requireAbsCwd: true,
		optionID: map[string]string{
			"allow_once": "opt-o", "allow_always": "opt-a",
			"reject_once": "opt-r", "reject_always": "opt-x",
		},
	},
	"synthetic": {
		loadSession: false, closeCap: false, httpMCP: false,
		modeConfigID:   "permission-mode",
		availableModes: []string{"vendor-full", "vendor-safe"}, askMode: "vendor-safe",
		cancelStop: "cancelled",
		optionID: map[string]string{
			"allow_once": "syn-yes", "allow_always": "syn-yes-4ever",
			"reject_once": "syn-no", "reject_always": "syn-no-4ever",
		},
	},
}

// faAttempt is one gated-tool scenario: identity for both emission styles
// plus the permission-option kinds offered (nil = all four). askFirst
// requests permission BEFORE any tool_call announcement, carrying the
// identity inside the request's own toolCall (kiro-cli's shape) — the
// engine must gate on that embedded identity, since there is no announced
// call to correlate with.
type faAttempt struct {
	ext, name, kind, title string
	input                  map[string]any
	offer                  []string
	askFirst               bool
}

var faAttempts = map[string]faAttempt{
	"attempt-shell":   {ext: "developer", name: "shell", kind: "execute", title: "shell", input: map[string]any{"command": "echo hi"}},
	"attempt-view":    {ext: "developer", name: "text_editor", kind: "read", title: "text_editor view", input: map[string]any{"command": "view", "path": "/tmp/fake.txt"}},
	"attempt-write":   {ext: "developer", name: "text_editor", kind: "edit", title: "text_editor write", input: map[string]any{"command": "write", "path": "/tmp/fake.txt", "file_text": "x"}},
	"attempt-unknown": {ext: "mystery", name: "mystery_tool", kind: "", title: "mystery_tool", input: map[string]any{}},
	// The permission-menu shape variants (hermes offers only a two-option
	// menu with no _always pair — testdata/captures/README.md).
	"attempt-view-two":        {ext: "developer", name: "text_editor", kind: "read", title: "text_editor view", input: map[string]any{"command": "view", "path": "/tmp/fake.txt"}, offer: []string{"allow_once", "reject_once"}},
	"attempt-view-always":     {ext: "developer", name: "text_editor", kind: "read", title: "text_editor view", input: map[string]any{"command": "view", "path": "/tmp/fake.txt"}, offer: []string{"allow_always", "reject_always"}},
	"attempt-shell-always":    {ext: "developer", name: "shell", kind: "execute", title: "shell", input: map[string]any{"command": "echo hi"}, offer: []string{"allow_always", "reject_always"}},
	"attempt-shell-allowonly": {ext: "developer", name: "shell", kind: "execute", title: "shell", input: map[string]any{"command": "echo hi"}, offer: []string{"allow_once", "allow_always"}},
	// Ask-before-announce (kiro-cli's shape): permission is requested with
	// the identity embedded in the request's toolCall, no prior tool_call
	// update to correlate with.
	"attempt-write-askfirst": {ext: "developer", name: "text_editor", kind: "edit", title: "text_editor write", input: map[string]any{"command": "write", "path": "/tmp/fake.txt", "file_text": "x"}, askFirst: true},
	"attempt-shell-askfirst": {ext: "developer", name: "shell", kind: "execute", title: "shell", input: map[string]any{"command": "echo hi"}, askFirst: true},
}

func faSend(m rpcMessage) {
	raw, _ := json.Marshal(m)
	fmt.Println(string(raw))
}

// faTranscriptEntry is one persisted turn — the cross-process memory an
// otherwise-stateless fake process needs to serve session/load replay and
// post-resume recall after a later process reattaches.
type faTranscriptEntry struct {
	Role string `json:"role"` // "user" or "agent"
	Text string `json:"text"`
}

func faAppendTranscript(path, role, text string) {
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_ = json.NewEncoder(f).Encode(faTranscriptEntry{Role: role, Text: text})
}

func faLoadTranscript(path string) []faTranscriptEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var entries []faTranscriptEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e faTranscriptEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			entries = append(entries, e)
		}
	}
	return entries
}

// faServe is the core JSON-RPC server loop. stdin lines feed a channel so
// a session/cancel notification can be detected mid-turn.
func faServe(pr faProfile) {
	dir := os.Getenv("MADS_FAKE_ACP_DIR")
	noSysPrompt := os.Getenv("MADS_FAKE_ACP_NO_SYSPROMPT") == "1"
	var id, transcript, mode, sysPrompt string
	var mcpNames []string
	var history []string
	model := "fake-default"
	mode = pr.availableModes[0]

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
		faSend(rpcMessage{JSONRPC: "2.0", ID: reqID, Result: raw})
	}
	respondErr := func(reqID json.RawMessage, code int, message string, data any) {
		e := &rpcError{Code: code, Message: message}
		if data != nil {
			e.Data, _ = json.Marshal(data)
		}
		faSend(rpcMessage{JSONRPC: "2.0", ID: reqID, Error: e})
	}
	notifyUpdate := func(fields map[string]any) {
		raw, _ := json.Marshal(fields)
		params, _ := json.Marshal(sessionUpdateParams{SessionID: id, Update: raw})
		faSend(rpcMessage{JSONRPC: "2.0", Method: "session/update", Params: params})
	}
	chunk := func(variant, text string) {
		u := map[string]any{"sessionUpdate": variant, "content": map[string]any{"type": "text", "text": text}}
		if variant == "agent_message_chunk" || variant == "user_message_chunk" {
			u["messageId"] = fmt.Sprintf("fa-msg-%s-%d", variant, time.Now().UnixNano())
		}
		notifyUpdate(u)
	}
	toolCall := func(callID string, a faAttempt) {
		u := map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": a.title, "rawInput": a.input}
		if pr.metaNS != "" {
			u["_meta"] = map[string]any{pr.metaNS: map[string]any{"toolCall": map[string]any{"toolName": a.name, "extensionName": a.ext}}}
		} else if a.kind != "" {
			u["kind"] = a.kind
		}
		notifyUpdate(u)
	}
	toolDone := func(callID, status, text string) {
		notifyUpdate(map[string]any{
			"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": status,
			"content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": text}}},
		})
	}
	sessionResultBody := func(includeID bool) map[string]any {
		modes := make([]map[string]any, 0, len(pr.availableModes))
		for _, m := range pr.availableModes {
			modes = append(modes, map[string]any{"id": m, "name": m})
		}
		opts := []map[string]any{{
			"id": "model", "name": "Model", "category": "model", "type": "select",
			"currentValue": model,
			"options": []map[string]any{
				{"value": "fake-default", "name": "Fake Default"},
				{"value": "fake-alt", "name": "Fake Alt"},
			},
		}}
		if pr.modeConfigID != "" {
			opts = append(opts, map[string]any{"id": pr.modeConfigID, "name": "Permission mode", "category": "mode"})
		}
		body := map[string]any{
			"modes":         map[string]any{"currentModeId": mode, "availableModes": modes},
			"configOptions": opts,
		}
		if includeID {
			body["sessionId"] = id
		}
		return body
	}
	usage := &wireUsage{TotalTokens: 30, InputTokens: 20, OutputTokens: 10}

	permSeq := 0
	// requestPermission sends a real session/request_permission (string id,
	// per the captures) with the profile's adversarial optionIds and blocks
	// for the client's reply — safe because the client answers synchronously
	// from within its read loop. Only the profile's own allow ids count as
	// approval; the returned outcome string makes the agent-side observation
	// assertable ("denied via cancelled" vs "denied via <optionId>").
	requestPermission := func(callID string, a faAttempt) (allowed bool, outcome string) {
		permSeq++
		reqID := json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("perm-%d", permSeq)))
		offer := a.offer
		if offer == nil {
			offer = []string{"allow_always", "allow_once", "reject_once", "reject_always"}
		}
		options := make([]map[string]any, 0, len(offer))
		for _, kind := range offer {
			options = append(options, map[string]any{"optionId": pr.optionID[kind], "name": "Option " + pr.optionID[kind], "kind": kind})
		}
		tc := map[string]any{"toolCallId": callID, "status": "pending", "title": a.title, "rawInput": a.input}
		if a.askFirst {
			// Unannounced call: the request itself carries the identity, in
			// the profile's emission style.
			if pr.metaNS != "" {
				tc["_meta"] = map[string]any{pr.metaNS: map[string]any{"toolCall": map[string]any{"toolName": a.name, "extensionName": a.ext}}}
			} else if a.kind != "" {
				tc["kind"] = a.kind
			}
		}
		params, _ := json.Marshal(map[string]any{
			"sessionId": id,
			"toolCall":  tc,
			"options":   options,
		})
		faSend(rpcMessage{JSONRPC: "2.0", ID: reqID, Method: "session/request_permission", Params: params})
		resp, ok := <-msgs
		if !ok {
			return false, "eof"
		}
		var res permissionResult
		_ = json.Unmarshal(resp.Result, &res)
		if res.Outcome.Outcome != "selected" {
			return false, res.Outcome.Outcome
		}
		sel := res.Outcome.OptionID
		return sel == pr.optionID["allow_once"] || sel == pr.optionID["allow_always"], sel
	}

	// attemptTool runs the shared gated-tool dance: announce the tool_call
	// (agents emit it before the permission request), ask permission when in
	// the profile's ask mode, then either complete or fail the call. An
	// askFirst attempt inverts the order — permission first, and the call is
	// only ever announced (then completed) when the reply allowed it.
	attemptTool := func(a faAttempt) string {
		permSeq++
		callID := fmt.Sprintf("call_%d", permSeq)
		if !a.askFirst {
			toolCall(callID, a)
		}
		allowed, outcome := true, "unasked"
		if mode == pr.askMode {
			allowed, outcome = requestPermission(callID, a)
		}
		if allowed {
			if a.askFirst {
				toolCall(callID, a)
			}
			toolDone(callID, "completed", "ran "+a.title)
			return "used " + a.title
		}
		if !a.askFirst {
			toolDone(callID, "failed", "permission denied")
		}
		return "denied via " + outcome
	}

	hasMCPServers := func(params json.RawMessage) bool {
		var probe map[string]json.RawMessage
		_ = json.Unmarshal(params, &probe)
		_, ok := probe["mcpServers"]
		return ok
	}
	// cwdIsAbsolute mirrors goose 1.51's session/new/load check: the cwd
	// param must be present and an absolute path (goose rejects a relative
	// one with -32602 "cwd must be an absolute path").
	cwdIsAbsolute := func(params json.RawMessage) bool {
		var probe struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(params, &probe) != nil || probe.Cwd == "" {
			return false
		}
		return filepath.IsAbs(probe.Cwd)
	}
	recordMCP := func(params json.RawMessage) {
		var p struct {
			MCPServers []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"mcpServers"`
		}
		_ = json.Unmarshal(params, &p)
		mcpNames = nil
		for _, srv := range p.MCPServers {
			name := srv.Name
			if srv.Type != "" {
				name += ":" + srv.Type
			}
			mcpNames = append(mcpNames, name)
		}
	}

	for m := range msgs {
		switch m.Method {
		case "initialize":
			sessCaps := map[string]any{"list": map[string]any{}}
			if pr.closeCap {
				sessCaps["close"] = map[string]any{}
			}
			respond(m.ID, map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"loadSession":         pr.loadSession,
					"mcpCapabilities":     map[string]any{"http": pr.httpMCP, "sse": false},
					"sessionCapabilities": sessCaps,
				},
				"agentInfo": map[string]any{"name": "fakeacp", "version": "0.0.1"},
			})

		case "session/new":
			if pr.requireMCP && !hasMCPServers(m.Params) {
				respondErr(m.ID, -32602, "Invalid params", map[string]any{"error": "missing field `mcpServers`"})
				continue
			}
			if pr.requireAbsCwd && !cwdIsAbsolute(m.Params) {
				respondErr(m.ID, -32602, "Invalid params", map[string]any{"error": "cwd must be an absolute path"})
				continue
			}
			recordMCP(m.Params)
			id = fmt.Sprintf("sess_%d", os.Getpid())
			if dir != "" {
				transcript = filepath.Join(dir, id+".jsonl")
			}
			respond(m.ID, sessionResultBody(true))

		case "session/load":
			if !pr.loadSession {
				respondErr(m.ID, -32601, "Method not found", nil)
				continue
			}
			if pr.requireMCP && !hasMCPServers(m.Params) {
				respondErr(m.ID, -32602, "Invalid params", map[string]any{"error": "missing field `mcpServers`"})
				continue
			}
			if pr.requireAbsCwd && !cwdIsAbsolute(m.Params) {
				respondErr(m.ID, -32602, "Invalid params", map[string]any{"error": "cwd must be an absolute path"})
				continue
			}
			recordMCP(m.Params)
			var p loadSessionParams
			_ = json.Unmarshal(m.Params, &p)
			id = p.SessionID
			if dir != "" {
				transcript = filepath.Join(dir, id+".jsonl")
				entries := faLoadTranscript(transcript)
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
			respond(m.ID, sessionResultBody(false))

		case "session/set_mode":
			if pr.modeConfigID != "" {
				// This profile's mode rides a configOption; a set_mode call
				// means the engine's transport detection failed.
				respondErr(m.ID, -32601, "Method not found", nil)
				continue
			}
			var p setModeParams
			_ = json.Unmarshal(m.Params, &p)
			mode = p.ModeID
			notifyUpdate(map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": mode})
			respond(m.ID, map[string]any{})

		case "session/set_config_option":
			var p setConfigOptionParams
			_ = json.Unmarshal(m.Params, &p)
			switch p.ConfigID {
			case pr.modeConfigID:
				mode = p.Value
			case "model":
				if p.Value == "bad-model" {
					respondErr(m.ID, -32602, "unknown model bad-model", nil)
					continue
				}
				model = p.Value
			}
			respond(m.ID, map[string]any{})

		case "session/prompt":
			var p promptParams
			_ = json.Unmarshal(m.Params, &p)
			var prompt string
			for _, b := range p.Prompt {
				prompt += b.Text
			}
			history = append(history, prompt)
			faAppendTranscript(transcript, "user", prompt)

			// Real turns open with noise the decoder must skip or map.
			notifyUpdate(map[string]any{"sessionUpdate": "session_info_update", "_meta": map[string]any{"fake": map[string]any{"activeRunId": "run_fake"}}})
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
			case strings.HasPrefix(prompt, "getenv "):
				name := strings.TrimPrefix(prompt, "getenv ")
				if v, ok := os.LookupEnv(name); ok {
					reply = "env " + name + "=" + v
				} else {
					reply = "env " + name + " unset"
				}
			case strings.HasSuffix(prompt, "mode"):
				reply = "mode: " + mode
			case strings.HasSuffix(prompt, "model"):
				reply = "model: " + model
			case strings.HasSuffix(prompt, "sysprompt"):
				reply = "sysprompt: " + sysPrompt
			case strings.HasSuffix(prompt, "mcp"):
				reply = "mcp: " + strings.Join(mcpNames, ",")
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
			case strings.HasSuffix(prompt, "busy"):
				// The agent-side concurrent-prompt surface (goose's shape); the
				// engine's own client-side guard is tested separately.
				respondErr(m.ID, -32602, "session already has active run run_fake for session "+id, nil)
				continue
			case strings.HasSuffix(prompt, "refuse"):
				stopReason = "refusal"
				reply = ""
			case strings.HasSuffix(prompt, "cancelled-stop"):
				stopReason = "cancelled"
				reply = ""
			case strings.HasSuffix(prompt, "weird"):
				// An official v1 variant many agents never emit, plus a stop
				// reason from the future — both must pass through cleanly.
				notifyUpdate(map[string]any{"sessionUpdate": "plan", "entries": []map[string]any{{"content": "step one", "priority": "high", "status": "pending"}}})
				stopReason = "some_future_stop"
				reply = "survived the plan"
			case strings.HasSuffix(prompt, "fail-tool"):
				permSeq++
				callID := fmt.Sprintf("call_%d", permSeq)
				toolCall(callID, faAttempts["attempt-shell"])
				notifyUpdate(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": "in_progress",
					"_meta": map[string]any{"toolNotification": map[string]any{"type": "live_output"}}})
				toolDone(callID, "failed", "boom: exit 1")
				reply = "the tool failed"
			case strings.HasSuffix(prompt, "slow"):
				interrupted := false
				for i := 0; i < 400 && !interrupted; i++ {
					chunk("agent_message_chunk", fmt.Sprintf("tick %d ", i))
					select {
					case mm, ok := <-msgs:
						if !ok {
							return
						}
						if mm.Method == "session/cancel" {
							interrupted = true
						}
					case <-time.After(5 * time.Millisecond):
					}
				}
				// Cancel stop-reasons genuinely diverge: goose resolves
				// end_turn (the client's own flag must carry the signal),
				// claude/hermes resolve cancelled — the profile picks.
				raw, _ := json.Marshal(promptResult{StopReason: pr.cancelStop, Usage: usage})
				faSend(rpcMessage{JSONRPC: "2.0", ID: m.ID, Result: raw})
				continue
			default:
				if a, ok := faAttempts[lastWord(prompt)]; ok {
					reply = attemptTool(a)
				}
			}

			if reply != "" {
				chunk("agent_message_chunk", reply)
				faAppendTranscript(transcript, "agent", reply)
			}
			notifyUpdate(map[string]any{"sessionUpdate": "usage_update", "used": 30, "size": 128000})
			raw, _ := json.Marshal(promptResult{StopReason: stopReason, Usage: usage})
			faSend(rpcMessage{JSONRPC: "2.0", ID: m.ID, Result: raw})

		case "session/cancel":
			// No turn in flight to interrupt outside the slow branch above.

		case "session/close":
			// The marker file makes the capability gate observable from the
			// test process: present after Close against a close-advertising
			// profile, absent when the engine correctly withheld the call.
			if dir != "" {
				_ = os.WriteFile(filepath.Join(dir, "close-called"), []byte(id), 0o644)
			}
			if !pr.closeCap {
				respondErr(m.ID, -32601, "Method not found", nil)
				continue
			}
			respond(m.ID, map[string]any{})

		default:
			if m.Method == pr.sysPromptMethod && pr.sysPromptMethod != "" && !noSysPrompt {
				var p struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(m.Params, &p)
				sysPrompt = p.Text
				respond(m.ID, map[string]any{})
				continue
			}
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

// faReplayMain serves a committed capture file verbatim: each incoming
// client request is matched against the capture's next c2s entry by
// method, then the following s2c lines are emitted with response ids
// rewritten to the live request's id (sessionIds work verbatim — the
// engine echoes what the captured session/new returned). Two documented
// tolerances keep byte-real goose output servable to today's engine:
//   - c2s/s2c pairs whose reply is the -32602 "missing field `mcpServers`"
//     error are dropped at load: they pin the requirement (the capture
//     client omitted the field once and retried), and this engine never
//     omits it.
//   - an expected session/set_mode matches an incoming
//     session/set_config_option: goose advertises its mode through BOTH
//     surfaces (the session/new result carries a category-"mode"
//     configOption, acp-transcript.jsonl line 4, so the engine's runtime
//     transport detection picks set_config_option; the capture's driver
//     used set_mode against the same session).
func faReplayMain(path string) {
	type capEntry struct {
		Dir string          `json:"dir"`
		Msg json.RawMessage `json:"msg"`
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakeacp replay: %v\n", err)
		os.Exit(1)
	}
	var entries []capEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e capEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Dir != "" {
			entries = append(entries, e)
		}
	}
	_ = f.Close()

	// Drop the missing-mcpServers request/error pairs (see doc comment).
	filtered := entries[:0]
	for i := 0; i < len(entries); i++ {
		if entries[i].Dir == "c2s" && i+1 < len(entries) &&
			entries[i+1].Dir == "s2c" && strings.Contains(string(entries[i+1].Msg), "missing field `mcpServers`") {
			i++ // skip both halves
			continue
		}
		filtered = append(filtered, entries[i])
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1<<20)
	fatal := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "fakeacp replay: "+format+"\n", args...)
		os.Exit(1)
	}
	idMap := map[string]json.RawMessage{} // capture request id → live request id
	for _, e := range filtered {
		var m rpcMessage
		_ = json.Unmarshal(e.Msg, &m)
		if e.Dir == "s2c" {
			out := e.Msg
			if m.Method == "" && len(m.ID) > 0 { // a response: rewrite to the live id
				if live, ok := idMap[string(m.ID)]; ok {
					m.ID = live
					out, _ = json.Marshal(m)
				}
			}
			fmt.Println(string(out))
			continue
		}
		// c2s: wait for the live client's next line.
		if !in.Scan() {
			fatal("client closed before sending expected %s", m.Method)
		}
		var live rpcMessage
		if err := json.Unmarshal(in.Bytes(), &live); err != nil {
			fatal("undecodable client line: %v", err)
		}
		if m.Method == "" {
			continue // capture expected a client response (permission reply): accept it
		}
		modeEquivalent := m.Method == "session/set_mode" && live.Method == "session/set_config_option"
		if live.Method != m.Method && !modeEquivalent {
			fatal("expected %s, client sent %s", m.Method, live.Method)
		}
		if len(m.ID) > 0 {
			idMap[string(m.ID)] = live.ID
		}
	}
}

// testEngine wires the engine to the stand-in binary. dir is the fake's
// cross-process state directory (transcripts, the close marker).
func testEngine(t *testing.T, cfg Config, profile, dir string, extraEnv ...string) *Engine {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Bin = exe
	cfg.SpawnArgs = []string{"acp"}
	cfg.Env = append(append(cfg.Env,
		"MADS_FAKE_ACP=1",
		"MADS_FAKE_ACP_PROFILE="+profile,
		"MADS_FAKE_ACP_DIR="+dir,
	), extraEnv...)
	return New(cfg)
}
