package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestMetricsCost checks the Router's /metrics/cost proxies the Data
// Service's own route, query param and response body unchanged.
func TestMetricsCost(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	ctx := context.Background()

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	env := contracts.Envelope{
		Type: contracts.UsageRecorded, SessionID: sess.ID,
		Payload: mustJSON(t, map[string]any{"model": "model-a", "input_tokens": 10, "output_tokens": 5, "cost_usd": 0.07}),
	}
	if err := data.Append(ctx, &env); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/metrics/cost?session_id=" + sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[contracts.CostMetrics](t, resp)
	if len(got.BySession) != 1 || got.BySession[0].SessionID != sess.ID {
		t.Fatalf("by_session = %+v, want one entry for %s", got.BySession, sess.ID)
	}
}

func TestMetricsAgentLeaderboard(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	ctx := context.Background()

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	env := contracts.Envelope{
		Type: contracts.AgentSelected, SessionID: sess.ID,
		Payload: mustJSON(t, map[string]any{"agent": "executor"}),
	}
	if err := data.Append(ctx, &env); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/metrics/agents")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[[]contracts.AgentLeaderboardEntry](t, resp)
	found := false
	for _, e := range got {
		if e.Agent == "executor" && e.UsageCount == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("GET /metrics/agents did not include seeded executor usage: %+v", got)
	}
}

func TestMetricsTaskOutcomes(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	ctx := context.Background()

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	task := &contracts.Task{SessionID: sess.ID, Title: "t1", Status: contracts.StatusReady, Source: "user"}
	if err := data.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/metrics/tasks?session_id=" + sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[contracts.TaskOutcomes](t, resp)
	if got.Total != 1 || got.Counts[contracts.StatusReady] != 1 {
		t.Fatalf("task outcomes = %+v, want total 1, ready 1", got)
	}
}

func TestMetricsCapabilityUsage(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	ctx := context.Background()

	unused := contracts.Capability{
		Name: "unused-skill", Type: contracts.CapabilitySkill,
		Config: contracts.CapabilityConfig{Path: "/skills/unused"},
	}
	if err := data.CreateCapability(ctx, &unused); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/metrics/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[[]contracts.CapabilityUsage](t, resp)
	found := false
	for _, u := range got {
		if u.Capability == unused.Name && u.Invocations == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("GET /metrics/capabilities did not include never-used %s zero-filled: %+v", unused.Name, got)
	}
}

func TestMetricsPipelineRuns(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	ctx := context.Background()

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	started := contracts.Envelope{Type: contracts.PipelineStarted, SessionID: sess.ID}
	if err := data.Append(ctx, &started); err != nil {
		t.Fatal(err)
	}
	completed := contracts.Envelope{
		Type: contracts.PipelineCompleted, SessionID: sess.ID,
		Payload: mustJSON(t, map[string]any{"outcome": "completed", "done": 1, "blocked": 0, "skipped": 0}),
	}
	if err := data.Append(ctx, &completed); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/metrics/pipelines?session_id=" + sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON[[]contracts.PipelineRun](t, resp)
	if len(got) != 1 || got[0].SessionID != sess.ID || got[0].Outcome != "completed" {
		t.Fatalf("pipeline runs = %+v, want one completed run for %s", got, sess.ID)
	}
}
