package dataclient_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestMetricsCost seeds usage.recorded events across two sessions, two
// models, and two days, plus one pipeline run wrapping a third usage event,
// and checks all three CostMetrics slices.
func TestMetricsCost(t *testing.T) {
	base := datatest.Start(t)
	ctx := context.Background()
	c := dataclient.New(base, "pipeline")

	sess1 := contracts.Session{Name: "s1"}
	sess2 := contracts.Session{Name: "s2"}
	if err := c.CreateSession(ctx, &sess1); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateSession(ctx, &sess2); err != nil {
		t.Fatal(err)
	}

	day1 := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	appendUsage := func(sessionID string, ts time.Time, model string, in, out int64, cost float64) {
		env := contracts.Envelope{
			Type: contracts.UsageRecorded, SessionID: sessionID, Timestamp: ts,
			Payload: mustJSON(t, map[string]any{
				"model": model, "input_tokens": in, "output_tokens": out, "cost_usd": cost,
			}),
		}
		if err := c.Append(ctx, &env); err != nil {
			t.Fatal(err)
		}
	}

	appendUsage(sess1.ID, day1, "model-a", 100, 50, 0.10)
	appendUsage(sess1.ID, day2, "model-a", 200, 100, 0.20)
	appendUsage(sess2.ID, day1, "model-b", 300, 150, 0.30)

	started := contracts.Envelope{Type: contracts.PipelineStarted, SessionID: sess2.ID, Timestamp: day2.Add(time.Hour)}
	if err := c.Append(ctx, &started); err != nil {
		t.Fatal(err)
	}
	appendUsage(sess2.ID, day2.Add(2*time.Hour), "model-a", 50, 25, 0.05)
	completed := contracts.Envelope{
		Type: contracts.PipelineCompleted, SessionID: sess2.ID, Timestamp: day2.Add(3 * time.Hour),
		Payload: mustJSON(t, map[string]any{"outcome": "completed", "done": 1, "blocked": 0, "skipped": 0}),
	}
	if err := c.Append(ctx, &completed); err != nil {
		t.Fatal(err)
	}

	got, err := c.MetricsCost(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(got.ByDay) != 3 {
		t.Fatalf("by_day: want 3 buckets, got %d: %+v", len(got.ByDay), got.ByDay)
	}
	byDayKey := func(day time.Time, model string) contracts.CostPoint {
		for _, p := range got.ByDay {
			if p.Day.Equal(day) && p.Model == model {
				return p
			}
		}
		t.Fatalf("missing by_day bucket for %v/%s in %+v", day, model, got.ByDay)
		return contracts.CostPoint{}
	}
	d1a := byDayKey(day1.Truncate(24*time.Hour), "model-a")
	if d1a.InputTokens != 100 || d1a.OutputTokens != 50 || math.Abs(d1a.CostUSD-0.10) > 1e-9 {
		t.Errorf("day1/model-a: %+v", d1a)
	}
	d1b := byDayKey(day1.Truncate(24*time.Hour), "model-b")
	if d1b.InputTokens != 300 || d1b.OutputTokens != 150 || math.Abs(d1b.CostUSD-0.30) > 1e-9 {
		t.Errorf("day1/model-b: %+v", d1b)
	}
	d2a := byDayKey(day2.Truncate(24*time.Hour), "model-a")
	if d2a.InputTokens != 250 || d2a.OutputTokens != 125 || math.Abs(d2a.CostUSD-0.25) > 1e-9 {
		t.Errorf("day2/model-a: %+v", d2a)
	}

	if len(got.BySession) != 2 {
		t.Fatalf("by_session: want 2 entries, got %d: %+v", len(got.BySession), got.BySession)
	}
	bySession := map[string]float64{}
	for _, s := range got.BySession {
		bySession[s.SessionID] = s.CostUSD
	}
	if math.Abs(bySession[sess1.ID]-0.30) > 1e-9 {
		t.Errorf("sess1 total = %v, want 0.30", bySession[sess1.ID])
	}
	if math.Abs(bySession[sess2.ID]-0.35) > 1e-9 {
		t.Errorf("sess2 total = %v, want 0.35", bySession[sess2.ID])
	}

	if len(got.ByPipelineRun) != 1 {
		t.Fatalf("by_pipeline_run: want 1 entry, got %d: %+v", len(got.ByPipelineRun), got.ByPipelineRun)
	}
	run := got.ByPipelineRun[0]
	if run.SessionID != sess2.ID || run.RunSeq != started.Seq || math.Abs(run.CostUSD-0.05) > 1e-9 {
		t.Errorf("by_pipeline_run entry = %+v, want session %s run_seq %d cost 0.05", run, sess2.ID, started.Seq)
	}
}

// TestMetricsAgentLeaderboard seeds agent.selected and agent.outcome_recorded
// events for two agents and checks usage counts, success rate, and retries.
func TestMetricsAgentLeaderboard(t *testing.T) {
	base := datatest.Start(t)
	ctx := context.Background()
	c := dataclient.New(base, "pipeline")

	sess := contracts.Session{Name: "leaderboard"}
	if err := c.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}

	appendAgent := func(evType contracts.EventType, payload map[string]any) {
		env := contracts.Envelope{Type: evType, SessionID: sess.ID, Payload: mustJSON(t, payload)}
		if err := c.Append(ctx, &env); err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < 3; i++ {
		appendAgent(contracts.AgentSelected, map[string]any{"agent": "executor"})
	}
	appendAgent(contracts.AgentSelected, map[string]any{"agent": "architect"})

	appendAgent(contracts.AgentOutcomeRecorded, map[string]any{"agent": "executor", "outcome": "success"})
	appendAgent(contracts.AgentOutcomeRecorded, map[string]any{"agent": "executor", "outcome": "success"})
	appendAgent(contracts.AgentOutcomeRecorded, map[string]any{"agent": "executor", "outcome": "failure"})
	appendAgent(contracts.AgentOutcomeRecorded, map[string]any{"agent": "architect", "outcome": "success"})

	got, err := c.MetricsAgentLeaderboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(got), got)
	}
	byAgent := map[string]contracts.AgentLeaderboardEntry{}
	for _, e := range got {
		byAgent[e.Agent] = e
	}

	exec := byAgent["executor"]
	if exec.UsageCount != 3 || exec.Successes != 2 || exec.Failures != 1 || exec.Retries != exec.Failures {
		t.Errorf("executor entry: %+v", exec)
	}
	if math.Abs(exec.SuccessRate-2.0/3.0) > 1e-9 {
		t.Errorf("executor success rate = %v, want %v", exec.SuccessRate, 2.0/3.0)
	}

	arch := byAgent["architect"]
	if arch.UsageCount != 1 || arch.Successes != 1 || arch.Failures != 0 || arch.Retries != arch.Failures {
		t.Errorf("architect entry: %+v", arch)
	}
	if math.Abs(arch.SuccessRate-1.0) > 1e-9 {
		t.Errorf("architect success rate = %v, want 1.0", arch.SuccessRate)
	}
}

