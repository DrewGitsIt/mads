package codexcli

// The scripted stand-in `codex app-server`: when MADS_FAKE_CODEX=1 the test
// binary re-execs as a fake app-server (the os/exec testing trick, as
// providers/goosecli's fakegoose). Its job is codex SHAPE FIDELITY: the
// 0.154.0 wire as captured under testdata/captures — no "jsonrpc" member,
// thread/start answering with the thread synchronously, turn/start
// answered before turn/started, userMessage items echoing clientId,
// agentMessage deltas then item/completed, approvals as server requests
// (item/started first, then item/*/requestApproval with the item's id),
// "declined" items on a refusal, turn/steer joining the running turn,
// turn/interrupt ending it "interrupted" with the partial message never
// completed, thread/compact/start as a turn of its own, and
// thread/tokenUsage/updated with per-process totals.
//
// It enforces what mads must never get wrong:
//   - argv is exactly `app-server`;
//   - a writable sandbox must come with the projects.<cwd> trust override
//     (threadConfig) — real codex would otherwise write the user's
//     ~/.codex/config.toml, so the fake refuses the thread instead;
//   - a gated tool only runs when the client answered "accept"; the sandbox
//     decides what a tool may do (read-only fails a write) — denial is
//     observable behavior, not inference.
//
// Knobs (env): MADS_FAKE_CODEX_STORE (required) — the thread store, one
// JSON file per thread, shared across processes so resume, fork and
// transcript work across them; MADS_FAKE_CODEX_PIDFILE — where the
// "sleeper" script writes the pid of the command it spawned (in a process
// group of its own, as real codex does); MADS_FAKE_CODEX_LOG — every line
// received is appended there, for argv-free assertions on what was sent.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("MADS_FAKE_CODEX") == "1" {
		fcMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fcThread struct {
	ID       string      `json:"id"`
	Cwd      string      `json:"cwd"`
	Sandbox  string      `json:"sandbox"`
	Approval string      `json:"approval"`
	Items    []itemEntry `json:"items"`

	turn   *fcTurn
	totals tokenBreakdown
}

type fcTurn struct {
	id          string
	interrupted chan struct{}
	once        sync.Once
	steer       chan string
}

func (t *fcTurn) interrupt() { t.once.Do(func() { close(t.interrupted) }) }

type fcServer struct {
	store string
	wmu   sync.Mutex
	out   *os.File
	log   *os.File

	mu      sync.Mutex
	threads map[string]*fcThread
	pending map[int]chan rpcMessage
	nextReq int
	seq     int
}

func fcMain() {
	if len(os.Args) != 2 || os.Args[1] != "app-server" {
		fmt.Fprintf(os.Stderr, "fakecodex: argv = %v, want [app-server]\n", os.Args[1:])
		os.Exit(1)
	}
	srv := &fcServer{
		store:   os.Getenv("MADS_FAKE_CODEX_STORE"),
		out:     os.Stdout,
		threads: map[string]*fcThread{},
		pending: map[int]chan rpcMessage{},
	}
	if srv.store == "" {
		fmt.Fprintln(os.Stderr, "fakecodex: MADS_FAKE_CODEX_STORE unset")
		os.Exit(1)
	}
	if path := os.Getenv("MADS_FAKE_CODEX_LOG"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			srv.log = f
		}
	}
	r := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if srv.log != nil {
				_, _ = srv.log.Write(line)
			}
			srv.handle(line)
		}
		if err != nil {
			return
		}
	}
}

func (srv *fcServer) send(v any) {
	b, _ := json.Marshal(v)
	srv.wmu.Lock()
	defer srv.wmu.Unlock()
	_, _ = srv.out.Write(append(b, '\n'))
}

func (srv *fcServer) result(id json.RawMessage, v any) {
	srv.send(map[string]any{"id": id, "result": v})
}

func (srv *fcServer) fail(id json.RawMessage, code int, msg string) {
	srv.send(map[string]any{"error": map[string]any{"code": code, "message": msg}, "id": id})
}

func (srv *fcServer) notify(method string, params any) {
	srv.send(map[string]any{"method": method, "params": params, "emittedAtMs": time.Now().UnixMilli()})
}

