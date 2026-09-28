package api_test

import (
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/providers/fake"
)

func TestDescribeAgentOverRouter(t *testing.T) {
	srv, _ := newTestServerWithScenario(t, fake.Scenario{Name: "describe", Rules: []fake.Rule{{
		Match: "You are a QA engineer.",
		Reply: "Runs and triages automated tests.",
	}}})

	resp := postJSON(t, srv.URL+"/agents/describe", map[string]string{"prompt": "You are a QA engineer."})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON[struct {
		Result string `json:"result"`
	}](t, resp)
	if body.Result != "Runs and triages automated tests." {
		t.Fatalf("result = %q", body.Result)
	}
}

func TestSuggestCapabilitiesOverRouter(t *testing.T) {
	srv, _ := newTestServerWithScenario(t, fake.Scenario{Name: "suggest-capabilities", Rules: []fake.Rule{{
		Match: "database migrations",
		Reply: `{"allowlist": ["shell"], "mcp_servers": [], "skills": ["db-migrate"]}`,
	}}})

	resp := postJSON(t, srv.URL+"/agents/suggest-capabilities", map[string]string{
		"prompt": "You run database migrations.",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON[struct {
		Allowlist []string `json:"allowlist"`
		Skills    []string `json:"skills"`
		Dropped   []string `json:"dropped"`
	}](t, resp)
	if len(body.Allowlist) != 1 || body.Allowlist[0] != "shell" {
		t.Fatalf("allowlist = %+v", body.Allowlist)
	}
	// "db-migrate" isn't registered: it must not come back as a suggestion.
	if len(body.Skills) != 0 || len(body.Dropped) != 1 || body.Dropped[0] != "db-migrate" {
		t.Fatalf("skills = %+v, dropped = %+v", body.Skills, body.Dropped)
	}
}

func TestRefineAgentOverRouter(t *testing.T) {
	srv, _ := newTestServerWithScenario(t, fake.Scenario{Name: "refine", Rules: []fake.Rule{{
		Match: "make it more concise",
		Reply: "You are a concise QA engineer.",
	}}})

	resp := postJSON(t, srv.URL+"/agents/refine", map[string]string{
		"prompt": "You are a QA engineer.", "instruction": "make it more concise",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON[struct {
		Result string `json:"result"`
	}](t, resp)
	if body.Result != "You are a concise QA engineer." {
		t.Fatalf("result = %q", body.Result)
	}
}
