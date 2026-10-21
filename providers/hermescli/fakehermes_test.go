package hermescli

// The scripted stand-in hermes-acp: when MADS_FAKE_HERMES=1 the test binary
// re-execs as a mock agent (the os/exec testing trick; goosecli's
// fakegoose_test.go is the template). Its job is HERMES SHAPE FIDELITY,
// every shape from the 2026-10-20 survey of hermes 0.22.2
// (testdata/captures/):
//
//   - the initialize result (runc1 line 2): loadSession, image prompts,
//     sessionCapabilities {fork, list, resume} — no close, no delete, no
//     mcpCapabilities;
//   - session/new with ACP's models selector of "provider:model" ids and
//     the three hermes modes, no configOptions (runc1 line 4);
//   - kind-based tool calls titled "<tool>: <preview>" under live tc-… ids,
//     stored under call_… ids (runc1 lines 21-22, rows 6-7);
//   - the "\n\n" hermes prepends to the first text after a tool;
//   - a permission ask for an edit under a synthetic id (edit-approval-1),
//     followed by a tool_call_update for that id (run9 lines 13-15);
//   - usage that is cumulative per process (runc1 lines 18/40/87, run2);
//   - session/cancel resolving stopReason "cancelled", the partial reply
//     stored with display_metadata {"interrupted": true}, and the next
//     prompt stored as "<cancelled>\n\nUser correction/guidance after
//     interrupt: <new>" (server.py:201; runc1 rows 12-13);
//   - user rows stored whitespace-stripped;
//   - session/load of an unknown id answered with an empty success and a
//     later prompt with stopReason "refusal" (probe-loadunknown);
//   - session/load of a known id replaying history and then rewriting the
//     session's active rows with fresh row ids, same message_uid
//     (session.py:376-398, survey run3);
//   - no sessions row until the first prompt (probe-emptynew).
//
// The store is a real SQLite state.db in $HERMES_HOME built from
// testdata/state-schema.sql (hermes's own CREATE TABLE text), so the
// TranscriptReader and the resume guard are exercised against the real
// columns. The argv assertion (hermes-acp takes no arguments) proves the
// binding adds none.