// request sends a server→client request and waits for its answer (nil
// when the turn was interrupted first).
func (srv *fcServer) request(method string, params any, turn *fcTurn) json.RawMessage {
	srv.mu.Lock()
	id := srv.nextReq
	srv.nextReq++
	ch := make(chan rpcMessage, 1)
	srv.pending[id] = ch
	srv.mu.Unlock()
	srv.send(map[string]any{"method": method, "id": id, "params": params})
	select {
	case m := <-ch:
		srv.notify("serverRequest/resolved", map[string]any{"threadId": "", "requestId": id})
		return m.Result
	case <-turn.interrupted:
		srv.notify("serverRequest/resolved", map[string]any{"threadId": "", "requestId": id})
		return nil
	}
}

func (srv *fcServer) newID() string {
	srv.mu.Lock()
	srv.seq++
	n := srv.seq
	srv.mu.Unlock()
	return fmt.Sprintf("01a1fa4e-%04x-7000-8000-%012x", os.Getpid()&0xffff, time.Now().UnixNano()&0xffffffffffff+int64(n))
}

func (srv *fcServer) path(id string) string { return filepath.Join(srv.store, id+".json") }

func (srv *fcServer) save(th *fcThread) {
	b, _ := json.Marshal(th)
	_ = os.WriteFile(srv.path(th.ID), b, 0o600)
}

func (srv *fcServer) load(id string) *fcThread {
	srv.mu.Lock()
	th := srv.threads[id]
	srv.mu.Unlock()
	if th != nil {
		return th
	}
	b, err := os.ReadFile(srv.path(id))
	if err != nil {
		return nil
	}
	th = &fcThread{}
	if json.Unmarshal(b, th) != nil {
		return nil
	}
	return th
}

func (srv *fcServer) handle(line []byte) {
	var m rpcMessage
	if json.Unmarshal(line, &m) != nil {
		return
	}
	if m.Method == "" && len(m.ID) > 0 { // a client's answer to our request
		id, _ := strconv.Atoi(string(m.ID))
		srv.mu.Lock()
		ch := srv.pending[id]
		delete(srv.pending, id)
		srv.mu.Unlock()
		if ch != nil {
			ch <- m
		}
		return
	}
	if len(m.ID) == 0 {
		return // initialized
	}
	var p map[string]json.RawMessage
	_ = json.Unmarshal(m.Params, &p)
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(p[k], &s)
		return s
	}
	switch m.Method {
	case "initialize":
		if os.Getenv("MADS_FAKE_CODEX_INIT_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "Error: not logged in. Run `codex login`.")
			os.Exit(1)
		}
		srv.result(m.ID, map[string]any{"userAgent": "mads/0.154.0", "codexHome": "/home/user/.codex", "platformFamily": "unix", "platformOs": "macos"})
	case "model/list":
		srv.result(m.ID, map[string]any{"data": []map[string]any{
			{"id": "gpt-5.6-terra", "model": "gpt-5.6-terra", "displayName": "GPT-5.6-Terra", "hidden": false, "isDefault": true},
			{"id": "gpt-5.6-luna", "model": "gpt-5.6-luna", "displayName": "GPT-5.6-Luna", "hidden": false, "isDefault": false},
			{"id": "internal-x", "model": "internal-x", "displayName": "Internal", "hidden": true, "isDefault": false},
		}, "nextCursor": nil})
	case "thread/start", "thread/resume", "thread/fork":
		srv.threadOpen(m.ID, m.Method, p, str)
	case "thread/items/list":
		th := srv.load(str("threadId"))
		if th == nil {
			srv.fail(m.ID, -32600, "no rollout found for thread id "+str("threadId"))
			return
		}
		limit := 100
		_ = json.Unmarshal(p["limit"], &limit)
		start, _ := strconv.Atoi(str("cursor"))
		srv.mu.Lock()
		items := append([]itemEntry(nil), th.Items...)
		srv.mu.Unlock()
		end := min(start+limit, len(items))
		var next any
		if end < len(items) {
			next = strconv.Itoa(end)
		}
		srv.result(m.ID, map[string]any{"data": items[start:end], "nextCursor": next})
	case "turn/start":
		th := srv.load(str("threadId"))
		if th == nil {
			srv.fail(m.ID, -32600, "thread not loaded")
			return
		}
		turn := &fcTurn{id: srv.newID(), interrupted: make(chan struct{}), steer: make(chan string, 4)}
		srv.mu.Lock()
		if th.turn != nil {
			srv.mu.Unlock()
			srv.fail(m.ID, -32600, "a turn is already running")
			return
		}
		th.turn = turn
		srv.mu.Unlock()
		var input []userInput
		_ = json.Unmarshal(p["input"], &input)
		text := ""
		if len(input) > 0 {
			text = input[0].Text
		}
		srv.result(m.ID, map[string]any{"turn": map[string]any{"id": turn.id, "items": []any{}, "status": "inProgress"}})
		go srv.runTurn(th, turn, text, str("clientUserMessageId"))
	case "turn/steer":
		th := srv.load(str("threadId"))
		srv.mu.Lock()
		var turn *fcTurn
		if th != nil {
			turn = th.turn
		}
		srv.mu.Unlock()
		if turn == nil || turn.id != str("expectedTurnId") {
			srv.fail(m.ID, -32600, "no active turn to steer")
			return
		}
		var input []userInput
		_ = json.Unmarshal(p["input"], &input)
		srv.result(m.ID, map[string]any{"turnId": turn.id})
		it := threadItem{Type: "userMessage", ID: srv.newID(), ClientID: str("clientUserMessageId"), Content: input}
		srv.item(th, turn, it, true)
		if len(input) > 0 {
			turn.steer <- input[0].Text
		}
	case "turn/interrupt":
		th := srv.load(str("threadId"))
		srv.mu.Lock()
		var turn *fcTurn
		if th != nil {
			turn = th.turn
		}
		srv.mu.Unlock()
		if turn != nil && turn.id == str("turnId") {
			turn.interrupt()
		}
		srv.result(m.ID, map[string]any{})
	case "thread/compact/start":
		th := srv.load(str("threadId"))
		turn := &fcTurn{id: srv.newID(), interrupted: make(chan struct{}), steer: make(chan string, 1)}
		srv.mu.Lock()
		busy := th == nil || th.turn != nil
		if !busy {
			th.turn = turn
		}
		srv.mu.Unlock()
		if busy {
			srv.fail(m.ID, -32600, "cannot compact while a turn is running")
			return
		}
		srv.result(m.ID, map[string]any{})
		go srv.runCompact(th, turn)
	default:
		srv.fail(m.ID, -32601, "method not found: "+m.Method)
	}
}

