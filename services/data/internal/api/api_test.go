package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/localauth"
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

	mux := NewWithIdentity(db, Identity{
		Root: "/src/checkout", StartedAt: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC),
	})
	// The token's holder sees where the service runs from; anyone else
	// sees only service and status (docs/LOCAL-FRONT-DOOR-DESIGN.md D3).
	anon := httptest.NewServer(mux)
	defer anon.Close()
	aresp, err := anon.Client().Get(anon.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	var abody map[string]string
	if err := json.NewDecoder(aresp.Body).Decode(&abody); err != nil {
		t.Fatal(err)
	}
	_ = aresp.Body.Close()
	if len(abody) != 2 || abody["service"] != "data" || abody["status"] != "ok" {
		t.Errorf("anonymous health body: %v", abody)
	}

	srv := httptest.NewServer(localauth.Insecure().Wrap(mux))
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
