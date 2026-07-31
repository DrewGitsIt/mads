package agentselect_test

import (
	"reflect"
	"testing"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
)

// BuildRequest is the shared population path pipeline agent:auto stages and
// interactive session-start agent auto both go through — capability names
// are expanded against the live registry into the Request's
// RequiredTools/MCPServers/Skills slices exactly like ExpandCapabilities.
func TestBuildRequest(t *testing.T) {
	registry := []contracts.Capability{
		{Name: "playwright", Type: contracts.CapabilitySkill},
		{Name: "github", Type: contracts.CapabilityMCP},
	}
	req := agentselect.BuildRequest("tsk_1", "sess_1", "impl", "Fix the login bug", "It flakes in CI",
		[]string{"bug"}, []string{"playwright", "github", "ghost"}, registry)

	want := agentselect.Request{
		TaskID: "tsk_1", SessionID: "sess_1", Stage: "impl",
		Title: "Fix the login bug", Description: "It flakes in CI", Labels: []string{"bug"},
		RequiredMCPServers: []string{"github"}, RequiredSkills: []string{"playwright"},
	}
	if !reflect.DeepEqual(req, want) {
		t.Errorf("BuildRequest() = %+v, want %+v", req, want)
	}
}

// A session-start caller with no task — just a free-text opening prompt —
// populates Title empty and Description as the prompt text.
func TestBuildRequestFromOpeningPrompt(t *testing.T) {
	req := agentselect.BuildRequest("", "sess_1", "", "", "help me debug this flaky test", nil, nil, nil)
	if req.Title != "" || req.Description != "help me debug this flaky test" {
		t.Errorf("BuildRequest() = %+v", req)
	}
}
