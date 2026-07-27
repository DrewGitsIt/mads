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
	in := strings.NewReader(strings.Join(reqLines, "\n") + "\n")
	var out bytes.Buffer
	srv := New(router.URL, sessionID, in, &out)
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

func TestInitializeAndToolsList(t *testing.T) {
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request to router: %s %s", r.Method, r.URL.Path)
	}))
	defer router.Close()

	resps := runLines(t, router, "leader-1",
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

	listResult, ok := resps[1].Result.(map[string]any)
	if !ok {
		t.Fatalf("tools/list result = %+v", resps[1].Result)
	}
	tools, ok := listResult["tools"].([]any)
	if !ok || len(tools) != 4 {
		t.Fatalf("tools = %+v, want 4 entries", listResult["tools"])
	}
	names := map[string]bool{}
	for _, tool := range tools {
		m := tool.(map[string]any)
		names[m["name"].(string)] = true
	}
	for _, want := range []string{"list_links", "get_summary", "get_turns", "send_message"} {
		if !names[want] {
			t.Errorf("tools/list missing %q, got %v", want, names)
		}
	}
}

func TestToolsCallListLinks(t *testing.T) {
	var gotPath, gotMethod, gotActor string
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotActor = r.URL.Path, r.Method, r.Header.Get("X-Mads-Actor")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"link_id":"lnk_1","subject_name":"subject"}]`))
	}))
	defer router.Close()

	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_links","arguments":{}}}`,
	)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if gotMethod != http.MethodGet || gotPath != "/sessions/leader-1/orchestrate/subjects" {
		t.Fatalf("router saw %s %s", gotMethod, gotPath)
	}
	if gotActor != "session-leader-1" {
		t.Fatalf("X-Mads-Actor = %q, want session-leader-1", gotActor)
	}
	result := resps[0].Result.(map[string]any)
	if result["isError"] != false {
		t.Fatalf("isError = %v, want false", result["isError"])
	}
	content := result["content"].([]any)[0].(map[string]any)
	if content["text"] != `[{"link_id":"lnk_1","subject_name":"subject"}]` {
		t.Fatalf("content text = %q", content["text"])
	}
}

func TestToolsCallGetSummary(t *testing.T) {
	var gotPath string
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"source":"summary","text":"did the thing"}`))
	}))
	defer router.Close()

	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_summary","arguments":{"subject_session_id":"ses_subject"}}}`,
	)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if gotPath != "/sessions/leader-1/orchestrate/summary?for=ses_subject" {
		t.Fatalf("router saw path %q", gotPath)
	}
	result := resps[0].Result.(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	if content["text"] != `{"source":"summary","text":"did the thing"}` {
		t.Fatalf("content text = %q", content["text"])
	}
}

func TestToolsCallGetTurns(t *testing.T) {
	var gotPath string
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"from_turn":1,"to_turn":2,"entries":[]}`))
	}))
	defer router.Close()

	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_turns","arguments":{"subject_session_id":"ses_subject","from_turn":1,"to_turn":2}}}`,
	)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if gotPath != "/sessions/leader-1/orchestrate/turns?for=ses_subject&from_turn=1&to_turn=2" {
		t.Fatalf("router saw path %q", gotPath)
	}
	result := resps[0].Result.(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	if content["text"] != `{"from_turn":1,"to_turn":2,"entries":[]}` {
		t.Fatalf("content text = %q", content["text"])
	}
}

func TestToolsCallSendMessage(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"delivered"}`))
	}))
	defer router.Close()

	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_message","arguments":{"link_id":"lnk_1","text":"hello","wait_ms":500}}}`,
	)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if gotPath != "/links/lnk_1/orchestrate/message" {
		t.Fatalf("router saw path %q", gotPath)
	}
	if gotBody["text"] != "hello" || gotBody["wait_ms"] != float64(500) {
		t.Fatalf("router saw body %+v", gotBody)
	}
	result := resps[0].Result.(map[string]any)
	if result["isError"] != false {
		t.Fatalf("isError = %v, want false", result["isError"])
	}
}

func TestToolsCallErrorStatusMarksIsError(t *testing.T) {
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"link does not carry the orchestrate grant"}`))
	}))
	defer router.Close()

	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_message","arguments":{"link_id":"lnk_1","text":"hello"}}}`,
	)
	result := resps[0].Result.(map[string]any)
	if result["isError"] != true {
		t.Fatalf("isError = %v, want true", result["isError"])
	}
}

func TestToolsCallUnknownToolReturnsError(t *testing.T) {
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request to router: %s", r.URL.Path)
	}))
	defer router.Close()

	resps := runLines(t, router, "leader-1",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nonexistent","arguments":{}}}`,
	)
	if resps[0].Error == nil {
		t.Fatalf("resp = %+v, want an error", resps[0])
	}
}
