package agentselect_test

import (
	"testing"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestSimilarityIdentical(t *testing.T) {
	a := contracts.Agent{
		ID: "agt_a", Name: "db-migrator", Description: "Handles postgres database migrations",
		Tags: []string{"postgres", "migrations"}, Quadrant: contracts.QuadrantAutonomousExecutor,
		Capabilities: contracts.AgentCapabilities{Tools: []string{"shell", "sql"}, MCPServers: []string{"postgres"}},
	}
	b := a
	b.ID = "agt_b"
	if got := agentselect.Similarity(a, b); got != 1.0 {
		t.Fatalf("identical agents: got %v, want 1.0", got)
	}
}

func TestSimilarityDisjoint(t *testing.T) {
	a := contracts.Agent{
		Description: "Handles frontend react components", Tags: []string{"react", "frontend"},
		Quadrant:     contracts.QuadrantResearchAnalyst,
		Capabilities: contracts.AgentCapabilities{Tools: []string{"browser"}},
	}
	b := contracts.Agent{
		Description: "Manages kubernetes cluster deployments", Tags: []string{"kubernetes", "devops"},
		Quadrant:     contracts.QuadrantInvestigator,
		Capabilities: contracts.AgentCapabilities{Tools: []string{"kubectl"}},
	}
	if got := agentselect.Similarity(a, b); got != 0 {
		t.Fatalf("fully disjoint agents: got %v, want 0", got)
	}
}

// TestSimilarityPartialOverlap hand-computes the expected score from the
// documented formula so the test breaks if the weighting silently drifts:
//
//	textTokens(a)  = {postgres, migrations, handles, database}            (4)
//	textTokens(b)  = {postgres, migrations, schema, handles, database, and, changes} (7)
//	intersection = {postgres, migrations, handles, database} = 4, union = 7
//	textScore = 4/7
//
//	capTokens(a) = capTokens(b) = {shell, sql, postgres} → capScore = 1
//
//	same quadrant, both non-empty → quadrantBonus = 0.15
//	score = (4/7 + 1)/2 + 0.15 = 0.9357142857142857
func TestSimilarityPartialOverlap(t *testing.T) {
	a := contracts.Agent{
		Description: "Handles postgres database migrations", Tags: []string{"postgres", "migrations"},
		Quadrant:     contracts.QuadrantAutonomousExecutor,
		Capabilities: contracts.AgentCapabilities{Tools: []string{"shell", "sql"}, MCPServers: []string{"postgres"}},
	}
	b := contracts.Agent{
		Description: "Handles postgres database migrations and schema changes",
		Tags:        []string{"postgres", "migrations", "schema"},
		Quadrant:    contracts.QuadrantAutonomousExecutor,
		Capabilities: contracts.AgentCapabilities{
			Tools: []string{"shell", "sql"}, MCPServers: []string{"postgres"},
		},
	}
	want := (4.0/7.0+1.0)/2 + 0.15
	if got := agentselect.Similarity(a, b); got != want {
		t.Fatalf("partial overlap: got %v, want %v", got, want)
	}
}

func TestFindSimilar(t *testing.T) {
	seeded := contracts.Agent{
		ID: "agt_seed", Name: "db-migrator", Description: "Handles postgres database migrations",
		Tags: []string{"postgres", "migrations"}, Quadrant: contracts.QuadrantAutonomousExecutor,
		Capabilities: contracts.AgentCapabilities{Tools: []string{"shell", "sql"}, MCPServers: []string{"postgres"}},
	}
	existing := []contracts.Agent{seeded}

	t.Run("above threshold matches", func(t *testing.T) {
		candidate := contracts.Agent{
			Description: "Handles postgres database migrations and schema changes",
			Tags:        []string{"postgres", "migrations", "schema"},
			Quadrant:    contracts.QuadrantAutonomousExecutor,
			Capabilities: contracts.AgentCapabilities{
				Tools: []string{"shell", "sql"}, MCPServers: []string{"postgres"},
			},
		}
		got := agentselect.FindSimilar(candidate, existing, 0.6)
		if got == nil || got.Agent != "db-migrator" {
			t.Fatalf("got %+v, want match on db-migrator", got)
		}
	})

	t.Run("below threshold returns nil", func(t *testing.T) {
		candidate := contracts.Agent{
			Description: "Manages kubernetes cluster deployments", Tags: []string{"kubernetes", "devops"},
			Quadrant:     contracts.QuadrantInvestigator,
			Capabilities: contracts.AgentCapabilities{Tools: []string{"kubectl"}},
		}
		if got := agentselect.FindSimilar(candidate, existing, 0.6); got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})

	t.Run("excludes self by ID", func(t *testing.T) {
		got := agentselect.FindSimilar(seeded, existing, 0.6)
		if got != nil {
			t.Fatalf("got %+v, want nil (self excluded)", got)
		}
	})
}
