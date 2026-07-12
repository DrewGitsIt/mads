package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestDefaultTemplateSeededAndValid(t *testing.T) {
	srv, _ := newTestServer(t)

	var list []contracts.Template
	doJSON(t, srv, "GET", "/templates", nil, &list)
	if len(list) != 1 || list[0].Name != "default-implementation" || !list[0].System {
		t.Fatalf("fresh install: want the system default template, got %+v", list)
	}

	// The seed must pass its own validator — its agents resolve against the
	// seeded system agents.
	var ok map[string]bool
	resp := doJSON(t, srv, "POST", "/templates/validate",
		map[string]string{"source": list[0].Source}, &ok)
	if resp.StatusCode != 200 || !ok["valid"] {
		t.Fatalf("seeded default template does not validate: status %d", resp.StatusCode)
	}
}

func TestValidateReportsLaunchChecksTogether(t *testing.T) {
	srv, _ := newTestServer(t)
	src := `
name: broken
stages:
  - name: plan
    agent: ghost-agent
  - name: build
    command: make build
    on_fail: { loop_back: nowhere, max_loops: 2 }
`
	var verr contracts.ValidationError
	resp := doJSON(t, srv, "POST", "/templates/validate", map[string]string{"source": src}, &verr)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", resp.StatusCode)
	}
	all := verr.Error()
	if !strings.Contains(all, "unknown agent") || !strings.Contains(all, "does not exist") {
		t.Errorf("want agent + loop-target failures together (M9), got: %s", all)
	}
}

func TestTemplateSaveValidatesAndAnnounces(t *testing.T) {
	srv, _ := newTestServer(t)

	// Invalid source is refused at save, not just at the validate endpoint.
	bad := contracts.Template{Name: "docs-only", Source: "name: docs-only\nstages: []\n"}
	if resp := doJSON(t, srv, "POST", "/templates", bad, nil); resp.StatusCode != 422 {
		t.Fatalf("empty-stages template saved: status %d", resp.StatusCode)
	}

	good := contracts.Template{
		Name: "docs-only",
		Source: "name: docs-only\nstages:\n" +
			"  - name: executor\n    agent: executor\n" +
			"  - name: code_review\n    agent: code-reviewer\n",
	}
	var created contracts.Template
	if resp := doJSON(t, srv, "POST", "/templates", good, &created); resp.StatusCode != 201 {
		t.Fatalf("valid template refused: status %d", resp.StatusCode)
	}

	// Edit via PUT by name; system flag survives on the default.
	created.Description = "For documentation tasks"
	if resp := doJSON(t, srv, "PUT", "/templates/docs-only", created, nil); resp.StatusCode != 200 {
		t.Fatalf("update: status %d", resp.StatusCode)
	}

	var events []contracts.Envelope
	doJSON(t, srv, "GET", "/events?types=template.*", nil, &events)
	if len(events) != 2 {
		t.Errorf("want template.created + template.updated, got %d", len(events))
	}
}

func TestTemplateDeleteGuards(t *testing.T) {
	srv, _ := newTestServer(t)

	// The system default refuses deletion.
	if resp := doJSON(t, srv, "DELETE", "/templates/default-implementation", nil, nil); resp.StatusCode != 422 {
		t.Errorf("system template delete: status %d, want 422", resp.StatusCode)
	}

	// A template bound to a workspace refuses deletion and names the binder.
	tpl := contracts.Template{
		Name:   "nightly",
		Source: "name: nightly\nstages:\n  - name: verify\n    command: make test\n",
	}
	doJSON(t, srv, "POST", "/templates", tpl, nil)
	doJSON(t, srv, "POST", "/workspaces", contracts.Workspace{
		Name: "mads-core", Defaults: contracts.WorkspaceDefaults{Template: "nightly"},
	}, nil)

	var verr contracts.ValidationError
	if resp := doJSON(t, srv, "DELETE", "/templates/nightly", nil, &verr); resp.StatusCode != 422 {
		t.Fatalf("bound template delete: status %d, want 422", resp.StatusCode)
	}
	if !strings.Contains(verr.Error(), "mads-core") {
		t.Errorf("refusal should name the binding workspace: %v", verr)
	}

	// used_by shows on GET.
	var out struct {
		UsedBy []string `json:"used_by"`
	}
	doJSON(t, srv, "GET", "/templates/nightly", nil, &out)
	if len(out.UsedBy) != 1 || out.UsedBy[0] != "mads-core" {
		t.Errorf("used_by: %v", out.UsedBy)
	}
}
