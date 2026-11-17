// Package datatest starts a real Data Service — full migration set, actual
// HTTP API, temp-dir SQLite — for other services' tests. It is the only
// exported doorway to services/data internals, and it exists so downstream
// suites (pipeline engine, router, session service) test against the real
// contract instead of mocks (spec §15).
package datatest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/localauth"
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
	if err := store.Bootstrap(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(db))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Guarded is a live Data Service behind the local front door, as main.go
// serves it: every route but GET /health needs Token
// (docs/LOCAL-FRONT-DOOR-DESIGN.md D3).
type Guarded struct {
	URL string
	// Home is the scratch $MADS_HOME holding the install token, and
	// TokenFile its local.token.
	Home, TokenFile string
	Token           string
}

// StartGuarded is Start behind the front door, with a fresh install token
// in a scratch $MADS_HOME.
func StartGuarded(t *testing.T) Guarded {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "mads.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Bootstrap(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	tokens, err := localauth.LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	mux := api.New(db)
	door := localauth.NewDoor(localauth.DoorOptions{
		Name:     "data",
		Store:    tokens,
		Classify: func(r *http.Request) string { _, p := mux.Handler(r); return p },
		Exempt:   []string{"GET /health"},
	})
	srv := httptest.NewServer(door.Wrap(mux))
	t.Cleanup(srv.Close)
	return Guarded{URL: srv.URL, Home: home, TokenFile: localauth.Path(home), Token: tokens.Token()}
}
