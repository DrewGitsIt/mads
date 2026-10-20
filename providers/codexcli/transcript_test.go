package codexcli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func loadItems(t *testing.T, name string) []itemEntry {
	t.Helper()
	raw, err := os.ReadFile("testdata/captures/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Data []itemEntry `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	return res.Data
}

// The real thread/items/list of the survey's five-turn thread: users,
// agents (commentary and final answers alike), the command with its
// result, the web search, and nothing of the interrupted turn's partial
// story — codex never stored it.
func TestNormalizeRun1Items(t *testing.T) {
	msgs := normalizeItems(loadItems(t, "run1-items-list.json"))
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	want := "user agent user agent tool tool_result agent user agent user agent tool tool_result agent user"
	if got := strings.Join(roles, " "); got != want {
		t.Fatalf("roles\n got %s\nwant %s", got, want)
	}
	if msgs[0].Text != "Reply with exactly one short sentence saying hello." || msgs[1].Text != "Hello!" {
		t.Errorf("turn a = %+v / %+v", msgs[0], msgs[1])
	}
	cmd := msgs[4]
	if cmd.Tool.ID != "exec-bceea15e-6692-431c-80a1-61e1e34f266a" || cmd.Tool.Title != "ls" {
		t.Errorf("command row = %+v", cmd.Tool)
	}
	if r := msgs[5]; r.Tool.ID != cmd.Tool.ID || r.IsError {
		t.Errorf("command result = %+v", r)
	}
	web := msgs[11]
	if web.Tool.Name != "web_search" || web.Tool.Title != "site:go.dev current stable Go release version" {
		t.Errorf("web row = %+v", web.Tool)
	}
	if !strings.Contains(msgs[12].Text, "go.dev") {
		t.Errorf("web result = %q", msgs[12].Text)
	}
	if last := msgs[len(msgs)-1]; last.Role != "user" || last.Text != "Write a 400-word story about a lighthouse. No tools." {
		t.Errorf("last row = %+v, want the interrupted turn's user message and nothing after", last)
	}
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Text, "<environment_context>") {
			t.Errorf("injected context surfaced as a user message: %q", m.Text)
		}
	}
}

// A thread whose app-server was killed mid-command, then resumed: the
// command item is gone from the store; the turn's user message and the
// commentary before the command remain.
func TestNormalizeKilledTurn(t *testing.T) {
	msgs := normalizeItems(loadItems(t, "run3-resume-items-list.json"))
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	if got := strings.Join(roles, " "); got != "user agent" {
		t.Fatalf("roles = %s", got)
	}
	if !strings.HasPrefix(msgs[1].Text, "I’ll run the requested shell command") {
		t.Errorf("agent row = %q", msgs[1].Text)
	}
}

func TestNormalizeInProgressToolHasNoResult(t *testing.T) {
	msgs := normalizeItems([]itemEntry{{Item: threadItem{Type: "commandExecution", ID: "exec-1", Status: "inProgress", Command: "sleep 9"}}})
	if len(msgs) != 1 || msgs[0].Role != "tool" {
		t.Fatalf("msgs = %+v", msgs)
	}
}
