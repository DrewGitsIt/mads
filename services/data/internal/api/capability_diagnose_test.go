package api

import (
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestDiagnoseCapabilityOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t)

	var cap contracts.Capability
	resp := doJSON(t, srv, "POST", "/capabilities", contracts.Capability{
		Name: "beads", Type: contracts.CapabilitySkill,
		Config: contracts.CapabilityConfig{Path: "/nonexistent/skills/beads"},
	}, &cap)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("capability create: status %d", resp.StatusCode)
	}

	var out struct {
		Healthy     bool     `json:"healthy"`
		Reason      string   `json:"reason"`
		Suggestions []string `json:"suggestions"`
	}
	resp = doJSON(t, srv, "POST", "/capabilities/"+cap.Name+"/diagnose", nil, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diagnose: status %d", resp.StatusCode)
	}
	if out.Healthy {
		t.Errorf("want unhealthy for a nonexistent skill path, got healthy")
	}
	if len(out.Suggestions) == 0 {
		t.Errorf("want at least one suggestion for an unhealthy capability, got none")
	}

	// Resolvable by id too.
	resp = doJSON(t, srv, "POST", "/capabilities/"+cap.ID+"/diagnose", nil, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diagnose by id: status %d", resp.StatusCode)
	}

	// Unknown capability: 404.
	resp = doJSON(t, srv, "POST", "/capabilities/does-not-exist/diagnose", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown capability: status %d, want 404", resp.StatusCode)
	}
}
