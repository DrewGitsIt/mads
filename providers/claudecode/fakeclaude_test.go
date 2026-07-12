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
	print        bool
	prompt       string
	outputFormat string
	inputFormat  string
	resume       string
	model        string
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
		case "--model":
			i++
			a.model = argv[i]
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

func fakeClaudeMain() {
	a := parseFCArgs(os.Args[1:])
	switch {
	case a.print && a.outputFormat == "json":
		fcOneShot(a)
	case a.print && a.inputFormat == "stream-json":
		fcStream(a)
	default:
		fcTerminal()
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

func fcOneShot(a fcArgs) {
	if a.prompt == "ratelimit" {
		fmt.Fprintln(os.Stderr, "Rate limit reached. Try again at 5pm.")
		os.Exit(1)
	}
	if _, crash := fcReply(a.prompt, nil); crash {
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(1)
	}
	reply, _ := fcReply(a.prompt, nil)
	out := map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": reply, "session_id": "fc-oneshot",
		"usage":      map[string]any{"input_tokens": 7, "output_tokens": 11},
		"modelUsage": map[string]any{"fake-sonnet": map[string]any{}},
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
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

// fcStream mimics the persistent stream-json session: init line, then one
// turn per user message, closed by a result line. A control_request
// interrupt lands mid-"slow"-turn.
func fcStream(a fcArgs) {
	dir := os.Getenv("MADS_FAKE_CLAUDE_DIR")
	id := a.resume
	if id == "" {
		id = fmt.Sprintf("fc-%d", os.Getpid())
	}
	transcript := ""
	var history []string
	if dir != "" {
		transcript = filepath.Join(dir, "proj", id+".jsonl")
		history = fcLoadHistory(transcript)
	}

	fcEmit(map[string]any{
		"type": "system", "subtype": "init", "session_id": id, "model": "fake-sonnet",
	})

	// stdin lines feed a channel so interrupts can land mid-turn.
	lines := make(chan wireLine)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var l wireLine
			if json.Unmarshal(sc.Bytes(), &l) == nil {
				lines <- l
			}
		}
	}()

	result := func(text string, interrupted bool) {
		fcEmit(map[string]any{
			"type": "result", "subtype": "success", "is_error": false,
			"result": text, "session_id": id,
			"usage": map[string]any{"input_tokens": 7, "output_tokens": len(text)/4 + 1},
		})
		_ = interrupted // claude reports interrupted turns as plain results too
	}

	for l := range lines {
		if l.Type != "user" {
			continue
		}
		var prompt string
		for _, b := range l.Message.blocks() {
			prompt += b.Text
		}
		history = append(history, prompt)
		fcAppendTranscript(transcript, "user", prompt, "")

		if prompt == "slow" {
			interrupted := false
			for i := 0; i < 400 && !interrupted; i++ {
				fcEmit(fcAssistantText(id, fmt.Sprintf("tick %d ", i)))
				select {
				case l, ok := <-lines:
					if ok && l.Type == "control_request" {
						interrupted = true
					}
				case <-time.After(5 * time.Millisecond):
				}
			}
			result("", interrupted)
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
			fcEmit(fcAssistantText(id, "uh o")) // dies mid-turn, no result line
			os.Exit(1)
		}
		fcEmit(fcAssistantText(id, reply))
		fcAppendTranscript(transcript, "assistant", reply, "")
		result(reply, false)
	}
}

// fcTerminal mimics the interactive UI for PTY probes: prompt, echo, prompt.
func fcTerminal() {
	fmt.Print("> ")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		reply, crash := fcReply(strings.TrimSpace(sc.Text()), nil)
		if crash {
			os.Exit(1)
		}
		fmt.Printf("%s\r\n> ", reply)
	}
}

// fcAppendTranscript writes claude's on-disk transcript format — string
// content for user lines, block content for assistant lines.
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
	var line map[string]any
	switch {
	case role == "user":
		line = map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}
	case toolPath != "":
		line = map[string]any{"type": "assistant", "message": map[string]any{
			"role": "assistant",
			"content": []map[string]any{{
				"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": toolPath},
			}},
		}}
	default:
		line = map[string]any{"type": "assistant", "message": map[string]any{
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
	})
}
