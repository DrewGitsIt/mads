package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/services/session/internal/api"
	"github.com/drewkidwell/mads/services/session/internal/runner"
)

func TestHealth(t *testing.T) {
	// /health never touches the runner (spec §4: always 200 regardless of
	// runner state), so an unstarted Runner is enough for this test.
	r := runner.New("unused", nil, nil)
	srv := httptest.NewServer(api.New(r))
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
	if body["service"] != "session" || body["status"] != "ok" {
		t.Errorf("unexpected health body: %v", body)
	}
}
