package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/services/data/internal/store"
)

func TestHealth(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	srv := httptest.NewServer(New(db))
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
	if body["service"] != "data" || body["status"] != "ok" {
		t.Errorf("unexpected health body: %v", body)
	}
}

func TestHealthReportsClosedDatabase(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	srv := httptest.NewServer(New(db))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 503 {
		t.Fatalf("GET /health with closed db = %d, want 503", resp.StatusCode)
	}
}
