package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
)

// Each agent one-shot posts a usage.recorded event carrying the fake's
// scripted cost, and the Data Service's cost metrics aggregate them
// correctly by day/model and by session (spec §8's cost pane, W5.7's
// consumer for this wiring).
func TestUsageRecordedFeedsCostMetrics(t *testing.T) {
	h := newHarness(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "scripted-cost", Rules: []fake.Rule{
		{Match: "Pipeline stage: executor", Reply: "STAGE: pass", Usage: &fake.UsageOverride{
			Model: "model-a", InputTokens: 100, OutputTokens: 50, CostUSD: 0.10,
		}},
		{Match: "Pipeline stage: code_review", Reply: "STAGE: pass", Usage: &fake.UsageOverride{
			Model: "model-b", InputTokens: 200, OutputTokens: 100, CostUSD: 0.20,
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Two independent trivial-labeled tasks: architect is skipped, so each
	// runs exactly the two scripted agent stages above.
	seeded := h.seed(
		contracts.Task{Title: "typo fix a", Labels: []string{"trivial"}},
		contracts.Task{Title: "typo fix b", Labels: []string{"trivial"}},
	)

	sum := h.run(t, f, h.defaultTemplate(), 2)
	if len(sum.Done) != 2 {
		t.Fatalf("summary: %+v, want both tasks done: %+v, %+v",
			sum, h.task(t, seeded[0].ID), h.task(t, seeded[1].ID))
	}

	got, err := h.data.MetricsCost(context.Background(), h.sess.ID)
	if err != nil {
		t.Fatal(err)
	}

	byModel := map[string]contracts.CostPoint{}
	for _, cp := range got.ByDay {
		byModel[cp.Model] = cp
	}
	a := byModel["model-a"]
	if a.InputTokens != 200 || a.OutputTokens != 100 || math.Abs(a.CostUSD-0.20) > 1e-9 {
		t.Errorf("by_day model-a: %+v, want 200/100/0.20 (2 executor stages)", a)
	}
	b := byModel["model-b"]
	if b.InputTokens != 400 || b.OutputTokens != 200 || math.Abs(b.CostUSD-0.40) > 1e-9 {
		t.Errorf("by_day model-b: %+v, want 400/200/0.40 (2 code_review stages)", b)
	}

	if len(got.BySession) != 1 || got.BySession[0].SessionID != h.sess.ID || math.Abs(got.BySession[0].CostUSD-0.60) > 1e-9 {
		t.Errorf("by_session: %+v, want one entry for %s totalling 0.60", got.BySession, h.sess.ID)
	}
}

// A usage.recorded post that fails never fails the stage it describes: the
// one-shot already succeeded, so the task still completes — only the cost
// metric is missing.
func TestUsageRecordFailureDoesNotBlockStage(t *testing.T) {
	h := newHarness(t)
	target, err := url.Parse(h.base)
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	// Fails only POST /events carrying a usage.recorded envelope; every
	// other write (task claims/updates, stage.* events) reaches the real
	// Data Service untouched, so a failure here is isolated to usage
	// posting rather than breaking the run wholesale.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/events" {
			body, _ := io.ReadAll(r.Body)
			var env contracts.Envelope
			_ = json.Unmarshal(body, &env)
			if env.Type == contracts.UsageRecorded {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		rp.ServeHTTP(w, r)
	}))
	defer proxy.Close()

	data := dataclient.New(proxy.URL, "pipeline")
	seeded := h.seed(contracts.Task{Title: "typo fix", Labels: []string{"trivial"}})
	sum, err := Run(context.Background(), Config{
		Data: data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: h.defaultTemplate(), Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v, want the task done despite failed usage posts", sum)
	}
	if task := h.task(t, seeded[0].ID); task.Status != contracts.StatusDone {
		t.Fatalf("task status: %s", task.Status)
	}

	// Confirm the posts really did fail (not a vacuous pass): no cost ever
	// landed for this session.
	got, err := h.data.MetricsCost(context.Background(), h.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ByDay) != 0 || len(got.BySession) != 0 {
		t.Errorf("cost metrics: %+v, want none — every usage post was made to fail", got)
	}
}

// usage.recorded names the stage that ran the shot and its loop attempt
// number, so per-stage cost analysis doesn't need a fragile seq-adjacency
// join against stage.completed (which breaks under interleaved histories).
// code_review fails every time and loops back to executor (default
// template), so code_review itself runs three times (initial + two
// loop-backs) — its own attempt count must climb 1, 2, 3 across those runs.
func TestUsageRecordedNamesStageAndAttempt(t *testing.T) {
	h := newHarness(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "review-fail-attribution", Rules: []fake.Rule{
		{Match: "code_review\nTask: flaky api", Reply: "This won't do.\nSTAGE: fail — missing error handling"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h.seed(contracts.Task{Title: "flaky api", Labels: []string{"trivial"}})

	h.run(t, f, h.defaultTemplate(), 1)

	type stageAttempt struct {
		Stage   string `json:"stage"`
		Attempt int    `json:"attempt"`
	}
	var executorAttempts, codeReviewAttempts []int
	for _, ev := range h.events(t, "usage.recorded") {
		var p stageAttempt
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		switch p.Stage {
		case "executor":
			executorAttempts = append(executorAttempts, p.Attempt)
		case "code_review":
			codeReviewAttempts = append(codeReviewAttempts, p.Attempt)
		case "":
			t.Errorf("usage.recorded missing stage: %s", ev.Payload)
		default:
			t.Errorf("usage.recorded for unexpected stage: %s", ev.Payload)
		}
	}
	if len(executorAttempts) != 3 {
		t.Errorf("executor usage.recorded events: %v, want 3 (one per loop-back re-run)", executorAttempts)
	}
	if want := []int{1, 2, 3}; len(codeReviewAttempts) != len(want) ||
		codeReviewAttempts[0] != want[0] || codeReviewAttempts[1] != want[1] || codeReviewAttempts[2] != want[2] {
		t.Errorf("code_review attempts: %v, want %v (climbing across its own loop-backs)", codeReviewAttempts, want)
	}
}
