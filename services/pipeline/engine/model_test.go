package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
)

// modelRan returns the model recorded on the sole usage.recorded event for
// stage "executor" — the fake provider reflects the model it actually
// received (OneShotRequest.Model) into Usage.Model, so this is what the
// engine resolved and handed to the provider, not just what we asked for.
func modelRan(t *testing.T, h *harness) string {
	t.Helper()
	evs := h.events(t, "usage.recorded")
	if len(evs) != 1 {
		t.Fatalf("usage.recorded events: %d, want 1: %+v", len(evs), evs)
	}
	var p struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p.Model
}

// setAgentModel sets the executor agent's registry model preference.
func setAgentModel(t *testing.T, h *harness, model string) {
	t.Helper()
	agent, err := h.data.Agent(context.Background(), "executor")
	if err != nil {
		t.Fatal(err)
	}
	agent.Preferences.Model = model
	if err := h.data.UpdateAgent(context.Background(), &agent); err != nil {
		t.Fatal(err)
	}
}

// solitaryTemplate is a single agent stage, optionally carrying a per-stage
// model override.
func solitaryTemplate(t *testing.T, model string) *template.Template {
	t.Helper()
	src := "name: solo\nstages:\n  - name: executor\n    agent: executor\n"
	if model != "" {
		src = "name: solo\nstages:\n  - name: executor\n    agent: executor\n    model: " + model + "\n"
	}
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	return tpl
}

func runWithModel(t *testing.T, h *harness, cfgModel string, tpl *template.Template) *Summary {
	t.Helper()
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second, Model: cfgModel,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// Model precedence in runAgent, most explicit wins: CLI -model (Config.Model)
// beats the template stage's model, which beats the agent registry's
// Preferences.Model, which falls back to the provider's own default when
// nothing is set anywhere.
func TestModelPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		cfgModel   string
		stageModel string
		agentModel string
		want       string
	}{
		{"cli beats stage beats agent pref", "cli-model", "stage-model", "pref-model", "cli-model"},
		{"stage beats agent pref", "", "stage-model", "pref-model", "stage-model"},
		{"agent pref wins when stage blank", "", "", "pref-model", "pref-model"},
		{"blank falls through to provider default", "", "", "", "fake-fast"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			setAgentModel(t, h, tc.agentModel)
			h.seed(contracts.Task{Title: "solo task"})
			sum := runWithModel(t, h, tc.cfgModel, solitaryTemplate(t, tc.stageModel))
			if len(sum.Done) != 1 {
				t.Fatalf("summary: %+v", sum)
			}
			if got := modelRan(t, h); got != tc.want {
				t.Errorf("model ran: %q, want %q", got, tc.want)
			}
		})
	}
}

// Only the stage carrying the override sees it — other stages resolve
// through the normal chain untouched.
func TestModelOverridesOnlyTargetedStage(t *testing.T) {
	h := newHarness(t)
	src := `name: two-stage
stages:
  - name: architect
    agent: architect
    model: claude-haiku-4-5-20251001
  - name: executor
    agent: executor
`
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	h.seed(contracts.Task{Title: "solo task"})
	sum := runWithModel(t, h, "", tpl)
	if len(sum.Done) != 1 {
		t.Fatalf("summary: %+v", sum)
	}

	byStage := map[string]string{}
	for _, ev := range h.events(t, "usage.recorded") {
		var p struct {
			Stage string `json:"stage"`
			Model string `json:"model"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		byStage[p.Stage] = p.Model
	}
	if byStage["architect"] != "claude-haiku-4-5-20251001" {
		t.Errorf("architect model: %q, want claude-haiku-4-5-20251001", byStage["architect"])
	}
	if byStage["executor"] != "sonnet" {
		t.Errorf("executor model: %q, want sonnet (its own agent pref, unaffected by architect's override)", byStage["executor"])
	}
}
