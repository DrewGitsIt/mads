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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if os.Getenv("MADS_FAKE_GOOSE") == "1" {
		fgMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fgMain() {
	if len(os.Args) > 1 && os.Args[1] == "session" {
		fgSessionMain(os.Args[2:])
		return
	}
	// The argv assertion: the binding must spawn `goose acp --with-builtin
	// developer` (the captured spawn shape) — this is what proves the
	// Builtins knob reaches the child.
	if len(os.Args) < 4 || os.Args[1] != "acp" || os.Args[2] != "--with-builtin" || os.Args[3] != "developer" {
		fmt.Fprintf(os.Stderr, "fakegoose: argv = %v, want [acp --with-builtin developer]\n", os.Args[1:])
		os.Exit(1)
	}
	fgServe()
}

// fgSessionMain mimics the interactive `goose session` REPL for PTY probes,
// asserting the spawn shape (bare `session`, or exactly the resume triple).
// Knobs ride env vars since the real argv is fixed: MADS_FAKE_PTY_BANNER
// names a file whose bytes are replayed verbatim as the startup banner —
// tests point it at the committed goose 1.48.0 capture, spinner redraws,
// OSC title and all — and MADS_FAKE_PTY_EXIT=1 prints one plain no-banner
// line and exits immediately (the stream-ends-without-a-banner path). Then
// a tiny REPL on raw stdin bytes (the tty is put in raw mode so Interrupt's
// 0x03 arrives as a byte instead of a line-discipline SIGINT): a line
// ending \r → `pong:<line>`, byte 0x03 → `INTERRUPT-MARK`, EOF → exit 0.
func fgSessionMain(args []string) {
	if len(args) > 0 && (args[0] == "list" || args[0] == "export") {
		fgSessionQuery(args)
		return
	}
	resumeShape := len(args) == 3 && args[0] == "--resume" && args[1] == "--session-id"
	if len(args) != 0 && !resumeShape {
		fmt.Fprintf(os.Stderr, "fakegoose: session argv = %v, want [] or [--resume --session-id <ref>]\n", args)
		os.Exit(1)
	}
	if path := os.Getenv("MADS_FAKE_PTY_BANNER"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakegoose:", err)
			os.Exit(1)
		}
		_, _ = os.Stdout.Write(raw)
	}
	if os.Getenv("MADS_FAKE_PTY_EXIT") == "1" {
		fmt.Println("no banner here")
		return
	}
	if tio, err := unix.IoctlGetTermios(0, ioctlGetTermios); err == nil {
		tio.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
		tio.Iflag &^= unix.ICRNL
		_ = unix.IoctlSetTermios(0, ioctlSetTermios, tio)
	} else {
		fmt.Fprintln(os.Stderr, "fakegoose: raw mode:", err)
	}
	// Bytes written before the ioctl above land translated (ICRNL) and
	// echoed — tests wait for this marker before sending anything.
	fmt.Print("FAKE-REPL-READY\n")
	var line []byte
	buf := make([]byte, 256)
	for {
		n, err := os.Stdin.Read(buf)
		for _, b := range buf[:n] {
			switch b {
			case 0x03:
				fmt.Print("INTERRUPT-MARK\n")
			case '\r', '\n':
				fmt.Printf("pong:%s\n", line)
				line = nil
			default:
				line = append(line, b)
			}
		}
		if err != nil {
			return
		}
	}
}

// fgSessionQuery serves the CLI query subcommands the TranscriptReader and
// SessionLocator exec, asserting their exact argv (the design-doc command
// shapes): `session list --format json` replays the fixture file named by
// MADS_FAKE_SESSION_LIST, `session export --session-id <id> --format json`
// replays MADS_FAKE_EXPORT_DIR/<id>.json — a missing export fixture
// exits 1 with goose's "Session not found" stderr shape.
func fgSessionQuery(args []string) {
	switch {
	case len(args) == 3 && args[0] == "list" && args[1] == "--format" && args[2] == "json":
		path := os.Getenv("MADS_FAKE_SESSION_LIST")
		if path == "" {
			fmt.Fprintln(os.Stderr, "fakegoose: MADS_FAKE_SESSION_LIST unset")
			os.Exit(1)
		}
		fgReplayFile(path)
	case len(args) == 5 && args[0] == "export" && args[1] == "--session-id" && args[3] == "--format" && args[4] == "json":
		dir := os.Getenv("MADS_FAKE_EXPORT_DIR")
		if dir == "" {
			fmt.Fprintln(os.Stderr, "fakegoose: MADS_FAKE_EXPORT_DIR unset")
			os.Exit(1)
		}
		path := filepath.Join(dir, args[2]+".json")
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(os.Stderr, "Error: Session not found: %s\n", args[2])
			os.Exit(1)
		}
		fgReplayFile(path)
	default:
		fmt.Fprintf(os.Stderr, "fakegoose: session query argv = %v, want [list --format json] or [export --session-id <id> --format json]\n", args)
		os.Exit(1)
	}
}