import (
	"bufio"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

//go:embed testdata/state-schema.sql
var stateSchema string

func TestMain(m *testing.M) {
	if os.Getenv("MADS_FAKE_HERMES") == "1" {
		fhMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fhFail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fakehermes: "+format+"\n", args...)
	os.Exit(1)
}

// openTestStore opens (creating from the real schema if needed) a state.db
// for writing — shared by the stand-in and the fixture tests.
func openTestStore(path string) (*sql.DB, error) {
	dsn := "file:" + url.PathEscape(path) + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'messages'").Scan(&n); err != nil {
		_ = db.Close()
		return nil, err
	}
	if n == 0 {
		if _, err := db.Exec(stateSchema); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

type fhMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func fhSend(m fhMessage) {
	m.JSONRPC = "2.0"
	raw, _ := json.Marshal(m)
	fmt.Println(string(raw))
}

// fhModels is a trimmed models selector in the captured shape (runc1 line
// 4): picker ids are "provider:model", names "<Provider> · <model>".
var fhModels = []map[string]string{
	{"modelId": "copilot:gpt-4.1", "name": "GitHub Copilot · gpt-4.1", "description": "Provider: GitHub Copilot"},
	{"modelId": "copilot:gpt-4o", "name": "GitHub Copilot · gpt-4o", "description": "Provider: GitHub Copilot"},
	{"modelId": "custom:qwen3.8-coder-nothink", "name": "Custom · qwen3.8-coder-nothink", "description": "Provider: Custom"},
}

var fhModes = []map[string]string{
	{"id": "default", "name": "Default", "description": "Ask before edits."},
	{"id": "accept_edits", "name": "Accept Edits", "description": "Auto-allow workspace and temp-dir edits; still asks for sensitive paths."},
	{"id": "dont_ask", "name": "Don't Ask", "description": "Auto-allow file edits for this session except sensitive paths."},
}

func fhMain() {
	if len(os.Args) != 1 {
		fhFail("argv = %v, want no arguments (hermes-acp takes none)", os.Args[1:])
	}
	home := os.Getenv("HERMES_HOME")
	if home == "" {
		fhFail("HERMES_HOME unset — the binding must pin the home in tests")
	}
	db, err := openTestStore(filepath.Join(home, "state.db"))
	if err != nil {
		fhFail("store: %v", err)
	}
	defer func() { _ = db.Close() }()
	if prefix := os.Getenv("MADS_FAKE_HERMES_REPLAY"); prefix != "" {
		fhReplay(db, prefix)
		return
	}
	(&fakeHermes{db: db, model: "copilot:gpt-4.1", mode: "default"}).serve()
}

// fhReplay serves a survey capture pair — <prefix>.wire.jsonl (the client's
// stdio log: "out" lines it sent, "in" lines hermes sent) and
// <prefix>.db.jsonl (the state.db inserts a 25 ms poll saw) — merged by
// timestamp: hermes's lines go out verbatim (response ids rewritten to the
// live request's), and each captured row is inserted into the store when
// its turn in the timeline comes, so rows land by position exactly as
// hermes wrote them. Each captured client line waits for the live client's
// next line of the same method; a live request the capture never made (the
// engine's session/set_mode) is answered with an empty result.
func fhReplay(db *sql.DB, prefix string) {
	type entry struct {
		T   int64           `json:"t"`
		Dir string          `json:"dir"`
		Msg json.RawMessage `json:"msg"`
		Ev  string          `json:"ev"`
		Row map[string]any  `json:"row"`
	}
	var entries []entry
	for _, path := range []string{prefix + ".wire.jsonl", prefix + ".db.jsonl"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			fhFail("replay: %v", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var e entry
			if json.Unmarshal([]byte(line), &e) == nil && (e.Dir == "in" || e.Dir == "out" || e.Ev == "insert") {
				entries = append(entries, e)
			}
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].T < entries[j].T })

	var sid string // the captured session id, which every update carries
	for _, e := range entries {
		var m fhMessage
		if e.Dir == "in" && json.Unmarshal(e.Msg, &m) == nil && m.Method == "session/update" {
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			sid = p.SessionID
			break
		}
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1<<20)
	ids := map[string]json.RawMessage{}
	sessionRow := false
	for _, e := range entries {
		switch {
		case e.Ev == "insert":
			str := func(k string) any {
				if v, ok := e.Row[k].(string); ok {
					return v
				}
				return nil
			}
			if !sessionRow {
				if _, err := db.Exec(`INSERT INTO sessions (id, source, started_at) VALUES (?, 'acp', ?)`, sid, e.Row["ts"]); err != nil {
					fhFail("replay session: %v", err)
				}
				sessionRow = true
			}
			if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, tool_name,
				display_kind, timestamp, message_uid, active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
				sid, e.Row["role"], str("content"), str("tool_calls"), str("tool_call_id"), str("tool_name"),
				str("display_kind"), e.Row["ts"], e.Row["message_uid"]); err != nil {
				fhFail("replay insert: %v", err)
			}
		case e.Dir == "in":
			var m fhMessage
			_ = json.Unmarshal(e.Msg, &m)
			out := e.Msg
			if m.Method == "" && len(m.ID) > 0 {
				if live, ok := ids[string(m.ID)]; ok {
					m.ID = live
					out, _ = json.Marshal(m)
				}
			}
			fmt.Println(string(out))
		case e.Dir == "out":
			var want fhMessage
			_ = json.Unmarshal(e.Msg, &want)
			for {
				if !in.Scan() {
					fhFail("replay: client closed before sending %s", want.Method)
				}
				var live fhMessage
				if err := json.Unmarshal(in.Bytes(), &live); err != nil {
					fhFail("replay: undecodable client line: %v", err)
				}
				if live.Method == want.Method {
					if len(want.ID) > 0 {
						ids[string(want.ID)] = live.ID
					}
					break
				}
				if len(live.ID) > 0 {
					fhSend(fhMessage{ID: live.ID, Result: json.RawMessage(`{}`)})
				}
			}
		}
	}
	for in.Scan() { // after the capture: answer anything, until the client goes
		var live fhMessage
		if json.Unmarshal(in.Bytes(), &live) == nil && len(live.ID) > 0 && live.Method != "" {
			fhSend(fhMessage{ID: live.ID, Result: json.RawMessage(`{}`)})
		}
	}
}

type fakeHermes struct {
	db            *sql.DB
	id, cwd, mode string
	model         string
	known         bool // the session exists (new, or loaded from the store)
	in, out       int  // cumulative usage, per process
	interrupted   string
	msgs          chan fhMessage
}

func (f *fakeHermes) respond(id json.RawMessage, result any) {
	raw, _ := json.Marshal(result)
	fhSend(fhMessage{ID: id, Result: raw})
}

func (f *fakeHermes) respondErr(id json.RawMessage, code int, msg string) {
	m := fhMessage{ID: id}
	m.Error = &struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{code, msg}
	fhSend(m)
}

func (f *fakeHermes) update(fields map[string]any) {
	params, _ := json.Marshal(map[string]any{"sessionId": f.id, "update": fields})
	fhSend(fhMessage{Method: "session/update", Params: params})
}

func (f *fakeHermes) chunk(text, messageID string) {
	u := map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}
	if messageID != "" {
		u["messageId"] = messageID
	}
	f.update(u)
}

