package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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
	if _, ok := body["root"]; ok {
		t.Errorf("health body has root without an identity: %v", body)
	}
}

// TestHealthReportsIdentity pins the fields scripts/app.sh reads to refuse
// reusing a Data Service launched from a different checkout.
func TestHealthReportsIdentity(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	srv := httptest.NewServer(NewWithIdentity(db, Identity{
		Root: "/src/checkout", StartedAt: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC),
	}))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["root"] != "/src/checkout" || body["started_at"] != "2026-09-28T10:00:00Z" || body["service"] != "data" {
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