func (srv *fcServer) threadOpen(id json.RawMessage, method string, p map[string]json.RawMessage, str func(string) string) {
	cwd, sandbox, approval := str("cwd"), str("sandbox"), str("approvalPolicy")
	if sandbox != "read-only" {
		var cfg struct {
			Projects map[string]struct {
				TrustLevel string `json:"trust_level"`
			} `json:"projects"`
		}
		_ = json.Unmarshal(p["config"], &cfg)
		key := cwd
		if real, err := filepath.EvalSymlinks(cwd); err == nil {
			key = real
		}
		if cfg.Projects[key].TrustLevel == "" {
			srv.fail(id, -32600, "fakecodex: writable sandbox without a projects trust override would persist trust for "+key+" in ~/.codex/config.toml")
			return
		}
	}
	var th *fcThread
	switch method {
	case "thread/start":
		th = &fcThread{ID: srv.newID()}
	case "thread/resume", "thread/fork":
		orig := srv.load(str("threadId"))
		if orig == nil {
			srv.fail(id, -32600, "no rollout found for thread id "+str("threadId"))
			return
		}
		th = orig
		if method == "thread/fork" {
			th = &fcThread{ID: srv.newID(), Items: append([]itemEntry(nil), orig.Items...)}
		}
	}
	th.Cwd, th.Sandbox, th.Approval = cwd, sandbox, approval
	srv.mu.Lock()
	srv.threads[th.ID] = th
	srv.mu.Unlock()
	srv.save(th)
	model := str("model")
	if model == "" {
		model = "gpt-5.6-terra"
	}
	thread := map[string]any{"id": th.ID, "cwd": cwd, "path": filepath.Join(srv.store, th.ID+".json"), "status": map[string]any{"type": "idle"}}
	srv.result(id, map[string]any{"thread": thread, "model": model, "approvalPolicy": approval, "sandbox": map[string]any{"type": sandbox}})
	srv.notify("thread/started", map[string]any{"thread": thread})
}

// item announces it (started) and, when complete, records and completes it.
func (srv *fcServer) item(th *fcThread, turn *fcTurn, it threadItem, complete bool) {
	srv.notify("item/started", map[string]any{"item": it, "threadId": th.ID, "turnId": turn.id})
	if complete {
		srv.complete(th, turn, it)
	}
}

