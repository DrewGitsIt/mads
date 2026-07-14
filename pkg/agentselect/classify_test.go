package agentselect_test

import (
	"testing"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name        string
		title       string
		description string
		want        contracts.Quadrant
	}{
		{
			name:  "high clarity, directing",
			title: "Implement the login rate limiter in auth/limiter.go exactly as specified",
			want:  contracts.QuadrantAutonomousExecutor,
		},
		{
			name:        "high clarity, exploring",
			title:       "Research how the payment providers handle idempotency keys",
			description: "We know exactly what we need to compare, just survey the options.",
			want:        contracts.QuadrantResearchAnalyst,
		},
		{
			name:        "low clarity, directing",
			title:       "Build the onboarding flow",
			description: "The exact shape is unclear — just make a reasonable call, no need to ask further.",
			want:        contracts.QuadrantTechnicalPartner,
		},
		{
			name:        "low clarity, exploring",
			title:       "Not sure why the login flow intermittently fails",
			description: "Can you look into it?",
			want:        contracts.QuadrantInvestigator,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := agentselect.Classify(agentselect.Request{Title: tc.title, Description: tc.description})
			if got != tc.want {
				t.Fatalf("Classify(%q, %q) = %q, want %q", tc.title, tc.description, got, tc.want)
			}
		})
	}
}