func (f *fakeHermes) ensureSessionRow() {
	_, err := f.db.Exec(`INSERT OR IGNORE INTO sessions (id, source, started_at, cwd, model) VALUES (?, 'acp', ?, ?, ?)`,
		f.id, nowFloat(), f.cwd, f.model)
	if err != nil {
		fhFail("insert session: %v", err)
	}
}

func (f *fakeHermes) addRow(role, content, toolCalls, toolCallID, toolName, displayMeta string) {
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	_, err := f.db.Exec(`INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, tool_name,
		display_metadata, timestamp, message_uid, active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		f.id, role, content, null(toolCalls), null(toolCallID), null(toolName), null(displayMeta), nowFloat(), randHex(16))
	if err != nil {
		fhFail("insert message: %v", err)
	}
}

func (f *fakeHermes) sessionResult() map[string]any {
	return map[string]any{
		"models": map[string]any{"availableModels": fhModels, "currentModelId": f.model},
		"modes":  map[string]any{"availableModes": fhModes, "currentModeId": f.mode},
		"_meta": map[string]any{"hermes": map[string]any{"sessionProvenance": map[string]any{
			"acpSessionId": f.id, "currentHermesSessionId": f.id, "rootHermesSessionId": f.id, "sessionKind": "root",
		}}},
	}
}

func (f *fakeHermes) serve() {
	f.msgs = make(chan fhMessage)
	go func() {
		defer close(f.msgs)
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var m fhMessage
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				f.msgs <- m
			}
		}
	}()
	for m := range f.msgs {
		switch m.Method {
		case "initialize":
			f.respond(m.ID, map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"loadSession":         true,
					"promptCapabilities":  map[string]any{"image": true},
					"sessionCapabilities": map[string]any{"fork": map[string]any{}, "list": map[string]any{}, "resume": map[string]any{}},
				},
				"agentInfo": map[string]any{"name": "hermes-agent", "version": "0.22.2"},
				"authMethods": []map[string]any{{"id": "copilot", "name": "copilot runtime credentials",
					"description": "Authenticate Hermes using the currently configured copilot runtime credentials."}},
			})
		case "session/new":
			var p struct {
				Cwd string `json:"cwd"`
			}
			_ = json.Unmarshal(m.Params, &p)
			f.id, f.cwd, f.known = fmt.Sprintf("%s-0000-4000-8000-%s", randHex(4), randHex(6)), p.Cwd, true
			res := f.sessionResult()
			res["sessionId"] = f.id
			f.respond(m.ID, res)
		case "session/load":
			f.load(m)
		case "session/set_mode":
			var p struct {
				ModeID string `json:"modeId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			f.mode = "default" // hermes normalizes an unknown mode to default (server.py:1062-1064)
			for _, mo := range fhModes {
				if mo["id"] == p.ModeID {
					f.mode = p.ModeID
				}
			}
			f.respond(m.ID, map[string]any{})
		case "session/set_model":
			var p struct {
				ModelID string `json:"modelId"`
			}
			_ = json.Unmarshal(m.Params, &p)
			ok := false
			for _, mo := range fhModels {
				ok = ok || mo["modelId"] == p.ModelID
			}
			if !ok {
				f.respondErr(m.ID, -32602, "Invalid params: Cannot switch to "+p.ModelID)
				continue
			}
			f.model = p.ModelID
			_, _ = f.db.Exec("UPDATE sessions SET model = ? WHERE id = ?", f.model, f.id)
			f.respond(m.ID, map[string]any{})
		case "session/prompt":
			f.prompt(m)
		case "session/cancel":
			// nothing in flight outside prompt()'s own loop
		default:
			if len(m.ID) > 0 {
				f.respondErr(m.ID, -32601, "Method not found: "+m.Method)
			}
		}
	}
}