func (srv *fcServer) complete(th *fcThread, turn *fcTurn, it threadItem) {
	srv.mu.Lock()
	th.Items = append(th.Items, itemEntry{TurnID: turn.id, Item: it})
	srv.mu.Unlock()
	srv.save(th)
	srv.notify("item/completed", map[string]any{"item": it, "threadId": th.ID, "turnId": turn.id})
}

func (srv *fcServer) delta(th *fcThread, turn *fcTurn, itemID, text string) {
	srv.notify("item/agentMessage/delta", map[string]any{"threadId": th.ID, "turnId": turn.id, "itemId": itemID, "delta": text})
}

// reply streams one agent message in two deltas, then completes it.
func (srv *fcServer) reply(th *fcThread, turn *fcTurn, phase, text string) {
	it := threadItem{Type: "agentMessage", ID: "msg_" + srv.newID(), Phase: phase}
	srv.item(th, turn, it, false)
	half := len(text) / 2
	srv.delta(th, turn, it.ID, text[:half])
	srv.delta(th, turn, it.ID, text[half:])
	it.Text = text
	srv.complete(th, turn, it)
}

func (srv *fcServer) usage(th *fcThread, turn *fcTurn, last tokenBreakdown) {
	srv.mu.Lock()
	th.totals.InputTokens += last.InputTokens
	th.totals.CachedInputTokens += last.CachedInputTokens
	th.totals.OutputTokens += last.OutputTokens
	th.totals.TotalTokens += last.TotalTokens
	total := th.totals
	srv.mu.Unlock()
	srv.notify("thread/tokenUsage/updated", map[string]any{"threadId": th.ID, "turnId": turn.id,
		"tokenUsage": map[string]any{"total": total, "last": last, "modelContextWindow": 258400}})
}

func (srv *fcServer) endTurn(th *fcThread, turn *fcTurn, status string, terr any) {
	srv.mu.Lock()
	th.turn = nil
	srv.mu.Unlock()
	srv.notify("thread/status/changed", map[string]any{"threadId": th.ID, "status": map[string]any{"type": "idle"}})
	srv.notify("turn/completed", map[string]any{"threadId": th.ID,
		"turn": map[string]any{"id": turn.id, "items": []any{}, "status": status, "error": terr}})
}

var markerRE = regexp.MustCompile(`mk-[a-z0-9-]+`)

