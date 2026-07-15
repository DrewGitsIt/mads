package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/provider"
)

func TestListSessions(t *testing.T) {
	srv := newTestServer(t)
	resp := postJSON(t, srv.URL+"/sessions", &contracts.Session{Name: "s1"})
	created := decodeJSON[map[string]any](t, resp)
	sess := created["session"].(map[string]any)
	id := sess["id"].(string)

	resp, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	list := decodeJSON[[]contracts.Session](t, resp)
	found := false
	for _, s := range list {
		if s.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("GET /sessions did not include created session %s: %v", id, list)
	}
}

func TestListWorkspaces(t *testing.T) {
	srv, base := newTestServerWithData(t)
	resp := postJSON(t, base+"/workspaces", &contracts.Workspace{Name: "ws1"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed workspace: status = %d", resp.StatusCode)
	}
	created := decodeJSON[contracts.Workspace](t, resp)

	resp, err := http.Get(srv.URL + "/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	list := decodeJSON[[]contracts.Workspace](t, resp)
	found := false
	for _, w := range list {
		if w.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("GET /workspaces did not include seeded workspace %s: %v", created.ID, list)
	}
}

func TestListAgents(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	agent := &contracts.Agent{Name: "agent1", Prompt: "do stuff"}
	if err := data.CreateAgent(context.Background(), agent); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/agents")
	if err != nil {
		t.Fatal(err)
	}
	list := decodeJSON[[]contracts.Agent](t, resp)
	found := false
	for _, a := range list {
		if a.ID == agent.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("GET /agents did not include seeded agent %s: %v", agent.ID, list)
	}
}

func TestListTasks(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	task := &contracts.Task{SessionID: sess.ID, Title: "t1", Status: contracts.StatusReady, Source: "user"}
	if err := data.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/tasks?session_id=" + sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	list := decodeJSON[[]contracts.Task](t, resp)
	found := false
	for _, tk := range list {
		if tk.ID == task.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("GET /tasks did not include seeded task %s: %v", task.ID, list)
	}
}

func TestListProviders(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/providers")
	if err != nil {
		t.Fatal(err)
	}
	infos := decodeJSON[[]struct {
		Name   string            `json:"name"`
		Matrix map[string]string `json:"matrix"`
	}](t, resp)
	if len(infos) != 1 || infos[0].Name != "fake" {
		t.Fatalf("GET /providers = %+v, want one entry named fake", infos)
	}
	if len(infos[0].Matrix) == 0 {
		t.Fatalf("GET /providers: fake entry has no matrix")
	}
}

func TestListProviderBehaviors(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/providers/behaviors")
	if err != nil {
		t.Fatal(err)
	}
	behaviors := decodeJSON[[]struct {
		Behavior string `json:"behavior"`
		Level    string `json:"level"`
	}](t, resp)
	if len(behaviors) == 0 {
		t.Fatalf("GET /providers/behaviors returned no rows")
	}
}

func TestListProviderModels(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/providers/fake/models")
	if err != nil {
		t.Fatal(err)
	}
	models := decodeJSON[[]provider.Model](t, resp)
	if len(models) == 0 {
		t.Fatalf("GET /providers/fake/models returned no models")
	}

	resp, err = http.Get(srv.URL + "/providers/nonexistent/models")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /providers/nonexistent/models = %d, want 404", resp.StatusCode)
	}
}
