package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
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

// TestTemplateVersionStamping covers W1.5's audit trail (spec §6): the
// seeded default starts at version 1, and each subsequent save bumps it by
// exactly one — off the last-stored record, not off whatever the client's
// PUT body happened to carry.
func TestTemplateVersionStamping(t *testing.T) {
	srv, _ := newTestServer(t)

	var list []contracts.Template
	doJSON(t, srv, "GET", "/templates", nil, &list)
	if len(list) != 1 || list[0].Version != 1 {
		t.Fatalf("fresh install: want the seeded default at version 1, got %+v", list)
	}

	tpl := list[0]
	tpl.Description = "first edit"
	var afterFirst contracts.Template
	if resp := doJSON(t, srv, "PUT", "/templates/"+tpl.Name, tpl, &afterFirst); resp.StatusCode != 200 {
		t.Fatalf("first edit: status %d", resp.StatusCode)
	}
	if afterFirst.Version != 2 {
		t.Fatalf("first edit: version = %d, want 2", afterFirst.Version)
	}

	afterFirst.Description = "second edit"
	var afterSecond contracts.Template
	if resp := doJSON(t, srv, "PUT", "/templates/"+tpl.Name, afterFirst, &afterSecond); resp.StatusCode != 200 {
		t.Fatalf("second edit: status %d", resp.StatusCode)
	}
	if afterSecond.Version != 3 {
		t.Fatalf("second edit: version = %d, want 3", afterSecond.Version)
	}
}

// TestTemplateCreateForcesVersion1 guards against a client-supplied Version
// (a stale copy, a duplicated export, or just a client bug) seeding the
// audit trail at anything other than 1 — the same reasoning that already
// forces ID/System to their defaults on create.
func TestTemplateCreateForcesVersion1(t *testing.T) {
	srv, _ := newTestServer(t)

	tpl := contracts.Template{
		Name:    "sneaky",
		Version: 99,
		Source: "name: sneaky\nstages:\n" +
			"  - name: executor\n    agent: executor\n",
	}
	var created contracts.Template
	if resp := doJSON(t, srv, "POST", "/templates", tpl, &created); resp.StatusCode != 201 {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	if created.Version != 1 {
		t.Fatalf("create: version = %d, want 1 regardless of client-supplied value", created.Version)
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

// TestValidateAllReportsEachStoredTemplate covers the discovery surface for
// stored templates a grammar tightening leaves behind: a template that
// fails semantic validation (here, an unresolvable agent reference) can
// still land in the store directly at the Registry layer, which only
// guarantees shape (see store.NewTemplates's doc) — the same route a
// template saved under a since-tightened pkg/template grammar would have
// taken. validate-all must still surface it, distinctly from the seeded,
// valid default.
func TestValidateAllReportsEachStoredTemplate(t *testing.T) {
	srv, db := newTestServer(t)

	legacy := contracts.Template{
		Name: "legacy",
		Source: "name: legacy\nstages:\n" +
			"  - name: build\n    agent: ghost-agent\n",
	}
	templates := store.NewTemplates(store.NewLog(db))
	if err := templates.Create(context.Background(), &legacy, "test"); err != nil {
		t.Fatalf("seed legacy template directly at the store layer: %v", err)
	}

	var out struct {
		Results []TemplateValidation `json:"results"`
	}
	resp := doJSON(t, srv, "POST", "/templates/validate-all", nil, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 even with an invalid template present", resp.StatusCode)
	}
	if len(out.Results) != 2 {
		t.Fatalf("want one row per stored template (default + legacy), got %+v", out.Results)
	}

	var gotDefault, gotLegacy *TemplateValidation
	for i := range out.Results {
		switch out.Results[i].Name {
		case "default-implementation":
			gotDefault = &out.Results[i]
		case "legacy":
			gotLegacy = &out.Results[i]
		}
	}
	if gotDefault == nil || !gotDefault.Valid || len(gotDefault.Errors) != 0 {
		t.Errorf("seeded default: want valid with no errors, got %+v", gotDefault)
	}
	if gotLegacy == nil || gotLegacy.Valid || len(gotLegacy.Errors) == 0 {
		t.Fatalf("legacy: want invalid with errors, got %+v", gotLegacy)
	}
	if gotLegacy.ID != legacy.ID {
		t.Errorf("legacy row id = %q, want %q", gotLegacy.ID, legacy.ID)
	}
	if !strings.Contains(gotLegacy.Errors[0].Message, "ghost-agent") &&
		!strings.Contains(gotLegacy.Errors[0].Message, "unknown agent") {
		t.Errorf("legacy error should explain the unresolved agent, got %+v", gotLegacy.Errors)
	}
}