func (srv *fcServer) runTurn(th *fcThread, turn *fcTurn, text, clientID string) {
	srv.notify("thread/status/changed", map[string]any{"threadId": th.ID, "status": map[string]any{"type": "active", "activeFlags": []any{}}})
	srv.notify("turn/started", map[string]any{"threadId": th.ID, "turn": map[string]any{"id": turn.id, "status": "inProgress"}})
	var cid any = clientID
	if clientID == "" {
		cid = nil
	}
	user := threadItem{Type: "userMessage", ID: srv.newID(), Content: []userInput{{Type: "text", Text: text}}}
	srv.notify("item/started", map[string]any{"item": withClientID(user, cid), "threadId": th.ID, "turnId": turn.id})
	user.ClientID = clientID
	srv.complete(th, turn, user)

	status := "completed"
	var terr any
	call := tokenBreakdown{TotalTokens: 1207, InputTokens: 1200, CachedInputTokens: 400, OutputTokens: 7}
	cmd, arg, _ := strings.Cut(text, " ")
	switch cmd {
	case "echo":
		srv.reply(th, turn, "final_answer", "echo: "+arg)
	case "echo-nodelta":
		it := threadItem{Type: "agentMessage", ID: "msg_" + srv.newID(), Phase: "final_answer"}
		srv.item(th, turn, it, false)
		it.Text = "echo: " + arg
		srv.complete(th, turn, it)
	case "recall":
		var seen []string
		srv.mu.Lock()
		for _, e := range th.Items {
			if e.Item.Type == "userMessage" && e.Item.ID != user.ID {
				seen = append(seen, markerRE.FindAllString(e.Item.userText(), -1)...)
			}
		}
		srv.mu.Unlock()
		srv.reply(th, turn, "final_answer", "recall: "+strings.Join(seen, " "))
	case "think":
		rs := threadItem{Type: "reasoning", ID: "rs_" + srv.newID()}
		srv.item(th, turn, rs, false)
		srv.notify("item/reasoning/summaryTextDelta", map[string]any{"threadId": th.ID, "turnId": turn.id, "itemId": rs.ID, "delta": "**Planning**", "summaryIndex": 0})
		rs.Summary = []string{"**Planning**"}
		srv.complete(th, turn, rs)
		srv.reply(th, turn, "commentary", "Checking.")
		srv.reply(th, turn, "final_answer", "Done.")
	case "web":
		ws := threadItem{Type: "webSearch", ID: "exec-" + srv.newID()}
		srv.item(th, turn, ws, false)
		ws.Query = "go stable version"
		ws.Action = json.RawMessage(`{"type":"search","query":"go stable version","queries":null}`)
		ws.Results = json.RawMessage(`[{"type":"text_result","title":"Release History","url":"https://go.dev/doc/devel/release"}]`)
		srv.complete(th, turn, ws)
		srv.reply(th, turn, "final_answer", "Go is current.")
	case "attempt-write":
		status = srv.attemptWrite(th, turn)
	case "attempt-shell":
		status = srv.attemptShell(th, turn)
	case "slow":
		status = srv.slow(th, turn)
	case "sleeper":
		status = srv.sleeper(th, turn)
	case "flood":
		// Far more non-text events than the client's buffer holds.
		for range 600 {
			rs := threadItem{Type: "reasoning", ID: "rs_" + srv.newID(), Summary: []string{"**thinking**"}}
			srv.item(th, turn, rs, true)
		}
		srv.reply(th, turn, "final_answer", "flooded")
	case "crash":
		fmt.Fprintln(os.Stderr, "fakecodex: thread panicked: boom")
		os.Exit(3)
	case "fail-limit":
		status = "failed"
		terr = map[string]any{"message": "You've hit your usage limit.", "codexErrorInfo": "usageLimitExceeded"}
	default:
		srv.reply(th, turn, "final_answer", "ok")
	}
	if status != "interrupted" && status != "failed" {
		srv.usage(th, turn, call)
	}
	srv.endTurn(th, turn, status, terr)
}

// withClientID renders a userMessage with clientId null when mads sent none,
// as codex does.
func withClientID(it threadItem, cid any) map[string]any {
	b, _ := json.Marshal(it)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["clientId"] = cid
	return m
}

// gate asks the client when the thread's approval policy says codex would
// ("untrusted": every edit and every non-read command), and reports whether
// the call may run. "never" runs without asking.
func (srv *fcServer) gate(th *fcThread, turn *fcTurn, method, itemID string, extra map[string]any) (allowed, asked bool) {
	if th.Approval != "untrusted" {
		return true, false
	}
	params := map[string]any{"threadId": th.ID, "turnId": turn.id, "itemId": itemID, "startedAtMs": time.Now().UnixMilli()}
	for k, v := range extra {
		params[k] = v
	}
	res := srv.request(method, params, turn)
	var r struct {
		Decision string `json:"decision"`
	}
	_ = json.Unmarshal(res, &r)
	return r.Decision == "accept", true
}

func (srv *fcServer) attemptWrite(th *fcThread, turn *fcTurn) string {
	path := filepath.Join(th.Cwd, "fake-write.txt")
	it := threadItem{Type: "fileChange", ID: "exec-" + srv.newID(), Status: "inProgress",
		Changes: []fileChange{{Path: path, Diff: "hi\n"}}}
	it.Changes[0].Kind.Type = "add"
	srv.item(th, turn, it, false)
	if th.Sandbox == "read-only" {
		it.Status = "failed" // the sandbox refuses the patch
	} else if allowed, _ := srv.gate(th, turn, "item/fileChange/requestApproval", it.ID, map[string]any{"reason": nil, "grantRoot": nil}); !allowed {
		it.Status = "declined"
	} else if err := os.WriteFile(path, []byte("hi\n"), 0o600); err != nil {
		it.Status = "failed"
	} else {
		it.Status = "completed"
	}
	srv.complete(th, turn, it)
	srv.reply(th, turn, "final_answer", "write "+it.Status)
	return "completed"
}

