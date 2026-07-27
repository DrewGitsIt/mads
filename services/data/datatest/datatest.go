// Package datatest starts a real Data Service — full migration set, actual
// HTTP API, temp-dir SQLite — for other services' tests. It is the only
// exported doorway to services/data internals, and it exists so downstream
// suites (pipeline engine, router, session service) test against the real
// contract instead of mocks (spec §15).
package datatest

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/services/data/internal/api"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// Start returns the base URL of a live Data Service that lasts for the test.
func Start(t *testing.T) string {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "mads.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db, store.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedShippedCapabilities(context.Background(), store.NewCapabilities(store.NewLog(db))); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(db))
	t.Cleanup(srv.Close)
	return srv.URL
}