// TestMetricsTaskOutcomes creates confirmed tasks in several statuses plus a
// draft, and checks the status distribution excludes the draft.
func TestMetricsTaskOutcomes(t *testing.T) {
	base := datatest.Start(t)
	ctx := context.Background()
	c := dataclient.New(base, "user")

	sess := contracts.Session{Name: "outcomes"}
	if err := c.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}

	mkTask := func(status contracts.TaskStatus) {
		task := contracts.Task{Title: "task", SessionID: sess.ID, Source: "user"}
		if err := c.CreateTask(ctx, &task); err != nil {
			t.Fatal(err)
		}
		if status != contracts.StatusReady {
			task.Status = status
			if err := c.UpdateTask(ctx, &task); err != nil {
				t.Fatal(err)
			}
		}
	}
	mkTask(contracts.StatusDone)
	mkTask(contracts.StatusBlocked)
	mkTask(contracts.StatusWontDo)
	mkTask(contracts.StatusReady)

	draft := contracts.Task{Title: "draft", SessionID: sess.ID, Source: "synthesizer", Draft: true}
	if err := c.CreateTask(ctx, &draft); err != nil {
		t.Fatal(err)
	}

	got, err := c.MetricsTaskOutcomes(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 4 {
		t.Fatalf("total = %d, want 4 (draft excluded): %+v", got.Total, got)
	}
	want := map[contracts.TaskStatus]int{
		contracts.StatusDone: 1, contracts.StatusBlocked: 1,
		contracts.StatusWontDo: 1, contracts.StatusReady: 1,
	}
	for status, n := range want {
		if got.Counts[status] != n {
			t.Errorf("counts[%s] = %d, want %d (all counts: %+v)", status, got.Counts[status], n, got.Counts)
		}
	}
	if math.Abs(got.CompletionRate-0.25) > 1e-9 {
		t.Errorf("completion rate = %v, want 0.25", got.CompletionRate)
	}
}