func (srv *fcServer) attemptShell(th *fcThread, turn *fcTurn) string {
	it := threadItem{Type: "commandExecution", ID: "exec-" + srv.newID(), Status: "inProgress",
		Command: "/bin/zsh -lc 'touch fake-shell.txt'", Cwd: th.Cwd,
		CommandActions: []commandAction{{Type: "unknown", Command: "touch fake-shell.txt"}}}
	srv.item(th, turn, it, false)
	allowed, _ := srv.gate(th, turn, "item/commandExecution/requestApproval", it.ID, map[string]any{
		"command": it.Command, "cwd": th.Cwd, "commandActions": it.CommandActions,
		"availableDecisions": []any{"accept", "cancel"}})
	switch {
	case !allowed:
		it.Status = "declined"
	case th.Sandbox == "read-only":
		out, code := "touch: fake-shell.txt: Operation not permitted\n", 1
		it.Status, it.AggregatedOutput, it.ExitCode = "failed", &out, &code
	default:
		_ = os.WriteFile(filepath.Join(th.Cwd, "fake-shell.txt"), nil, 0o600)
		out, code := "", 0
		it.Status, it.AggregatedOutput, it.ExitCode = "completed", &out, &code
	}
	srv.complete(th, turn, it)
	srv.reply(th, turn, "final_answer", "shell "+it.Status)
	return "completed"
}

// slow streams ticks until interrupted (the message never completes and is
// not stored — survey finding 6) or steered (the steer's text is answered
// inside the same turn).
func (srv *fcServer) slow(th *fcThread, turn *fcTurn) string {
	it := threadItem{Type: "agentMessage", ID: "msg_" + srv.newID(), Phase: "final_answer"}
	srv.item(th, turn, it, false)
	var sent strings.Builder
	for i := range 400 {
		select {
		case <-turn.interrupted:
			return "interrupted"
		case s := <-turn.steer:
			it.Text = sent.String()
			srv.complete(th, turn, it)
			srv.reply(th, turn, "final_answer", "steered: "+s)
			return "completed"
		case <-time.After(25 * time.Millisecond):
			tick := fmt.Sprintf("tick %d ", i)
			sent.WriteString(tick)
			srv.delta(th, turn, it.ID, tick)
		}
	}
	it.Text = sent.String()
	srv.complete(th, turn, it)
	return "completed"
}

// sleeper starts a long command in a process group of its own — exactly
// what codex does with tool commands — and blocks until interrupted.
func (srv *fcServer) sleeper(th *fcThread, turn *fcTurn) string {
	it := threadItem{Type: "commandExecution", ID: "exec-" + srv.newID(), Status: "inProgress",
		Command: "/bin/zsh -lc 'sleep 300'", Cwd: th.Cwd,
		CommandActions: []commandAction{{Type: "unknown", Command: "sleep 300"}}}
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "fakecodex: sleeper:", err)
		return "failed"
	}
	go func() { _ = cmd.Wait() }()
	if path := os.Getenv("MADS_FAKE_CODEX_PIDFILE"); path != "" {
		_ = os.WriteFile(path+".tmp", []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		_ = os.Rename(path+".tmp", path)
	}
	srv.item(th, turn, it, false)
	<-turn.interrupted
	return "interrupted"
}

func (srv *fcServer) runCompact(th *fcThread, turn *fcTurn) {
	srv.notify("turn/started", map[string]any{"threadId": th.ID, "turn": map[string]any{"id": turn.id, "status": "inProgress"}})
	it := threadItem{Type: "contextCompaction", ID: srv.newID()}
	srv.item(th, turn, it, false)
	srv.usage(th, turn, tokenBreakdown{TotalTokens: 1200})
	srv.complete(th, turn, it)
	srv.endTurn(th, turn, "completed", nil)
}

// fakeEnv is the stand-in's environment for one test.
func fakeEnv(t *testing.T) (env []string, store string) {
	t.Helper()
	store = t.TempDir()
	// GORACE: a -race build of the stand-in otherwise sleeps 1 s at every
	// exit, which each Close waits out.
	return []string{"MADS_FAKE_CODEX=1", "MADS_FAKE_CODEX_STORE=" + store, "GORACE=atexit_sleep_ms=0"}, store
}

// fakeProvider is the adapter pointed at the stand-in.
func fakeProvider(t *testing.T, extraEnv ...string) *Provider {
	t.Helper()
	env, _ := fakeEnv(t)
	return New(Config{Bin: os.Args[0], Env: append(env, extraEnv...)})
}

// sortedKeys is a test helper for deterministic map assertions.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
