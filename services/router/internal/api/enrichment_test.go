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
		Reply: `{"tools": ["psql"], "mcp_servers": [], "skills": ["db-migrate"]}`,
	}}})

	resp := postJSON(t, srv.URL+"/agents/suggest-capabilities", map[string]string{
		"prompt": "You run database migrations.",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeJSON[struct {
		Tools      []string `json:"tools"`
		MCPServers []string `json:"mcp_servers"`
		Skills     []string `json:"skills"`
	}](t, resp)
	if len(body.Tools) != 1 || body.Tools[0] != "psql" {
		t.Fatalf("tools = %+v", body.Tools)
	}
	if len(body.Skills) != 1 || body.Skills[0] != "db-migrate" {
		t.Fatalf("skills = %+v", body.Skills)
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
