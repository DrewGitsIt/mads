package kirocli

// The scripted stand-in binary: when MADS_FAKE_KIRO=1, the test binary
// re-execs as a mock kiro-cli speaking the real acp JSON-RPC wire protocol
// (the standard os/exec testing trick). Every adapter path — process spawn,
// request/response correlation, session/load replay, resume, interrupt —
// gets real subprocess coverage in CI with no kiro-cli installed. The ACP
// client under test is the generic providers/acprpc engine driven through
// this package's knob values, so the fake carries its own minimal wire
// shapes (fkMessage plus inline maps): the adapter has no JSON-RPC
// plumbing left to borrow, which is the point of the migration.
//
// The reply dialect (echo/recall/crash/slow) mirrors provider-fake's and
// claude-code's scripted stand-in exactly, per HANDOFF §6's shared-contract
// instruction. attempt-write/attempt-command are the one addition beyond
// that shared dialect: they drive a real session/request_permission round
// trip against the engine's trust gate. The fake asks BEFORE announcing a
// tool_call — the request's own toolCall carries the ACP kind, and the
// call is announced only after approval — and its permission menu offers
// opaque optionIds ("fk-yes"/"fk-no"…, deliberately NOT "allow"/"reject"):
// a client that hardcodes ids the way this package's superseded session.go
// did fails the round trip instead of silently passing.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider/ptytest"
)

func TestMain(m *testing.M) {
	if os.Getenv("MADS_FAKE_KIRO") == "1" {
		fakeKiroMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fkMessage is the fake's own JSON-RPC 2.0 line shape. ID is raw JSON:
// client requests use int ids, while the fake's own agent→client requests
// (session/request_permission) use string ids the reply echoes verbatim —
// the id shape the committed real-agent captures pin
// (providers/acprpc/testdata/captures/).
type fkMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

// fkArgs is a loose parse of the kiro-cli flags the adapter uses.
type fkArgs struct {
	acp           bool
	chat          bool
	noInteractive bool
	listModels    bool
	model         string
	resumeID      string
	prompt        string
}

func parseFKArgs(argv []string) fkArgs {
	var a fkArgs
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "acp":
			a.acp = true
		case "chat":
			a.chat = true
		case "--no-interactive":
			a.noInteractive = true
		case "--trust-all-tools":
		case "--list-models":
			a.listModels = true
		case "--format":
			i++ // value ("json") unused: the fake only ever emits json
		case "--model":
			i++
			a.model = argv[i]
		case "--resume-id":
			i++
			a.resumeID = argv[i]
		default:
			if a.chat && a.noInteractive && a.prompt == "" && !strings.HasPrefix(argv[i], "-") {
				a.prompt = argv[i]
			}
		}
	}
	return a
}

func fakeKiroMain() {
	a := parseFKArgs(os.Args[1:])
	switch {
	case a.acp:
		fkACP()
	case a.chat && a.listModels:
		fkModels()
	case a.chat && a.noInteractive:
		fkOneShot(a)
	case a.chat:
		fkTerminal()
	}
}

// fkReply is the dialect shared with provider-fake's and claude-code's
// built-ins.
func fkReply(prompt string, history []string) (string, bool /*crash*/) {
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

// fkOneShot mimics `kiro-cli chat --no-interactive --trust-all-tools <prompt>`.
func fkOneShot(a fkArgs) {
	if a.prompt == "ratelimit" {
		fmt.Fprintln(os.Stderr, "Rate limit reached. Try again later.")
		os.Exit(1)
	}
	reply, crash := fkReply(a.prompt, nil)
	if crash {
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(1)
	}
	fmt.Println(reply)
}

// fkModels mimics `kiro-cli chat --list-models --format json`.
func fkModels() {
	models := []wireModel{
		{ModelID: "fake-kiro-large", ModelName: "Fake Kiro Large", IsDefault: true},
		{ModelID: "fake-kiro-small", ModelName: "Fake Kiro Small", IsDefault: false},
	}
	_ = json.NewEncoder(os.Stdout).Encode(models)
}

// fkTerminal mimics the interactive `kiro-cli chat` REPL for PTY probes:
// prompt, echo, prompt, its input line read as ptytest.Prompt.
func fkTerminal() {
	if err := ptytest.RawMode(0); err != nil {
		fmt.Fprintln(os.Stderr, "fakekiro:", err)
	}
	fmt.Print("> ")
	_ = ptytest.Prompt{Submit: func(text string) {
		reply, crash := fkReply(strings.TrimSpace(text), nil)
		if crash {
			os.Exit(1)
		}
		fmt.Printf("%s\r\n> ", reply)
	}}.Read(os.Stdin)
}

// fkTranscriptEntry is one persisted turn — the cross-process memory a
// scripted, otherwise-stateless fake process needs to serve session/load
// replay and post-resume recall after a later process reattaches to the
// same session id.
type fkTranscriptEntry struct {
	Role string `json:"role"` // "user" or "agent"
	Text string `json:"text"`
}

func fkAppendTranscript(path, role, text string) {
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_ = json.NewEncoder(f).Encode(fkTranscriptEntry{Role: role, Text: text})
}

func fkLoadTranscript(path string) []fkTranscriptEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var entries []fkTranscriptEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e fkTranscriptEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			entries = append(entries, e)
		}
	}
	return entries
}

