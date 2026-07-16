package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/pkg/registrytest"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// newTestServerWithRegistry is like newTestServer but wires the Router to a
// caller-supplied registry base URL instead of testRegistry's inert
// default — used to point two independent Routers at the same registrytest
// fixture server (a "source" side that publishes, a "fresh install" side
// that installs).
func newTestServerWithRegistry(t *testing.T, registryBase string) (*httptest.Server, string) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	fm := fastmodel.New(fake.New(), fastmodel.Config{})
	reg := registryclient.New(registryBase)
	opts := testOptions(t, data, sup)
	opts.Jobs = job.New(data, fm, reg)
	opts.VaultRefs = nil
	opts.Registry = reg
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	return srv, base
}

// waitForJobTerminal polls dataBase's event log for jobID's job.completed or
// job.failed event, returning it. Fails the test if it doesn't show up
// within timeout.
func waitForJobTerminal(t *testing.T, dataBase, jobID string, timeout time.Duration) contracts.Envelope {
	t.Helper()
	verify := dataclient.New(dataBase, "test-verify")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs, err := verify.Events(context.Background(), 0, "", []string{"job.completed", "job.failed"})
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			var payload struct {
				JobID string `json:"job_id"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err == nil && payload.JobID == jobID {
				return ev
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach a terminal state within %s", jobID, timeout)
	return contracts.Envelope{}
}

func spawnJobID(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	body := decodeJSON[struct {
		JobID string `json:"job_id"`
	}](t, resp)
	if body.JobID == "" {
		t.Fatal("no job_id returned")
	}
	return body.JobID
}

// TestRegistryPublishInstallUpdateRoundTrip is the acceptance test: a
// "source" Router publishes agent/capability/template bundles to a shared
// registrytest fixture, a "fresh install" Router browses and installs them,
// a malformed bundle is refused loudly rather than silently installed, and
// re-installing a newer version upserts in place instead of duplicating.
func TestRegistryPublishInstallUpdateRoundTrip(t *testing.T) {
	registryBase := registrytest.Start(t)

	// --- Step 1-2: source Router A, seeded with one of each item type. ---
	srvA, dataBaseA := newTestServerWithRegistry(t, registryBase)
	dataA := dataclient.New(dataBaseA, "test-seed")

	agent := &contracts.Agent{
		Name: "diff-reviewer", Prompt: "You review code for correctness.",
		Quadrant: contracts.QuadrantTechnicalPartner,
	}
	if err := dataA.CreateAgent(context.Background(), agent); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	capability := &contracts.Capability{
		Name: "jira", Type: contracts.CapabilityMCP,
		Config: contracts.CapabilityConfig{Command: "npx @modelcontextprotocol/server-jira"},
	}
	if err := dataA.CreateCapability(context.Background(), capability); err != nil {
		t.Fatalf("seed capability: %v", err)
	}
	tmpl := &contracts.Template{
		Name: "nightly", Source: "name: nightly\nstages:\n  - name: verify\n    command: make test\n",
	}
	if err := dataA.CreateTemplate(context.Background(), tmpl); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	// --- Step 3: publish all three from A, wait for job.completed. ---
	publishResp := postJSON(t, srvA.URL+"/registry/publish", map[string]any{
		"items": []map[string]string{
			{"type": "agent", "id": agent.ID},
			{"type": "capability", "id": capability.ID},
			{"type": "template", "id": tmpl.ID},
		},
	})
	publishJobID := spawnJobID(t, publishResp)
	if ev := waitForJobTerminal(t, dataBaseA, publishJobID, 5*time.Second); ev.Type != contracts.JobCompleted {
		t.Fatalf("publish job = %s, want job.completed: %s", ev.Type, ev.Payload)
	}

	// --- Step 4: fresh install Router B, pointed at the same registry. ---
	srvB, dataBaseB := newTestServerWithRegistry(t, registryBase)
	dataB := dataclient.New(dataBaseB, "test-verify")

	// --- Step 5: index on B shows all three at version "1". ---
	indexResp, err := http.Get(srvB.URL + "/registry/index")
	if err != nil {
		t.Fatal(err)
	}
	entries := decodeJSON[[]registryclient.IndexEntry](t, indexResp)
	byName := map[string]registryclient.IndexEntry{}
	for _, e := range entries {
		byName[string(e.Type)+"/"+e.Name] = e
	}
	for _, want := range []string{"agent/diff-reviewer", "capability/jira", "template/nightly"} {
		e, ok := byName[want]
		if !ok {
			t.Fatalf("index missing %s: %+v", want, entries)
		}
		if e.Latest != "1" || len(e.Versions) != 1 {
			t.Fatalf("%s: latest=%q versions=%v, want latest=1 versions=[1]", want, e.Latest, e.Versions)
		}
	}

	// --- Step 6: install all three into B. ---
	installJobIDs := map[string]string{}
	for _, it := range []struct{ typ, name string }{
		{"agent", "diff-reviewer"}, {"capability", "jira"}, {"template", "nightly"},
	} {
		resp := postJSON(t, srvB.URL+"/registry/install", map[string]string{"type": it.typ, "name": it.name})
		installJobIDs[it.typ] = spawnJobID(t, resp)
	}
	for typ, id := range installJobIDs {
		if ev := waitForJobTerminal(t, dataBaseB, id, 5*time.Second); ev.Type != contracts.JobCompleted {
			t.Fatalf("install %s job = %s, want job.completed: %s", typ, ev.Type, ev.Payload)
		}
	}

	// --- Step 7: B's local registry matches what A published (ignoring
	// ID/timestamps, which B regenerates on Create). ---
	gotAgent, err := dataB.Agent(context.Background(), "diff-reviewer")
	if err != nil {
		t.Fatalf("B: get installed agent: %v", err)
	}
	if gotAgent.Prompt != agent.Prompt || gotAgent.Quadrant != agent.Quadrant {
		t.Fatalf("B installed agent = %+v, want prompt/quadrant matching %+v", gotAgent, agent)
	}
	installedAgentID := gotAgent.ID

	gotCapability, err := dataB.Capability(context.Background(), "jira")
	if err != nil {
		t.Fatalf("B: get installed capability: %v", err)
	}
	if gotCapability.Type != capability.Type || gotCapability.Config != capability.Config {
		t.Fatalf("B installed capability = %+v, want type/config matching %+v", gotCapability, capability)
	}

	gotTemplate, err := dataB.Template(context.Background(), "nightly")
	if err != nil {
		t.Fatalf("B: get installed template: %v", err)
	}
	if gotTemplate.Source != tmpl.Source {
		t.Fatalf("B installed template source = %q, want %q", gotTemplate.Source, tmpl.Source)
	}

	// --- Step 8a: a malformed publish body is rejected 422 by the fixture
	// server itself. ---
	badPublishResp, err := http.Post(registryBase+"/bundles", "application/json", strings.NewReader(`{"type":"agent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if badPublishResp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("fixture publish of malformed bundle: status = %d, want 422", badPublishResp.StatusCode)
	}

	// --- Step 8b: a bundle that reaches Bundle() malformed (type/payload
	// mismatch, seeded directly via the Go API — bypassing the HTTP publish
	// path's own Validate check, simulating a compromised/broken remote)
	// makes install fail loudly rather than installing silently. ---
	badServer := httptest.NewServer(registrytest.New(registryclient.Bundle{
		Type: registryclient.ItemAgent, Name: "sneaky", // Agent left nil: type/payload mismatch
		Capability: &contracts.Capability{
			Name: "sneaky", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "x"},
		},
	}))
	t.Cleanup(badServer.Close)

	srvC, dataBaseC := newTestServerWithRegistry(t, badServer.URL)
	dataC := dataclient.New(dataBaseC, "test-verify")
	beforeAgents, err := dataC.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	badInstallResp := postJSON(t, srvC.URL+"/registry/install", map[string]string{"type": "agent", "name": "sneaky"})
	badInstallJobID := spawnJobID(t, badInstallResp)
	badEv := waitForJobTerminal(t, dataBaseC, badInstallJobID, 5*time.Second)
	if badEv.Type != contracts.JobFailed {
		t.Fatalf("malformed install job = %s, want job.failed: %s", badEv.Type, badEv.Payload)
	}
	var badPayload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(badEv.Payload, &badPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(badPayload.Error, "malformed bundle") {
		t.Fatalf("malformed install error = %q, want it to mention a malformed bundle", badPayload.Error)
	}
	afterAgents, err := dataC.Agents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(afterAgents) != len(beforeAgents) {
		t.Fatalf("malformed install polluted the local registry: before=%d after=%d", len(beforeAgents), len(afterAgents))
	}

	// --- Step 9: update flow — publish v2 of the agent from A with a
	// different prompt, update B, and confirm it upserts in place (same ID)
	// rather than duplicating. ---
	agent.Prompt = "You review code for correctness and security."
	if err := dataA.UpdateAgent(context.Background(), agent); err != nil {
		t.Fatalf("A: update agent for v2: %v", err)
	}
	publishV2Resp := postJSON(t, srvA.URL+"/registry/publish", map[string]any{
		"items": []map[string]string{{"type": "agent", "id": agent.ID}},
	})
	publishV2JobID := spawnJobID(t, publishV2Resp)
	if ev := waitForJobTerminal(t, dataBaseA, publishV2JobID, 5*time.Second); ev.Type != contracts.JobCompleted {
		t.Fatalf("publish v2 job = %s, want job.completed: %s", ev.Type, ev.Payload)
	}

	updateResp := postJSON(t, srvB.URL+"/registry/update", map[string]string{
		"type": "agent", "name": "diff-reviewer", "version": "2",
	})
	updateJobID := spawnJobID(t, updateResp)
	if ev := waitForJobTerminal(t, dataBaseB, updateJobID, 5*time.Second); ev.Type != contracts.JobCompleted {
		t.Fatalf("update job = %s, want job.completed: %s", ev.Type, ev.Payload)
	}

	updatedAgent, err := dataB.Agent(context.Background(), "diff-reviewer")
	if err != nil {
		t.Fatalf("B: get updated agent: %v", err)
	}
	if updatedAgent.ID != installedAgentID {
		t.Fatalf("update produced a new ID (%s), want the same ID as the original install (%s) — upsert-by-name is broken", updatedAgent.ID, installedAgentID)
	}
	if updatedAgent.Prompt != agent.Prompt {
		t.Fatalf("updated agent prompt = %q, want %q", updatedAgent.Prompt, agent.Prompt)
	}
}
