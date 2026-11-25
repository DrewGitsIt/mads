package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/data/internal/api"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// TestRound2DataServiceRefusesRouterOnlyCredentials (adversarial round 2):
// a session-scoped token is the Router's alone; the Data Service accepts
// only the install token, by bearer, and no cookie of any name.
func TestRound2DataServiceRefusesRouterOnlyCredentials(t *testing.T) {
	h, tokens := newDataStack(t)
	scoped, err := localauth.NewRegistry().Mint("ses_a")
	if err != nil {
		t.Fatal(err)
	}
	for name, set := range map[string]func(*http.Request){
		"install token as a cookie": func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "mads_local_17374", Value: tokens.Token()})
		},
		"install token as the legacy cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "mads_local", Value: tokens.Token()}) },
		"scoped token as a bearer":           func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+scoped) },
	} {
		for _, pattern := range []string{"GET /sessions", "GET /tasks", "POST /tasks", "GET /config"} {
			r := requestFor(t, pattern)
			r.Host = "127.0.0.1:17374"
			set(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s on %s: %d, want 401", name, pattern, w.Code)
			}
		}
	}
}

// TestRound2ChildTokenFileAcrossRotationDeleteAndChmod (adversarial round 2,
// H4): a child's FileSource against the real Data Service door. A rotation
// mid-flight: the child's next call uses the new token and the old one is
// refused. The file deleted, or chmodded world-readable, while both are
// running: both sides keep the last good token (a pinned design choice:
// "a service shouldn't lock its own user out"), so the token that just sat
// in a world-readable file stays valid until someone rotates — the check
// in D1 only bites on a fresh load.
func TestRound2ChildTokenFileAcrossRotationDeleteAndChmod(t *testing.T) {
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
	srv := httptest.NewServer(newHandler(db, api.Identity{Root: "/src"}, dataDoor(tokens)))
	defer srv.Close()
	src := localauth.FileSource(localauth.Path(home))
	call := func(tok string) int {
		req, _ := http.NewRequest("GET", srv.URL+"/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	old := src()
	if call(old) != http.StatusOK {
		t.Fatal("child's first call refused")
	}
	if _, err := localauth.Rotate(home); err != nil {
		t.Fatal(err)
	}
	if got := call(src()); got != http.StatusOK {
		t.Fatalf("child after rotation: %d", got)
	}
	if got := call(old); got != http.StatusUnauthorized {
		t.Fatalf("pre-rotation token after rotation: %d, want 401", got)
	}
	cur := src()
	// chmod 644: everyone on the machine can now read cur.
	if err := os.Chmod(localauth.Path(home), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := call(src()); got != http.StatusOK {
		t.Fatalf("child after chmod 644: %d", got)
	}
	if got := call(cur); got != http.StatusOK {
		t.Logf("the service stopped accepting a token from a world-readable file: %d", got)
	} else {
		t.Logf("qualified: the service still accepts the token that sat in a world-readable file (last good token kept)")
	}
	// Deleted: both keep the last good token; a later LoadOrCreate (any
	// service restarting) mints a new one, and both follow it.
	if err := os.Remove(localauth.Path(home)); err != nil {
		t.Fatal(err)
	}
	if got := call(src()); got != http.StatusOK {
		t.Fatalf("child after the file was deleted: %d", got)
	}
	if _, err := localauth.LoadOrCreate(home); err != nil {
		t.Fatal(err)
	}
	if got := call(src()); got != http.StatusOK {
		t.Fatalf("child after a new token was created: %d", got)
	}
	if got := call(cur); got != http.StatusUnauthorized {
		t.Fatalf("the deleted file's token after a new one was created: %d, want 401", got)
	}
}
