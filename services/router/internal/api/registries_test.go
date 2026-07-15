package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func jsonBody(t *testing.T, v any) io.Reader {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(raw)
}

func TestAgentCRUDOverRouter(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/agents", contracts.Agent{Name: "release-notes", Prompt: "You write release notes."})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	created := decodeJSON[contracts.Agent](t, resp)
	if created.ID == "" {
		t.Fatal("create: no ID assigned")
	}

	resp, err := http.Get(srv.URL + "/agents/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status %d", resp.StatusCode)
	}
	fetched := decodeJSON[contracts.Agent](t, resp)
	if fetched.Name != "release-notes" {
		t.Fatalf("get: Name = %q, want release-notes", fetched.Name)
	}

	created.Tags = []string{"docs"}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/agents/"+created.ID, jsonBody(t, created))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/agents/"+created.ID, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/agents/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: status %d, want 404", resp.StatusCode)
	}
}

func TestCapabilityCRUDAndDiagnoseOverRouter(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/capabilities", contracts.Capability{
		Name: "beads", Type: contracts.CapabilitySkill,
		Config: contracts.CapabilityConfig{Path: "/nonexistent/skills/beads"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	created := decodeJSON[contracts.Capability](t, resp)

	resp, err := http.Get(srv.URL + "/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	list := decodeJSON[[]contracts.Capability](t, resp)
	if len(list) != 1 {
		t.Fatalf("list: len = %d, want 1", len(list))
	}

	resp, err = http.Get(srv.URL + "/capabilities/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status %d", resp.StatusCode)
	}

	created.Description = "beads skill"
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/capabilities/"+created.ID, jsonBody(t, created))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d", resp.StatusCode)
	}

	resp = postJSON(t, srv.URL+"/capabilities/"+created.ID+"/diagnose", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diagnose: status %d", resp.StatusCode)
	}
	diag := decodeJSON[struct {
		Healthy     bool     `json:"healthy"`
		Suggestions []string `json:"suggestions"`
	}](t, resp)
	if diag.Healthy {
		t.Error("want unhealthy for a nonexistent skill path")
	}
	if len(diag.Suggestions) == 0 {
		t.Error("want at least one suggestion")
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/capabilities/"+created.ID, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}
}

func TestWorkspaceCRUDOverRouter(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/workspaces", contracts.Workspace{
		Name:  "mads-core",
		Repos: []contracts.WorkspaceRepo{{Path: "/repo", Primary: true}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	created := decodeJSON[contracts.Workspace](t, resp)

	resp, err := http.Get(srv.URL + "/workspaces/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status %d", resp.StatusCode)
	}

	created.Description = "core repo"
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/workspaces/"+created.ID, jsonBody(t, created))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/workspaces/"+created.ID, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}
}

func TestTemplateCRUDAndValidateOverRouter(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/templates", contracts.Template{
		Name:   "nightly",
		Source: "name: nightly\nstages:\n  - name: verify\n    command: make test\n",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	created := decodeJSON[contracts.Template](t, resp)

	resp, err := http.Get(srv.URL + "/templates")
	if err != nil {
		t.Fatal(err)
	}
	list := decodeJSON[[]contracts.Template](t, resp)
	found := false
	for _, tpl := range list {
		found = found || tpl.Name == "nightly"
	}
	if !found {
		t.Fatalf("list: %+v, want to contain 'nightly'", list)
	}

	resp, err = http.Get(srv.URL + "/templates/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: status %d", resp.StatusCode)
	}
	got := decodeJSON[struct {
		Template contracts.Template `json:"template"`
		UsedBy   []string           `json:"used_by"`
	}](t, resp)
	if got.Template.Name != "nightly" {
		t.Fatalf("get: Name = %q, want nightly", got.Template.Name)
	}
	if got.UsedBy == nil {
		t.Error("used_by: want an (empty) slice, got nil")
	}

	// Validate: valid source passes.
	resp = postJSON(t, srv.URL+"/templates/validate", map[string]string{
		"source": "name: nightly\nstages:\n  - name: verify\n    command: make test\n",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate valid source: status %d", resp.StatusCode)
	}

	// Validate: a template with multiple seeded errors reports them all
	// together — duplicate stage name, unknown agent, forward loop_back.
	resp = postJSON(t, srv.URL+"/templates/validate", map[string]string{
		"source": "name: bad\nstages:\n" +
			"  - name: dup\n    agent: no-such-agent\n" +
			"  - name: dup\n    command: make test\n    on_fail:\n      loop_back: later\n" +
			"  - name: later\n    command: make test\n",
	})
	// The Router flattens validation failures to 400 regardless of source
	// (its own decoding or a proxied dataclient call) — see writeError.
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("validate invalid source: status %d, want 400", resp.StatusCode)
	}
	verr := decodeJSON[contracts.ValidationError](t, resp)
	if len(verr.Errors) < 3 {
		t.Fatalf("want at least 3 errors reported together, got %d: %+v", len(verr.Errors), verr.Errors)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/templates/"+created.ID, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}
}
