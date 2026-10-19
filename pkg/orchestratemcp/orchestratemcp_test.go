package orchestratemcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runLines feeds reqLines (each a raw JSON-RPC request, no trailing
// newline) through a Server backed by router, and returns the decoded
// response for every request that carried an id, in order.
func runLines(t *testing.T, router *httptest.Server, sessionID string, reqLines ...string) []rpcResponse {
	t.Helper()
	return runLinesMode(t, router, sessionID, false, reqLines...)
}

// runLinesMode is runLines with an explicit readOnly mode, for exercising
// the read-grant-only tool slice (item 1).
func runLinesMode(t *testing.T, router *httptest.Server, sessionID string, readOnly bool, reqLines ...string) []rpcResponse {
	t.Helper()
	in := strings.NewReader(strings.Join(reqLines, "\n") + "\n")
	var out bytes.Buffer
	srv := New(router.URL, sessionID, readOnly, in, &out)
	if err := srv.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var resps []rpcResponse
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("decode response line %q: %v", line, err)
		}
		resps = append(resps, resp)
	}
	return resps
}

// defaultSubjects is the subjects route's body for fakeRouter: one linked
// session, "payments", over link lnk_1.
const defaultSubjects = `[{"link_id":"lnk_1","subject_session_id":"ses_pay","subject_name":"payments","grants":["read","inject","orchestrate"],"activity":"idle","cadence":"on_signal"}]`

// routerCall is one non-subjects request fakeRouter saw.
type routerCall struct {
	Method, Path, Actor string
	Body                map[string]any
}

// fakeRouter serves subjects (the resolution table every per-session tool
// reads first) for leaderID and records every other request, answering it
// with status and reply.
func fakeRouter(t *testing.T, leaderID, subjects string, status int, reply string) (*httptest.Server, *[]routerCall) {
	t.Helper()
	var calls []routerCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/sessions/"+leaderID+"/orchestrate/subjects" {
			_, _ = w.Write([]byte(subjects))
			return
		}
		c := routerCall{Method: r.Method, Path: r.URL.Path, Actor: r.Header.Get("X-Mads-Actor")}
		if r.URL.RawQuery != "" {
			c.Path += "?" + r.URL.RawQuery
		}
		_ = json.NewDecoder(r.Body).Decode(&c.Body)
		calls = append(calls, c)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func noRouter(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request to router: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// resultText returns a tools/call result's text and isError.
func resultText(t *testing.T, resp rpcResponse) (string, bool) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("resp is a JSON-RPC error %+v, want a tool result", resp.Error)
	}
	result := resp.Result.(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	return content["text"].(string), result["isError"] == true
}