// TestMetricsCapabilityUsage creates two capabilities, invokes only one, and
// checks the never-used one still shows up zero-filled.
func TestMetricsCapabilityUsage(t *testing.T) {
	base := datatest.Start(t)
	ctx := context.Background()
	c := dataclient.New(base, "user")

	used := contracts.Capability{
		Name: "used-skill", Type: contracts.CapabilitySkill,
		Config: contracts.CapabilityConfig{Path: "/skills/used"},
	}
	unused := contracts.Capability{
		Name: "unused-skill", Type: contracts.CapabilitySkill,
		Config: contracts.CapabilityConfig{Path: "/skills/unused"},
	}
	if err := c.CreateCapability(ctx, &used); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateCapability(ctx, &unused); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		env := contracts.Envelope{
			Type: contracts.CapabilityInvoked, Actor: "session",
			Payload: mustJSON(t, map[string]any{"capability": used.Name, "type": "skill"}),
		}
		if err := c.Append(ctx, &env); err != nil {
			t.Fatal(err)
		}
	}

	got, err := c.MetricsCapabilityUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]contracts.CapabilityUsage{}
	for _, u := range got {
		byName[u.Capability] = u
	}
	if byName[used.Name].Invocations != 3 {
		t.Errorf("used capability invocations = %d, want 3", byName[used.Name].Invocations)
	}
	if byName[unused.Name].Invocations != 0 {
		t.Errorf("unused capability invocations = %d, want 0", byName[unused.Name].Invocations)
	}
}

// TestMetricsPipelineRuns seeds two pipeline runs in one session — one
// completed with usage and an agent selection inside it, one cancelled — and
// checks both come back in order with the right fields.
func TestMetricsPipelineRuns(t *testing.T) {
	base := datatest.Start(t)
	ctx := context.Background()
	c := dataclient.New(base, "pipeline")

	sess := contracts.Session{Name: "runs"}
	if err := c.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)

	run1Start := contracts.Envelope{Type: contracts.PipelineStarted, SessionID: sess.ID, Timestamp: t0}
	if err := c.Append(ctx, &run1Start); err != nil {
		t.Fatal(err)
	}
	agentSel := contracts.Envelope{
		Type: contracts.AgentSelected, SessionID: sess.ID, Timestamp: t0.Add(time.Minute),
		Payload: mustJSON(t, map[string]any{"agent": "executor"}),
	}
	if err := c.Append(ctx, &agentSel); err != nil {
		t.Fatal(err)
	}
	usage := contracts.Envelope{
		Type: contracts.UsageRecorded, SessionID: sess.ID, Timestamp: t0.Add(2 * time.Minute),
		Payload: mustJSON(t, map[string]any{"model": "model-a", "input_tokens": 10, "output_tokens": 5, "cost_usd": 0.07}),
	}
	if err := c.Append(ctx, &usage); err != nil {
		t.Fatal(err)
	}
	run1End := contracts.Envelope{
		Type: contracts.PipelineCompleted, SessionID: sess.ID, Timestamp: t0.Add(3 * time.Minute),
		Payload: mustJSON(t, map[string]any{"outcome": "completed", "done": 2, "blocked": 0, "skipped": 0}),
	}
	if err := c.Append(ctx, &run1End); err != nil {
		t.Fatal(err)
	}

	run2Start := contracts.Envelope{Type: contracts.PipelineStarted, SessionID: sess.ID, Timestamp: t0.Add(time.Hour)}
	if err := c.Append(ctx, &run2Start); err != nil {
		t.Fatal(err)
	}
	run2End := contracts.Envelope{
		Type: contracts.PipelineCancelled, SessionID: sess.ID, Timestamp: t0.Add(time.Hour + time.Minute),
	}
	if err := c.Append(ctx, &run2End); err != nil {
		t.Fatal(err)
	}

	got, err := c.MetricsPipelineRuns(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 runs, got %d: %+v", len(got), got)
	}
	if !got[0].StartedAt.Before(got[1].StartedAt) {
		t.Fatalf("runs not in StartedAt order: %+v", got)
	}

	first := got[0]
	if first.RunSeq != run1Start.Seq {
		t.Errorf("first run_seq = %d, want %d", first.RunSeq, run1Start.Seq)
	}
	if first.Outcome != "completed" || first.TasksDone != 2 {
		t.Errorf("first run outcome/done: %+v", first)
	}
	if math.Abs(first.CostUSD-0.07) > 1e-9 {
		t.Errorf("first run cost = %v, want 0.07", first.CostUSD)
	}
	if len(first.Agents) != 1 || first.Agents[0] != "executor" {
		t.Errorf("first run agents = %+v, want [executor]", first.Agents)
	}

	second := got[1]
	if second.Outcome != "cancelled" {
		t.Errorf("second run outcome = %q, want cancelled", second.Outcome)
	}
}