// fkACP is the core JSON-RPC server loop: `kiro-cli acp`. stdin lines feed a
// channel so a session/cancel notification can be detected mid-turn (the
// same concurrency shape as claude-code's fake stream).
func fkACP() {
	dir := os.Getenv("MADS_FAKE_KIRO_DIR")
	var id, transcript string
	var history []string

	msgs := make(chan fkMessage)
	go func() {
		defer close(msgs)
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var m fkMessage
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				msgs <- m
			}
		}
	}()

	respond := func(reqID json.RawMessage, result any) {
		raw, _ := json.Marshal(result)
		fkSend(fkMessage{JSONRPC: "2.0", ID: reqID, Result: raw})
	}
	notifyUpdate := func(update map[string]any) {
		raw, _ := json.Marshal(map[string]any{"sessionId": id, "update": update})
		fkSend(fkMessage{JSONRPC: "2.0", Method: "session/update", Params: raw})
	}
	chunk := func(variant, text string) {
		notifyUpdate(map[string]any{"sessionUpdate": variant, "content": map[string]any{"type": "text", "text": text}})
	}
	toolCall := func(callID, title, kind string) {
		notifyUpdate(map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": title, "kind": kind})
	}
	permSeq := 0
	// requestPermission sends a real session/request_permission request and
	// blocks for the client's reply — exercising the engine's production
	// trust gate, not just wire shapes. Safe to block on the next msgs
	// receive: the client's read loop answers synchronously from within
	// handleLine before reading its next line, so nothing else can arrive
	// first. The request's toolCall carries the kind (the fake asks before
	// announcing, so there is no prior tool_call to correlate with), and
	// only the fake's own opaque allow ids count as approval.
	requestPermission := func(callID, title, kind string) bool {
		permSeq++
		reqID := json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("perm-%d", permSeq)))
		params, _ := json.Marshal(map[string]any{
			"sessionId": id,
			"toolCall":  map[string]any{"toolCallId": callID, "status": "pending", "title": title, "kind": kind},
			"options": []map[string]any{
				{"optionId": "fk-yes", "name": "Allow", "kind": "allow_once"},
				{"optionId": "fk-yes-4ever", "name": "Always allow", "kind": "allow_always"},
				{"optionId": "fk-no", "name": "Reject", "kind": "reject_once"},
			},
		})
		fkSend(fkMessage{JSONRPC: "2.0", ID: reqID, Method: "session/request_permission", Params: params})
		resp, ok := <-msgs
		if !ok {
			return false
		}
		var pr struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}
		_ = json.Unmarshal(resp.Result, &pr)
		return pr.Outcome.Outcome == "selected" &&
			(pr.Outcome.OptionID == "fk-yes" || pr.Outcome.OptionID == "fk-yes-4ever")
	}

	for m := range msgs {
		switch m.Method {
		case "initialize":
			respond(m.ID, map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					// kiro's real agent advertises loadSession: true and an
					// empty sessionCapabilities (README.md Transcript and
					// Branching rows) — the engine gates session/load on the
					// former, so the fake must advertise what it serves.
					"loadSession":         true,
					"mcpCapabilities":     map[string]any{"http": false, "sse": false},
					"sessionCapabilities": map[string]any{},
				},
			})

		case "session/new":
			id = fmt.Sprintf("fk-%d", os.Getpid())
			if dir != "" {
				transcript = filepath.Join(dir, id+".jsonl")
			}
			respond(m.ID, map[string]any{"sessionId": id})

		case "session/load":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			id = p.SessionID
			if dir != "" {
				transcript = filepath.Join(dir, id+".jsonl")
				for _, e := range fkLoadTranscript(transcript) {
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
			history = append(history, prompt)
			fkAppendTranscript(transcript, "user", prompt)

			if prompt == "attempt-write" || prompt == "attempt-command" {
				title, kind := "Write", "edit"
				if prompt == "attempt-command" {
					title, kind = "Bash", "execute"
				}
				permSeq++
				callID := fmt.Sprintf("call_%d", permSeq)
				var reply string
				if requestPermission(callID, title, kind) {
					toolCall(callID, title, kind)
					reply = "used " + title
				} else {
					reply = fmt.Sprintf("cannot use %s: permission denied", title)
				}
				chunk("agent_message_chunk", reply)
				fkAppendTranscript(transcript, "agent", reply)
				respond(m.ID, map[string]any{"stopReason": "end_turn"})
				continue
			}

			if prompt == "slow" {
				interrupted := false
				for i := 0; i < 400 && !interrupted; i++ {
					chunk("agent_message_chunk", fmt.Sprintf("tick %d ", i))
					select {
					case mm, ok := <-msgs:
						if ok && mm.Method == "session/cancel" {
							interrupted = true
						}
					case <-time.After(5 * time.Millisecond):
					}
				}
				stopReason := "end_turn"
				if interrupted {
					stopReason = "cancelled"
				}
				respond(m.ID, map[string]any{"stopReason": stopReason})
				continue
			}

			reply, crash := fkReply(prompt, history[:len(history)-1])
			if crash {
				chunk("agent_message_chunk", "uh o")
				os.Exit(1) // dies mid-turn, no response
			}
			chunk("agent_message_chunk", reply)
			fkAppendTranscript(transcript, "agent", reply)
			respond(m.ID, map[string]any{"stopReason": "end_turn"})

		case "session/cancel":
			// No turn in flight to interrupt outside the slow branch above.
		}
	}
}

func fkSend(m fkMessage) {
	raw, _ := json.Marshal(m)
	fmt.Println(string(raw))
}

// testProvider wires the adapter to the stand-in binary with an isolated
// cross-process session directory.
func testProvider(t *testing.T) *Provider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return New(Config{
		Bin: exe,
		Env: []string{"MADS_FAKE_KIRO=1", "MADS_FAKE_KIRO_DIR=" + dir},
	})
}