func toolsByName(t *testing.T, resp rpcResponse) map[string]map[string]any {
	t.Helper()
	tools := resp.Result.(map[string]any)["tools"].([]any)
	out := map[string]map[string]any{}
	for _, tool := range tools {
		m := tool.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

func TestInitializeAndToolsList(t *testing.T) {
	resps := runLines(t, noRouter(t), "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2 (notification must not respond)", len(resps))
	}

	initResult, ok := resps[0].Result.(map[string]any)
	if !ok || initResult["protocolVersion"] != protocolVersion {
		t.Fatalf("initialize result = %+v", resps[0].Result)
	}
	instr, _ := initResult["instructions"].(string)
	for _, want := range []string{"list_linked_sessions", "message_session", "get_session_summary", "get_session_turns", "attributed to you"} {
		if !strings.Contains(instr, want) {
			t.Errorf("instructions %q missing %q", instr, want)
		}
	}

	tools := toolsByName(t, resps[1])
	// Every per-session tool takes `session`; list_linked_sessions takes
	// nothing (2026-10-19 rename).
	want := map[string][]string{
		"list_linked_sessions": nil,
		"message_session":      {"session", "text"},
		"get_session_summary":  {"session"},
		"get_session_turns":    {"session"},
		"check_mailbox":        {"session"},
		"set_attention":        {"session", "cadence"},
	}
	if len(tools) != len(want) {
		t.Fatalf("tools = %v, want exactly %v", tools, want)
	}
	for name, required := range want {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("tools/list missing %q", name)
			continue
		}
		if d, _ := tool["description"].(string); d == "" {
			t.Errorf("%s has no description", name)
		}
		schema := tool["inputSchema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		var got []string
		if r, ok := schema["required"].([]any); ok {
			for _, v := range r {
				got = append(got, v.(string))
			}
		}
		if strings.Join(got, ",") != strings.Join(required, ",") {
			t.Errorf("%s required = %v, want %v", name, got, required)
		}
		if required == nil {
			continue
		}
		session, ok := props["session"].(map[string]any)
		if !ok || session["description"] == "" || session["description"] == nil {
			t.Errorf("%s session param = %+v, want a described string", name, props["session"])
		}
	}

	// get_session_turns slices transcript turns by count and an optional
	// from_turn; the old seq-range to_turn is gone (2026-10-19).
	turnsProps := tools["get_session_turns"]["inputSchema"].(map[string]any)["properties"].(map[string]any)
	for _, p := range []string{"count", "from_turn"} {
		prop, ok := turnsProps[p].(map[string]any)
		if !ok || prop["type"] != "integer" || prop["minimum"] != float64(1) {
			t.Errorf("get_session_turns %s = %+v, want an integer with minimum 1", p, turnsProps[p])
		}
	}
	if _, ok := turnsProps["to_turn"]; ok {
		t.Errorf("get_session_turns still takes to_turn")
	}
	for _, want := range []string{"input and output", "count", "from_turn"} {
		if d := tools["get_session_turns"]["description"].(string); !strings.Contains(d, want) {
			t.Errorf("get_session_turns description missing %q: %s", want, d)
		}
	}
}

// TestReadOnlyToolsListExcludesMessageSession proves a read-grant-only
// session (MADS_ORCHESTRATE_READONLY, plumbed through as New's readOnly
// bool) never advertises message_session — the pull tools it does get all
// remain (item 1: "pull tools for read grants") — and that its instructions
// don't point at a tool it lacks.
func TestReadOnlyToolsListExcludesMessageSession(t *testing.T) {
	resps := runLinesMode(t, noRouter(t), "monitor-1", true,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2", len(resps))
	}
	instr, _ := resps[0].Result.(map[string]any)["instructions"].(string)
	if instr == "" || strings.Contains(instr, "message_session") {
		t.Fatalf("read-only instructions = %q, want non-empty and without message_session", instr)
	}
	tools := toolsByName(t, resps[1])
	if _, ok := tools["message_session"]; ok {
		t.Fatalf("tools/list in read-only mode advertised message_session: %v", tools)
	}
	for _, want := range []string{"list_linked_sessions", "get_session_summary", "get_session_turns", "check_mailbox", "set_attention"} {
		if _, ok := tools[want]; !ok {
			t.Errorf("read-only tools/list missing %q", want)
		}
	}
}

// TestReadOnlyMessageSessionRejected proves a read-only Server refuses a
// message_session tools/call outright — never forwarding it to the Router —
// even if a read-grant-only session's agent tries to call it anyway.
func TestReadOnlyMessageSessionRejected(t *testing.T) {
	resps := runLinesMode(t, noRouter(t), "monitor-1", true,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"message_session","arguments":{"session":"payments","text":"hello"}}}`,
	)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if resps[0].Error == nil {
		t.Fatalf("resp = %+v, want an error rejecting message_session", resps[0])
	}
}

// TestToolsCallSetAttention proves set_attention resolves the session name
// to its link and forwards to the Router's per-session attention route with
// the caller's own session ID (item 2).
func TestToolsCallSetAttention(t *testing.T) {
	router, calls := fakeRouter(t, "monitor-1", defaultSubjects, http.StatusOK, `{"id":"lnk_1","cadence":"manual_only"}`)
	resps := runLines(t, router, "monitor-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"set_attention","arguments":{"session":"payments","cadence":"manual_only"}}}`,
	)
	if _, isErr := resultText(t, resps[0]); isErr {
		t.Fatalf("isError = true, want false")
	}
	if len(*calls) != 1 || (*calls)[0].Path != "/sessions/monitor-1/orchestrate/attention" {
		t.Fatalf("router saw %+v", *calls)
	}
	if b := (*calls)[0].Body; b["link_id"] != "lnk_1" || b["cadence"] != "manual_only" {
		t.Fatalf("router saw body %+v", b)
	}
}

func TestToolsCallCheckMailbox(t *testing.T) {
	router, calls := fakeRouter(t, "leader-1", defaultSubjects, http.StatusOK, `[]`)
	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_mailbox","arguments":{"session":"payments"}}}`,
	)
	if _, isErr := resultText(t, resps[0]); isErr {
		t.Fatalf("isError = true, want false")
	}
	if len(*calls) != 1 || (*calls)[0].Path != "/sessions/leader-1/orchestrate/mailbox" || (*calls)[0].Body["link_id"] != "lnk_1" {
		t.Fatalf("router saw %+v", *calls)
	}
}

