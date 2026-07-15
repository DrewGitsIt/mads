package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/agentgen"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// newTestServerWithScenario is like newTestServer but wires the job.Runner's
// Fast Model to a scripted fake, for tests that need a real generated draft
// rather than the built-in dialect's "ok".
func newTestServerWithScenario(t *testing.T, sc fake.Scenario) (*httptest.Server, string) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	f, err := fake.NewWithScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	fm := fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
	srv := httptest.NewServer(api.New(data, sup, job.New(data, fm), testProviders(t)))
	t.Cleanup(srv.Close)
	return srv, base
}

func eventually(t *testing.T, budget time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !fn() {
		t.Fatalf("condition not met within %s", budget)
	}
}

func TestGenerateAgentFromTaskContext(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{Name: "generate-task", Rules: []fake.Rule{{
		Match: "Fix the flaky login test",
		Reply: `{"name":"login-test-fixer","description":"Fixes flaky login tests",` +
			`"prompt":"You are a QA engineer.","quadrant":"autonomous_executor",` +
			`"tags":["testing"],"capabilities":{},"permissions":{}}`,
	}}})

	resp := postJSON(t, srv.URL+"/agents/generate", map[string]any{
		"session_id": "ses_gen", "task_id": "tsk_gen", "title": "Fix the flaky login test",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	body := decodeJSON[struct {
		JobID string `json:"job_id"`
	}](t, resp)
	if body.JobID == "" {
		t.Fatal("no job_id returned")
	}

	verify := dataclient.New(dataBase, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(context.Background(), 0, "ses_gen", []string{"agent.draft_generated"})
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		return len(evs) > 0
	})

	evs, err := verify.Events(context.Background(), 0, "ses_gen", []string{"agent.draft_generated"})
	if err != nil || len(evs) != 1 {
		t.Fatalf("events: %d, %v", len(evs), err)
	}
	if evs[0].TaskID != "tsk_gen" {
		t.Fatalf("TaskID = %q, want tsk_gen", evs[0].TaskID)
	}
	var res agentgen.Result
	if err := json.Unmarshal(evs[0].Payload, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Draft.Name != "login-test-fixer" {
		t.Fatalf("draft.Name = %q, want login-test-fixer", res.Draft.Name)
	}
}

func TestGenerateAgentFromDescription(t *testing.T) {
	srv, dataBase := newTestServerWithScenario(t, fake.Scenario{Name: "generate-description", Rules: []fake.Rule{{
		Match: "database migrations and knows PostgreSQL",
		Reply: `{"name":"db-migrator","description":"Specializes in PostgreSQL migrations",` +
			`"prompt":"You are a database migration specialist.","quadrant":"technical_partner",` +
			`"tags":["database"],"capabilities":{},"permissions":{}}`,
	}}})

	resp := postJSON(t, srv.URL+"/agents/generate", map[string]any{
		"session_id":  "ses_gen_nl",
		"description": "I need an agent that specializes in database migrations and knows PostgreSQL",
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	verify := dataclient.New(dataBase, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(context.Background(), 0, "ses_gen_nl", []string{"agent.draft_generated"})
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		return len(evs) > 0
	})

	evs, err := verify.Events(context.Background(), 0, "ses_gen_nl", []string{"agent.draft_generated"})
	if err != nil || len(evs) != 1 {
		t.Fatalf("events: %d, %v", len(evs), err)
	}
	var res agentgen.Result
	if err := json.Unmarshal(evs[0].Payload, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Draft.Name != "db-migrator" {
		t.Fatalf("draft.Name = %q, want db-migrator", res.Draft.Name)
	}
}

func TestGenerateAgentValidation(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/agents/generate", map[string]any{"session_id": "ses_x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	verr := decodeJSON[contracts.ValidationError](t, resp)
	if len(verr.Errors) != 1 || verr.Errors[0].Field != "title" {
		t.Fatalf("errors = %+v, want one error on title", verr.Errors)
	}
}