// load replays a stored session — user and agent text as chunks without
// messageIds, tool calls as tool_call + completed update (server.py
// _history_replay_updates) — answers, then rewrites the active rows (new
// ids, same message_uid). An unknown id gets hermes's empty success.
func (f *fakeHermes) load(m fhMessage) {
	var p struct {
		SessionID  string           `json:"sessionId"`
		Cwd        string           `json:"cwd"`
		MCPServers *json.RawMessage `json:"mcpServers"`
	}
	_ = json.Unmarshal(m.Params, &p)
	f.id, f.cwd = p.SessionID, p.Cwd
	var model string
	if err := f.db.QueryRow("SELECT COALESCE(model, '') FROM sessions WHERE id = ?", f.id).Scan(&model); err != nil {
		f.known = false
		f.respond(m.ID, map[string]any{})
		return
	}
	f.known = true
	if model != "" {
		f.model = model
	}
	type row struct {
		role, content, toolCalls, toolCallID, toolName, meta, uid string
		ts                                                        float64
	}
	rs, err := f.db.Query(`SELECT role, COALESCE(content,''), COALESCE(tool_calls,''), COALESCE(tool_call_id,''),
		COALESCE(tool_name,''), COALESCE(display_metadata,''), message_uid, timestamp
		FROM messages WHERE session_id = ? AND active = 1 ORDER BY id`, f.id)
	if err != nil {
		fhFail("load: %v", err)
	}
	var rows []row
	for rs.Next() {
		var r row
		_ = rs.Scan(&r.role, &r.content, &r.toolCalls, &r.toolCallID, &r.toolName, &r.meta, &r.uid, &r.ts)
		rows = append(rows, r)
	}
	_ = rs.Close()
	for _, r := range rows {
		switch r.role {
		case "user":
			f.update(map[string]any{"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": r.content}})
		case "assistant":
			if r.content != "" {
				f.chunk(r.content, "")
			}
		case "tool":
			f.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": r.toolCallID, "kind": "execute", "title": r.toolName})
			f.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": r.toolCallID, "status": "completed",
				"content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": r.content}}}})
		}
	}
	f.respond(m.ID, f.sessionResult())
	// The rewrite after the response (hermes's load persists the restored
	// history): delete the active rows, reinsert them with fresh ids.
	_, _ = f.db.Exec("DELETE FROM messages WHERE session_id = ? AND active = 1", f.id)
	for _, r := range rows {
		null := func(s string) any {
			if s == "" {
				return nil
			}
			return s
		}
		_, _ = f.db.Exec(`INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, tool_name,
			display_metadata, timestamp, message_uid, active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			f.id, r.role, r.content, null(r.toolCalls), null(r.toolCallID), null(r.toolName), null(r.meta), r.ts, r.uid)
	}
}

func (f *fakeHermes) result(id json.RawMessage, stop string) {
	f.update(map[string]any{"sessionUpdate": "usage_update", "size": 128000, "used": f.in / 10})
	f.respond(id, map[string]any{"stopReason": stop, "usage": map[string]any{
		"inputTokens": f.in, "outputTokens": f.out, "totalTokens": f.in + f.out, "cachedReadTokens": 0, "thoughtTokens": 0,
	}})
}

func (f *fakeHermes) prompt(m fhMessage) {
	var p struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Text string `json:"text"`
		} `json:"prompt"`
	}
	_ = json.Unmarshal(m.Params, &p)
	if !f.known || p.SessionID != f.id {
		// What real hermes answers on a session it never loaded.
		f.respond(m.ID, map[string]any{"stopReason": "refusal"})
		return
	}
	var prompt string
	for _, b := range p.Prompt {
		prompt += b.Text
	}
	// Unsolicited noise the client must tolerate (runc1 lines 8, 10).
	f.update(map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []map[string]any{{"name": "compress", "description": "Compress conversation context"}}})
	f.update(map[string]any{"sessionUpdate": "session_info_update", "title": "fake"})

	f.ensureSessionRow()
	stored := prompt
	if f.interrupted != "" {
		stored = f.interrupted + "\n\nUser correction/guidance after interrupt: " + prompt
		f.interrupted = ""
	}
	f.addRow("user", strings.TrimSpace(stored), "", "", "", "")

	// The dialect reads the prompt's last paragraph, so a first-send system
	// prompt prefix (persona + "\n\n" + text) doesn't hide the command.
	cmd := strings.TrimSpace(prompt)
	if i := strings.LastIndex(cmd, "\n\n"); i >= 0 {
		cmd = strings.TrimSpace(cmd[i+2:])
	}
	msgID := randHex(8)
	reply := ""
	switch {
	case cmd == "slow":
		f.slow(m.ID, prompt, msgID)
		return
	case strings.HasPrefix(cmd, "echo "):
		reply = "you said " + strings.TrimPrefix(cmd, "echo ")
	case cmd == "recall":
		var users []string
		rs, _ := f.db.Query("SELECT content FROM messages WHERE session_id = ? AND role = 'user' AND active = 1 ORDER BY id", f.id)
		for rs.Next() {
			var c string
			_ = rs.Scan(&c)
			users = append(users, c)
		}
		_ = rs.Close()
		reply = "earlier you said: " + strings.Join(users, "; ")
	case cmd == "whoami":
		reply = fmt.Sprintf("model %s mode %s first %q", f.model, f.mode, prompt)
	case cmd == "tool-text":
		f.toolText(msgID)
		f.in += 3000
		f.out += 30
		f.result(m.ID, "end_turn")
		return
	case cmd == "tool":
		f.tool(msgID)
		f.in += 2000
		f.out += 15
		f.result(m.ID, "end_turn")
		return
	case cmd == "ask-edit":
		f.askEdit()
		reply = "done"
	case cmd == "detach" || cmd == "detach-slow":
		// What hermes's tools do (process_registry.py:1246 etc.): a
		// helper in a session of its own, plus one left in hermes-acp's
		// group (the PM worker's case, pm/client.py:77). Both pids go to
		// MADS_FAKE_HERMES_PIDS, announced as a tool call first.
		f.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "tc-" + randHex(6), "kind": "execute", "title": "terminal: sleep 300 &"})
		f.spawnHelpers()
		if cmd == "detach-slow" {
			f.slow(m.ID, prompt, msgID)
			return
		}
		reply = "started helpers"
	case cmd == "ratelimit":
		f.respondErr(m.ID, -32603, "Internal error: HTTP 429: rate limit exceeded")
		return
	default:
		reply = "ok"
	}
	f.chunk(reply, msgID)
	f.addRow("assistant", reply, "", "", "", "")
	f.in += 1000
	f.out += 10
	f.result(m.ID, "end_turn")
}

func (f *fakeHermes) spawnHelpers() {
	detached := exec.Command("sleep", "300")
	detached.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	grouped := exec.Command("sleep", "300")
	var pids []string
	for _, c := range []*exec.Cmd{detached, grouped} {
		if err := c.Start(); err != nil {
			fhFail("helper: %v", err)
		}
		pids = append(pids, strconv.Itoa(c.Process.Pid))
	}
	if path := os.Getenv("MADS_FAKE_HERMES_PIDS"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(pids, " ")), 0o644)
	}
}

// tool is runc1's turn 2: tool call announced under a live tc- id, stored
// under a call_ id, then the next model call's text with the injected break.
func (f *fakeHermes) tool(firstMsgID string) {
	live, stored := "tc-"+randHex(6), "call_"+randHex(8)
	f.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": live, "kind": "execute", "title": "terminal: ls",
		"locations": []any{}, "content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "$ ls"}}}})
	calls, _ := json.Marshal([]map[string]any{{"id": stored, "call_id": stored, "type": "function",
		"function": map[string]any{"name": "terminal", "arguments": `{"command":"ls"}`}}})
	f.addRow("assistant", "", string(calls), "", "", "")
	f.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": live, "kind": "execute", "status": "completed",
		"content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "terminal result\n- **output:** alpha.txt\n- **exit_code:** 0"}}}})
	f.addRow("tool", `{"output": "alpha.txt", "exit_code": 0, "error": null}`, "", stored, "terminal", "")
	_ = firstMsgID
	f.chunk("\n\nI found alpha.txt.", randHex(8))
	f.addRow("assistant", "I found alpha.txt.", "", "", "", "")
}

// toolText is the stable_keys probe's turn, written by position as hermes
// writes it (survey runc1, run2 rows 5-8): model call 1 streams a sentence
// under its messageId, then its assistant row — that text plus the
// tool_calls under the stored call_ id — lands just before the tool_call
// frame (Copilot's order, −5..−15 ms); the tool runs, its row lands after
// the completed frame; model call 2 streams under a new messageId with the
// injected "\n\n", and its row (without it) lands before the prompt
// response.
func (f *fakeHermes) toolText(firstMsgID string) {
	live, stored := "tc-"+randHex(6), "call_"+randHex(8)
	f.chunk("I'll list ", firstMsgID)
	f.chunk("the files.", firstMsgID)
	calls, _ := json.Marshal([]map[string]any{{"id": stored, "call_id": stored, "type": "function",
		"function": map[string]any{"name": "terminal", "arguments": `{"command":"ls"}`}}})
	f.addRow("assistant", "I'll list the files.", string(calls), "", "", "")
	f.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": live, "kind": "execute", "title": "terminal: ls",
		"locations": []any{}, "content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "$ ls"}}}})
	f.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": live, "kind": "execute", "status": "completed",
		"content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "terminal result\n- **output:** alpha.txt\n- **exit_code:** 0"}}}})
	time.Sleep(10 * time.Millisecond)
	f.addRow("tool", `{"output": "alpha.txt", "exit_code": 0, "error": null}`, "", stored, "terminal", "")
	second := randHex(8)
	f.chunk("\n\nOne file: ", second)
	f.chunk("alpha.txt.", second)
	f.addRow("assistant", "One file: alpha.txt.", "", "", "", "")
}

// askEdit is run9's turn 1: the edit is announced under its live id, the
// ask under a synthetic one, and both get a completed update.
func (f *fakeHermes) askEdit() {
	live := "tc-" + randHex(6)
	f.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": live, "kind": "edit", "title": "write_file: note.txt",
		"locations": []map[string]any{{"path": filepath.Join(f.cwd, "note.txt")}}})
	params, _ := json.Marshal(map[string]any{
		"sessionId": f.id,
		"options": []map[string]any{
			{"kind": "allow_once", "name": "Allow edit", "optionId": "allow_once"},
			{"kind": "reject_once", "name": "Deny", "optionId": "deny"},
		},
		"toolCall": map[string]any{"toolCallId": "edit-approval-1", "kind": "edit", "status": "pending",
			"title": "Approve edit: note.txt", "rawInput": map[string]any{"tool": "write_file", "arguments": map[string]any{"path": "note.txt", "content": "hi"}}},
	})
	fhSend(fhMessage{ID: json.RawMessage("0"), Method: "session/request_permission", Params: params})
	resp, ok := <-f.msgs
	if !ok {
		os.Exit(0)
	}
	var res struct {
		Outcome struct {
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	_ = json.Unmarshal(resp.Result, &res)
	status := "failed"
	if res.Outcome.OptionID == "allow_once" {
		status = "completed"
	}
	f.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "edit-approval-1", "status": status})
	f.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": live, "kind": "edit", "status": status,
		"content": []map[string]any{{"type": "content", "content": map[string]any{"type": "text", "text": "write_file " + status}}}})
}

// slow streams until session/cancel, then stores the partial reply marked
// interrupted and resolves "cancelled" with the usage totals unchanged
// (runc1 line 323: the cancelled call's tokens are never counted).
func (f *fakeHermes) slow(id json.RawMessage, prompt, msgID string) {
	var sent strings.Builder
	for i := 0; i < 400; i++ {
		text := fmt.Sprintf("tick %d ", i)
		f.chunk(text, msgID)
		sent.WriteString(text)
		select {
		case mm, ok := <-f.msgs:
			if !ok {
				os.Exit(0)
			}
			if mm.Method == "session/cancel" {
				f.addRow("assistant", sent.String(), "", "", "", `{"interrupted": true}`)
				f.interrupted = strings.TrimSpace(prompt)
				f.result(id, "cancelled")
				return
			}
		case <-time.After(5 * time.Millisecond):
		}
	}
	f.addRow("assistant", sent.String(), "", "", "", "")
	f.in += 1000
	f.out += 400
	f.result(id, "end_turn")
}