// TestToolsCallListLinkedSessions proves list_linked_sessions reshapes the
// subjects route into the agent-facing rows: the name it passes back as
// `session`, the session ID, grants, activity, cadence, turns (asked for
// with ?turns=1) — and no link ID.
func TestToolsCallListLinkedSessions(t *testing.T) {
	var gotActor string
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sessions/leader-1/orchestrate/subjects" || r.URL.RawQuery != "turns=1" {
			t.Fatalf("router saw %s %s", r.Method, r.URL)
		}
		gotActor = r.Header.Get("X-Mads-Actor")
		_, _ = w.Write([]byte(strings.Replace(defaultSubjects, `"cadence":"on_signal"`, `"cadence":"on_signal","turns":42`, 1)))
	}))
	defer router.Close()

	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_linked_sessions","arguments":{}}}`,
	)
	if gotActor != "session-leader-1" {
		t.Fatalf("X-Mads-Actor = %q, want session-leader-1", gotActor)
	}
	text, isErr := resultText(t, resps[0])
	if isErr {
		t.Fatalf("isError = true: %s", text)
	}
	want := `[{"name":"payments","session_id":"ses_pay","grants":["read","inject","orchestrate"],"activity":"idle","cadence":"on_signal","turns":42}]`
	if text != want {
		t.Fatalf("content text = %s, want %s", text, want)
	}
}

func TestToolsCallGetSessionSummary(t *testing.T) {
	router, calls := fakeRouter(t, "leader-1", defaultSubjects, http.StatusOK, `{"source":"summary","text":"did the thing"}`)
	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_session_summary","arguments":{"session":"payments"}}}`,
	)
	if len(*calls) != 1 || (*calls)[0].Path != "/sessions/leader-1/orchestrate/summary?for=ses_pay" {
		t.Fatalf("router saw %+v", *calls)
	}
	if text, _ := resultText(t, resps[0]); text != `{"source":"summary","text":"did the thing"}` {
		t.Fatalf("content text = %q", text)
	}
}

// TestToolsCallGetSessionTurns proves get_session_turns forwards count and
// from_turn (each only when set) and hands the Router's plain-text
// rendering to the model as is — a from_turn past the end (400) as a tool
// error the agent can read (2026-10-19).
func TestToolsCallGetSessionTurns(t *testing.T) {
	cases := []struct {
		args     string
		status   int
		reply    string
		wantPath string
		wantErr  bool
	}{
		{`{"session":"payments"}`, http.StatusOK, "payments — turn 4 of 4\n", "/sessions/leader-1/orchestrate/turns?for=ses_pay", false},
		{`{"session":"payments","count":3}`, http.StatusOK, "payments — turns 2–4 of 4\n", "/sessions/leader-1/orchestrate/turns?for=ses_pay&count=3", false},
		{`{"session":"payments","from_turn":2,"count":2}`, http.StatusOK, "payments — turns 2–3 of 4\n", "/sessions/leader-1/orchestrate/turns?for=ses_pay&count=2&from_turn=2", false},
		{`{"session":"payments","from_turn":9}`, http.StatusBadRequest, "payments has 4 turns; from_turn 9 is past the end.", "/sessions/leader-1/orchestrate/turns?for=ses_pay&from_turn=9", true},
	}
	for _, tc := range cases {
		router, calls := fakeRouter(t, "leader-1", defaultSubjects, tc.status, tc.reply)
		resps := runLines(t, router, "leader-1",
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_session_turns","arguments":`+tc.args+`}}`,
		)
		if len(*calls) != 1 || (*calls)[0].Path != tc.wantPath {
			t.Fatalf("%s: router saw %+v, want %s", tc.args, *calls, tc.wantPath)
		}
		if text, isErr := resultText(t, resps[0]); text != tc.reply || isErr != tc.wantErr {
			t.Fatalf("%s: content text = %q isError %v", tc.args, text, isErr)
		}
	}
}

