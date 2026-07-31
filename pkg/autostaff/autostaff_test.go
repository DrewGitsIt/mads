package autostaff_test

import (
	"context"
	"testing"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/autostaff"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func setup(t *testing.T) *dataclient.Client {
	t.Helper()
	base := datatest.Start(t)
	return dataclient.New(base, "autostaff")
}

// A surviving candidate is selected — no generation involved.
func TestSelectOrGenerateSelectsExistingCandidate(t *testing.T) {
	c := setup(t)
	bot := contracts.Agent{Name: "investigator-bot", Prompt: "You investigate things.", Quadrant: contracts.QuadrantInvestigator}
	if err := c.CreateAgent(context.Background(), &bot); err != nil {
		t.Fatal(err)
	}
	req := agentselect.BuildRequest("tsk_1", "sess_1", "impl", "", "investigate something", nil, nil, nil)
	name, agent, err := autostaff.SelectOrGenerate(context.Background(), c, nil, req)
	if err != nil {
		t.Fatalf("SelectOrGenerate: %v", err)
	}
	if name != "investigator-bot" || agent.Name != "investigator-bot" {
		t.Errorf("name = %q, agent = %+v, want investigator-bot", name, agent)
	}
}

// No candidate survives and no Fast Model is configured: a clear failure,
// never a silent fallback to a hardcoded agent.
func TestSelectOrGenerateNoCandidateNoFastModelFails(t *testing.T) {
	c := setup(t)
	req := agentselect.BuildRequest("tsk_1", "sess_1", "impl", "investigate something", "", nil, nil, nil)
	_, _, err := autostaff.SelectOrGenerate(context.Background(), c, nil, req)
	if err == nil {
		t.Fatal("SelectOrGenerate: want error when no candidate and no Fast Model")
	}
}

// No candidate survives but a Fast Model is configured: generation kicks in
// automatically ("that's what auto means") and the draft is registered.
func TestSelectOrGenerateGeneratesWhenNoCandidate(t *testing.T) {
	c := setup(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "generate", Rules: []fake.Rule{{
		Match: "Generate an agent definition for this task",
		Reply: `{"name":"generated-investigator","description":"Generated for this task",` +
			`"prompt":"You investigate things.","quadrant":"investigator","tags":[],` +
			`"capabilities":{"tools":[],"mcp_servers":[],"skills":[]},"permissions":{}}`,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	fm := fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
	req := agentselect.BuildRequest("tsk_1", "sess_1", "impl", "investigate something", "", nil, nil, nil)
	name, agent, err := autostaff.SelectOrGenerate(context.Background(), c, fm, req)
	if err != nil {
		t.Fatalf("SelectOrGenerate: %v", err)
	}
	if name != "generated-investigator" {
		t.Errorf("name = %q, want generated-investigator", name)
	}
	if _, aerr := c.Agent(context.Background(), "generated-investigator"); aerr != nil {
		t.Errorf("generated agent was never registered: %v", aerr)
	}
	if agent.Quadrant != contracts.QuadrantInvestigator {
		t.Errorf("quadrant = %s", agent.Quadrant)
	}
}
