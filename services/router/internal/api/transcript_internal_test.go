package api

import (
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// TestNormalizeTurnsCarriesEnrichment: the dormant fallback (and the
// pipeline drill-in) must serve the same enriched, status-stamped tool rows
// the live child does — kind/title/locations/server survive the mapping and
// the T5 rule engine resolves recorded results (found missing in the T18
// live pass: dormant transcripts came back stripped).
func TestNormalizeTurnsCarriesEnrichment(t *testing.T) {
	msgs := []provider.Message{
		{Role: "tool", Tool: &provider.ToolUse{
			ID: "t1", Name: "Edit", Path: "/repo/a.go",
			Kind: provider.ToolKindEdit, Title: "/repo/a.go",
			Locations: []provider.ToolLocation{{Path: "/repo/a.go", Line: 3}},
		}},
		{Role: "tool_result", Tool: &provider.ToolUse{ID: "t1"}},
		{Role: "tool", Tool: &provider.ToolUse{
			ID: "t2", Name: "mcp__gh__search", Kind: provider.ToolKindMCP,
			Server: "gh", Title: "gh:search",
		}},
	}
	out := normalizeTurns(msgs, nil)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	edit := out[0].Tool
	if edit.Kind != "edit" || edit.Title != "/repo/a.go" || edit.Path != "/repo/a.go" {
		t.Errorf("edit enrichment lost: %+v", edit)
	}
	if len(edit.Locations) != 1 || edit.Locations[0].Line != 3 {
		t.Errorf("edit locations lost: %+v", edit.Locations)
	}
	if edit.Status != "completed" {
		t.Errorf("edit status = %q, want completed (stamped from its result)", edit.Status)
	}
	mcp := out[2].Tool
	if mcp.Kind != "mcp" || mcp.Server != "gh" {
		t.Errorf("mcp enrichment lost: %+v", mcp)
	}
	if mcp.Status != "in_progress" {
		t.Errorf("open call status = %q, want in_progress (honestly open)", mcp.Status)
	}
}