func TestToolsCallMessageSession(t *testing.T) {
	router, calls := fakeRouter(t, "leader-1", defaultSubjects, http.StatusAccepted, `{"status":"delivered"}`)
	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"message_session","arguments":{"session":"payments","text":"hello","wait_ms":500}}}`,
	)
	if _, isErr := resultText(t, resps[0]); isErr {
		t.Fatalf("isError = true, want false")
	}
	if len(*calls) != 1 || (*calls)[0].Path != "/links/lnk_1/orchestrate/message" {
		t.Fatalf("router saw %+v", *calls)
	}
	if b := (*calls)[0].Body; b["text"] != "hello" || b["wait_ms"] != float64(500) {
		t.Fatalf("router saw body %+v", b)
	}
}

// TestSessionResolution covers the `session` lookup order — exact name,
// then exact session ID, then case-insensitive name — and the errors an
// agent can act on: unknown lists the valid names, ambiguous lists the
// candidates with IDs. Errors are isError tool results, never JSON-RPC
// errors, and never reach a per-session route.
func TestSessionResolution(t *testing.T) {
	subjects := `[
		{"link_id":"lnk_pay","subject_session_id":"ses_pay","subject_name":"payments","grants":["orchestrate"]},
		{"link_id":"lnk_PAY","subject_session_id":"ses_PAY","subject_name":"Payments","grants":["orchestrate"]},
		{"link_id":"lnk_api","subject_session_id":"ses_api","subject_name":"API work","grants":["orchestrate"]},
		{"link_id":"lnk_w1","subject_session_id":"ses_w1","subject_name":"worker","grants":["read"]},
		{"link_id":"lnk_w2","subject_session_id":"ses_w2","subject_name":"worker","grants":["read"]}
	]`
	cases := []struct {
		name, session string
		wantPath      string   // the summary route's ?for= on success
		wantErr       []string // substrings of the isError text otherwise
	}{
		{name: "exact name wins over case-insensitive", session: "Payments", wantPath: "ses_PAY"},
		{name: "exact name", session: "payments", wantPath: "ses_pay"},
		{name: "session ID", session: "ses_w2", wantPath: "ses_w2"},
		{name: "case-insensitive name", session: "api WORK", wantPath: "ses_api"},
		{name: "unknown lists names", session: "billing",
			wantErr: []string{`"billing"`, `"payments"`, `"API work"`, `"worker" (ses_w1)`, `"worker" (ses_w2)`}},
		{name: "ambiguous lists names and IDs", session: "worker",
			wantErr: []string{"More than one", `"worker" (ses_w1)`, `"worker" (ses_w2)`, "session ID"}},
		{name: "ambiguous case-insensitive", session: "PAYMENTS",
			wantErr: []string{`"payments" (ses_pay)`, `"Payments" (ses_PAY)`}},
		{name: "empty", session: "", wantErr: []string{"session is required", `"payments"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, calls := fakeRouter(t, "leader-1", subjects, http.StatusOK, `{}`)
			args, _ := json.Marshal(map[string]any{"session": tc.session})
			resps := runLines(t, router, "leader-1",
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_session_summary","arguments":`+string(args)+`}}`,
			)
			text, isErr := resultText(t, resps[0])
			if tc.wantErr == nil {
				if isErr {
					t.Fatalf("isError: %s", text)
				}
				if len(*calls) != 1 || (*calls)[0].Path != "/sessions/leader-1/orchestrate/summary?for="+tc.wantPath {
					t.Fatalf("router saw %+v, want summary for %s", *calls, tc.wantPath)
				}
				return
			}
			if !isErr {
				t.Fatalf("isError = false, text %q", text)
			}
			if len(*calls) != 0 {
				t.Fatalf("router saw %+v, want no per-session call", *calls)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(text, want) {
					t.Errorf("error %q missing %q", text, want)
				}
			}
		})
	}
}

// TestSessionResolutionNoLinks: with nothing linked, the error says so
// rather than listing an empty set.
func TestSessionResolutionNoLinks(t *testing.T) {
	router, _ := fakeRouter(t, "leader-1", `[]`, http.StatusOK, `{}`)
	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_mailbox","arguments":{"session":"payments"}}}`,
	)
	text, isErr := resultText(t, resps[0])
	if !isErr || !strings.Contains(text, "no linked sessions") {
		t.Fatalf("result = %q (isError %v), want a no-linked-sessions error", text, isErr)
	}
}

// TestSessionResolutionPerCall proves the lookup isn't cached: a rename
// between two calls on the same server resolves to the new name.
func TestSessionResolutionPerCall(t *testing.T) {
	name := "untitled"
	var forParams []string
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/orchestrate/subjects") {
			_, _ = w.Write([]byte(`[{"link_id":"lnk_1","subject_session_id":"ses_1","subject_name":"` + name + `"}]`))
			name = "payments" // the session is auto-renamed after the first lookup
			return
		}
		forParams = append(forParams, r.URL.Query().Get("for"))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer router.Close()

	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_session_summary","arguments":{"session":"untitled"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_session_summary","arguments":{"session":"payments"}}}`,
	)
	for i, resp := range resps {
		if text, isErr := resultText(t, resp); isErr {
			t.Fatalf("call %d: isError: %s", i+1, text)
		}
	}
	if strings.Join(forParams, ",") != "ses_1,ses_1" {
		t.Fatalf("summary calls for %v, want ses_1 twice", forParams)
	}
}

func TestToolsCallErrorStatusMarksIsError(t *testing.T) {
	router, _ := fakeRouter(t, "leader-1", defaultSubjects, http.StatusForbidden, `{"error":"link does not carry the orchestrate grant"}`)
	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"message_session","arguments":{"session":"payments","text":"hello"}}}`,
	)
	if _, isErr := resultText(t, resps[0]); !isErr {
		t.Fatalf("isError = false, want true")
	}
}

func TestToolsCallUnknownToolReturnsError(t *testing.T) {
	resps := runLines(t, noRouter(t), "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nonexistent","arguments":{}}}`,
	)
	if resps[0].Error == nil {
		t.Fatalf("resp = %+v, want an error", resps[0])
	}
}
