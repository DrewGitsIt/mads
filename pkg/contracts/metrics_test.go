package contracts_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/golden"
)

func sampleCostMetrics() contracts.CostMetrics {
	day := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	return contracts.CostMetrics{
		ByDay: []contracts.CostPoint{
			{Day: day, Model: "claude-sonnet-5", InputTokens: 1200, OutputTokens: 400, CostUSD: 0.42},
		},
		BySession: []contracts.CostBySession{
			{SessionID: "ses_01hq3k", CostUSD: 0.42},
		},
		ByPipelineRun: []contracts.CostByPipelineRun{
			{SessionID: "ses_01hq3k", RunSeq: 3, CostUSD: 0.42},
		},
	}
}

func TestCostMetricsProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(sampleCostMetrics(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "cost_metrics.json", append(got, '\n'))
}

func TestCostMetricsConsumerParsesGolden(t *testing.T) {
	var got contracts.CostMetrics
	if err := json.Unmarshal(golden.Read(t, "cost_metrics.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	want := sampleCostMetrics()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("parsed cost metrics differs from expected:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func sampleAgentLeaderboardEntry() contracts.AgentLeaderboardEntry {
	return contracts.AgentLeaderboardEntry{
		Agent:       "executor",
		UsageCount:  3,
		Successes:   2,
		Failures:    1,
		SuccessRate: 0.6666666666666666,
		Retries:     1,
	}
}

func TestAgentLeaderboardEntryProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(sampleAgentLeaderboardEntry(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "agent_leaderboard_entry.json", append(got, '\n'))
}

func TestAgentLeaderboardEntryConsumerParsesGolden(t *testing.T) {
	var got contracts.AgentLeaderboardEntry
	if err := json.Unmarshal(golden.Read(t, "agent_leaderboard_entry.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	want := sampleAgentLeaderboardEntry()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("parsed agent leaderboard entry differs from expected:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func sampleTaskOutcomes() contracts.TaskOutcomes {
	return contracts.TaskOutcomes{
		Counts: map[contracts.TaskStatus]int{
			contracts.StatusDone:    2,
			contracts.StatusBlocked: 1,
			contracts.StatusReady:   1,
		},
		Total:          4,
		CompletionRate: 0.5,
	}
}

func TestTaskOutcomesProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(sampleTaskOutcomes(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "task_outcomes.json", append(got, '\n'))
}

func TestTaskOutcomesConsumerParsesGolden(t *testing.T) {
	var got contracts.TaskOutcomes
	if err := json.Unmarshal(golden.Read(t, "task_outcomes.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	want := sampleTaskOutcomes()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("parsed task outcomes differs from expected:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func sampleCapabilityUsage() contracts.CapabilityUsage {
	return contracts.CapabilityUsage{
		Capability:  "git-review",
		Type:        contracts.CapabilitySkill,
		Invocations: 0,
	}
}

func TestCapabilityUsageProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(sampleCapabilityUsage(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "capability_usage.json", append(got, '\n'))
}

func TestCapabilityUsageConsumerParsesGolden(t *testing.T) {
	var got contracts.CapabilityUsage
	if err := json.Unmarshal(golden.Read(t, "capability_usage.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	want := sampleCapabilityUsage()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("parsed capability usage differs from expected:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func samplePipelineRun() contracts.PipelineRun {
	started := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	return contracts.PipelineRun{
		SessionID:    "ses_01hq3k",
		RunSeq:       3,
		StartedAt:    started,
		EndedAt:      started.Add(10 * time.Minute),
		Outcome:      "completed",
		TasksDone:    2,
		TasksBlocked: 0,
		TasksSkipped: 0,
		CostUSD:      0.42,
		Agents:       []string{"architect", "executor"},
	}
}

func TestPipelineRunProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(samplePipelineRun(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "pipeline_run.json", append(got, '\n'))
}

func TestPipelineRunConsumerParsesGolden(t *testing.T) {
	var got contracts.PipelineRun
	if err := json.Unmarshal(golden.Read(t, "pipeline_run.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	want := samplePipelineRun()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("parsed pipeline run differs from expected:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}
