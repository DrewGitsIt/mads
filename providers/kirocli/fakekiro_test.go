package kirocli

// The scripted stand-in binary: when MADS_FAKE_KIRO=1, the test binary
// re-execs as a mock kiro-cli speaking the real acp JSON-RPC wire protocol
// (the standard os/exec testing trick). Every adapter path — process spawn,
// request/response correlation, session/load replay, resume, interrupt —
// gets real subprocess coverage in CI with no kiro-cli installed.
//
// The reply dialect (echo/recall/crash/slow) mirrors provider-fake's and
// claude-code's scripted stand-in exactly, per HANDOFF §6's shared-contract
// instruction — no extra tool-call special case is added here since it is
// not part of that contract.

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
	if os.Getenv("MADS_FAKE_KIRO") == "1" {
		fakeKiroMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
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
// prompt, echo, prompt.
func fkTerminal() {
	fmt.Print("> ")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		reply, crash := fkReply(strings.TrimSpace(sc.Text()), nil)
		if crash {
			os.Exit(1)
		}
		fmt.Printf("%s\r\n> ", reply)
	}
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

	respond := func(reqID *int, result any) {
		raw, _ := json.Marshal(result)
		fkSend(rpcMessage{JSONRPC: "2.0", ID: reqID, Result: raw})
	}
	notify := func(method string, params any) {
		raw, _ := json.Marshal(params)
		fkSend(rpcMessage{JSONRPC: "2.0", Method: method, Params: raw})
	}
	update := func(kind, text string) {
		raw, _ := json.Marshal(sessionUpdate{SessionUpdate: kind, Content: &updateContent{Type: "text", Text: text}})
		notify("session/update", sessionUpdateParams{SessionID: id, Update: raw})
	}

	for m := range msgs {
		switch m.Method {
		case "initialize":
			respond(m.ID, map[string]any{"protocolVersion": 1})

		case "session/new":
			id = fmt.Sprintf("fk-%d", os.Getpid())
			if dir != "" {
				transcript = filepath.Join(dir, id+".jsonl")
			}
			respond(m.ID, newSessionResult{SessionID: id})

		case "session/load":
			var p loadSessionParams
			_ = json.Unmarshal(m.Params, &p)
			id = p.SessionID
			if dir != "" {
				transcript = filepath.Join(dir, id+".jsonl")
				for _, e := range fkLoadTranscript(transcript) {
					switch e.Role {
					case "user":
						history = append(history, e.Text)
						update("user_message_chunk", e.Text)
					case "agent":
						update("agent_message_chunk", e.Text)
					}
				}
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
			fkAppendTranscript(transcript, "user", prompt)

			if prompt == "slow" {
				interrupted := false
				for i := 0; i < 400 && !interrupted; i++ {
					update("agent_message_chunk", fmt.Sprintf("tick %d ", i))
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
				respond(m.ID, promptResult{StopReason: stopReason})
				continue
			}

			reply, crash := fkReply(prompt, history[:len(history)-1])
			if crash {
				update("agent_message_chunk", "uh o")
				os.Exit(1) // dies mid-turn, no response
			}
			update("agent_message_chunk", reply)
			fkAppendTranscript(transcript, "agent", reply)
			respond(m.ID, promptResult{StopReason: "end_turn"})

		case "session/cancel":
			// No turn in flight to interrupt outside the slow branch above.
		}
	}
}

func fkSend(m rpcMessage) {
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