func fgReplayFile(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakegoose:", err)
		os.Exit(1)
	}
	_, _ = os.Stdout.Write(raw)
}

// fgConvEntry / fgConvContent are the persisted conversation shape — one
// export-format `conversation[]` entry as transcript.go parses it
// (testdata/captures/oneshot-export.json convention: role, unix `created`,
// text content blocks).
type fgConvEntry struct {
	Role    string          `json:"role"`
	Created int64           `json:"created"`
	Content []fgConvContent `json:"content"`
}

type fgConvContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// fgWriteExport rewrites the session's export file — the single store that
// serves resume (session/load rebuilds history from it), recall, AND the
// transcript probe (`goose session export` replays it via fgSessionQuery).
// Callers invoke it synchronously inside the session/prompt handler before
// the response resolves, so a probe reading the file after its turn ends
// can never race the write.
func fgWriteExport(dir, id, cwd, mode string, conv []fgConvEntry) {
	if dir == "" || id == "" {
		return
	}
	raw, _ := json.Marshal(map[string]any{
		"id": id, "working_dir": cwd, "goose_mode": mode, "conversation": conv,
	})
	_ = os.WriteFile(filepath.Join(dir, id+".json"), raw, 0o644)
}

// fgModes is the captured availableModes set (acp-transcript.jsonl line 4),
// served on both session/new and session/load results.
func fgModes() []map[string]any {
	modes := []map[string]any{}
	for _, mo := range []string{"auto", "approve", "smart_approve", "chat"} {
		modes = append(modes, map[string]any{"id": mo, "name": mo})
	}
	return modes
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
	var id, cwd, mode, sysPrompt string
	model := "qwen3.8"
	mode = "auto"
	// Cross-process persistence (resume/recall/transcript): the in-memory
	// conversation is rewritten to $MADS_FAKE_EXPORT_DIR/<id>.json inside
	// each prompt handler; session/load rebuilds both from the file.
	exportDir := os.Getenv("MADS_FAKE_EXPORT_DIR")
	var conv []fgConvEntry
	var userHistory []string

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
	// then complete or fail the call. smart_approve stands in for goose's
	// reviewer (permission_inspector.rs): the read is judged read-only and
	// runs unasked, everything else is escalated to an ask.
	attemptTool := func(a fgAttempt) string {
		permSeq++
		callID := fmt.Sprintf("call_%d", permSeq)
		toolCall(callID, a)
		allowed, outcome := true, "unasked"
		reviewedSafe := mode == "smart_approve" && a.title == "text_editor view"
		if mode == "approve" || (mode == "smart_approve" && !reviewedSafe) {
			allowed, outcome = requestPermission(callID, a)
		}
		if allowed {
			toolDone(callID, "completed", "ran "+a.title)
			return "used " + a.title
		}
		toolDone(callID, "failed", "The user has declined to run this tool")
		return "denied via " + outcome
	}

	record := func(role, text string) {
		conv = append(conv, fgConvEntry{Role: role, Created: time.Now().Unix(), Content: []fgConvContent{{Type: "text", Text: text}}})
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
			var p struct {
				Cwd string `json:"cwd"`
			}
			_ = json.Unmarshal(m.Params, &p)
			cwd = p.Cwd
			// Full pid: conformance shares one export dir across processes,
			// and a %1000 truncation risks cross-process id collisions. The
			// id shape is only ever matched as \d{8}_\d+.
			id = fmt.Sprintf("20260921_%d", os.Getpid())
			respond(m.ID, map[string]any{
				"sessionId":     id,
				"modes":         map[string]any{"currentModeId": mode, "availableModes": fgModes()},
				"configOptions": configOptions(),
			})

		case "session/load":
			// 1.48.0 rejects a missing mcpServers field on load exactly as on
			// session/new (acp-load-replay.jsonl lines 3-4).
			if !hasMCPServers(m.Params) {
				respondErr(m.ID, -32602, "Invalid params", map[string]any{"error": "missing field `mcpServers`"})
				continue
			}
			var p struct {
				SessionID string `json:"sessionId"`
				Cwd       string `json:"cwd"`
			}
			_ = json.Unmarshal(m.Params, &p)
			id, cwd = p.SessionID, p.Cwd
			raw, err := os.ReadFile(filepath.Join(exportDir, id+".json"))
			if err != nil {
				respondErr(m.ID, -32603, "Session not found: "+id, nil)
				continue
			}
			var doc struct {
				GooseMode    string        `json:"goose_mode"`
				Conversation []fgConvEntry `json:"conversation"`
			}
			_ = json.Unmarshal(raw, &doc)
			if doc.GooseMode != "" {
				mode = doc.GooseMode
			}
			conv = doc.Conversation
			userHistory = nil
			// Replay coalesced per message, BEFORE the response resolves —
			// the capture's replay convention (acp-load-replay.jsonl; the
			// engine's replaying window is only open during the request).
			for _, e := range conv {
				var text strings.Builder
				for _, c := range e.Content {
					text.WriteString(c.Text)
				}
				switch e.Role {
				case "user":
					userHistory = append(userHistory, text.String())
					notifyUpdate(map[string]any{"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": text.String()}})
				case "assistant":
					chunk(text.String())
				}
			}
			// The load response carries BOTH modes (the persisted
			// currentModeId) and configOptions (acp-load-replay.jsonl; the
			// engine reads configOptions off the load result,
			// acprpc/session.go).
			respond(m.ID, map[string]any{
				"sessionId":     id,
				"modes":         map[string]any{"currentModeId": mode, "availableModes": fgModes()},
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

			// The capture's usage shape and numbers (acp-transcript.jsonl
			// line 85) ride every prompt result.
			endTurn := func() {
				raw, _ := json.Marshal(map[string]any{
					"stopReason": "end_turn",
					"usage":      map[string]any{"totalTokens": 8436, "inputTokens": 8415, "outputTokens": 21},
				})
				fgSend(fgMessage{JSONRPC: "2.0", ID: m.ID, Result: raw})
			}

			if prompt == "slow" {
				// kirocli's bounded loop shape (a mid-turn session/cancel
				// breaks it, so it never swallows a later request) — but the
				// result is end_turn EVEN WHEN CANCELLED. Capture-pinned
				// deviation from fakekiro's "cancelled": real goose resolved
				// end_turn in 3/3 cancel attempts (acp-transcript.jsonl lines
				// 86-105 + captures README "Turn 2 + cancel"); the engine's
				// own interrupted flag does the detection. This deviation is
				// the fake's whole reason to differ here.
				for i := 0; i < 400; i++ {
					chunk(fmt.Sprintf("tick %d ", i))
					stop := false
					select {
					case mm, ok := <-msgs:
						if !ok || mm.Method == "session/cancel" {
							stop = true
						}
					case <-time.After(5 * time.Millisecond):
					}
					if stop {
						break
					}
				}
				endTurn()
				continue
			}

			reply := "ok"
			switch {
			case strings.HasPrefix(prompt, "echo "):
				reply = "you said " + strings.TrimPrefix(prompt, "echo ")
			case prompt == "recall":
				// Earlier user turns must come back — accumulated per prompt
				// and rebuilt from the export on session/load, so the
				// conformance resume probe's post-resume recall works.
				reply = "earlier you said: " + strings.Join(userHistory, "; ")
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

			userHistory = append(userHistory, prompt)
			record("user", prompt)
			record("assistant", reply)
			// Synchronous, before the response resolves — the transcript
			// probe reads the file right after its turn ends.
			fgWriteExport(exportDir, id, cwd, mode, conv)

			chunk(reply)
			endTurn()

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

// conformanceProvider is testProvider plus a shared export dir — the one
// store serving the suite's cross-process resume, recall, and transcript
// probes. Deliberately not baked into testProvider: the transcript/locate
// fixture tests pass their own dirs, and duplicate env keys would be
// ambiguous.
func conformanceProvider(t *testing.T) *Provider {
	t.Helper()
	return testProvider(t, "MADS_FAKE_EXPORT_DIR="+t.TempDir())
}
