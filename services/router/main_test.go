package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

func TestHealth(t *testing.T) {
	data := dataclient.New("http://localhost:0", "router")
	sup := supervisor.New(data, nil, nil, nil)
	jobs := job.New(data, fastmodel.New(fake.New(), fastmodel.Config{}))
	providers := provider.NewRegistry()
	if err := providers.Register(fake.New()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newMux(data, sup, jobs, providers))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /health = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["service"] != "router" || body["status"] != "ok" {
		t.Errorf("unexpected health body: %v", body)
	}
}
