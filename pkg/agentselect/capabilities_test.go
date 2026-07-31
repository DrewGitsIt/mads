package agentselect_test

import (
	"reflect"
	"testing"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestEffectiveCapabilities(t *testing.T) {
	cases := []struct {
		name      string
		task      contracts.Task
		workspace contracts.Workspace
		want      []string
	}{
		{
			name: "both empty",
			want: nil,
		},
		{
			name:      "workspace only",
			workspace: contracts.Workspace{Capabilities: []string{"gitlab-access"}},
			want:      []string{"gitlab-access"},
		},
		{
			name: "task only",
			task: contracts.Task{RequiredCapabilities: []string{"playwright"}},
			want: []string{"playwright"},
		},
		{
			name:      "union of both, distinct",
			task:      contracts.Task{RequiredCapabilities: []string{"playwright"}},
			workspace: contracts.Workspace{Capabilities: []string{"gitlab-access"}},
			want:      []string{"gitlab-access", "playwright"},
		},
		{
			name:      "overlap deduplicated, workspace order preferred",
			task:      contracts.Task{RequiredCapabilities: []string{"gitlab-access", "playwright"}},
			workspace: contracts.Workspace{Capabilities: []string{"gitlab-access"}},
			want:      []string{"gitlab-access", "playwright"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := agentselect.EffectiveCapabilities(c.task, c.workspace)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("EffectiveCapabilities() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestExpandCapabilities(t *testing.T) {
	registry := []contracts.Capability{
		{Name: "playwright", Type: contracts.CapabilitySkill},
		{Name: "gitlab-access", Type: contracts.CapabilityMCP},
		{Name: "jira", Type: contracts.CapabilityMCP},
	}

	t.Run("empty names", func(t *testing.T) {
		tools, mcp, skills := agentselect.ExpandCapabilities(nil, registry)
		if tools != nil || mcp != nil || skills != nil {
			t.Errorf("empty input should expand to nothing, got tools=%v mcp=%v skills=%v", tools, mcp, skills)
		}
	})

	t.Run("empty registry", func(t *testing.T) {
		tools, mcp, skills := agentselect.ExpandCapabilities([]string{"playwright"}, nil)
		if tools != nil || mcp != nil || skills != nil {
			t.Errorf("empty registry should expand to nothing, got tools=%v mcp=%v skills=%v", tools, mcp, skills)
		}
	})

	t.Run("mixed skill and mcp, grouped by type", func(t *testing.T) {
		tools, mcp, skills := agentselect.ExpandCapabilities([]string{"playwright", "gitlab-access", "jira"}, registry)
		if tools != nil {
			t.Errorf("no capability type maps to tools, got %v", tools)
		}
		if !reflect.DeepEqual(mcp, []string{"gitlab-access", "jira"}) {
			t.Errorf("mcp = %v, want [gitlab-access jira]", mcp)
		}
		if !reflect.DeepEqual(skills, []string{"playwright"}) {
			t.Errorf("skills = %v, want [playwright]", skills)
		}
	})

	t.Run("unknown name dropped, not errored", func(t *testing.T) {
		_, mcp, skills := agentselect.ExpandCapabilities([]string{"playwright", "ghost-capability"}, registry)
		if len(mcp) != 0 || !reflect.DeepEqual(skills, []string{"playwright"}) {
			t.Errorf("unknown name should be silently dropped: mcp=%v skills=%v", mcp, skills)
		}
	})
}
