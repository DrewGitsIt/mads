package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/services/data/internal/api"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// dataRoutes reads every pattern the Data Service registers straight from
// its source, so a new route can't dodge the guard below by omission.
func dataRoutes(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("internal/api/*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`)
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) //nolint:gosec // test reads its own package's source
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			seen[m[1]] = true
		}
	}
	if len(seen) < 60 {
		t.Fatalf("found only %d Data Service routes; has registration moved out of internal/api?", len(seen))
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// requestFor turns a pattern ("POST /tasks/{id}/notes") into a concrete
// request with every wildcard filled.
func requestFor(t *testing.T, pattern string) *http.Request {
	t.Helper()
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		method, path = http.MethodGet, pattern
	}
	path = regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "x1")
	path = strings.ReplaceAll(path, "{$}", "")
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func newDataStack(t *testing.T) (http.Handler, *localauth.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "mads.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tokens, err := localauth.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newHandler(db, api.Identity{Root: "/src"}, dataDoor(tokens)), tokens
}

// TestEveryDataRouteNeedsTheToken is the guard: every route the Data
// Service registers refuses a caller with no token, or the wrong one —
// except GET /health (docs/LOCAL-FRONT-DOOR-DESIGN.md D3).
func TestEveryDataRouteNeedsTheToken(t *testing.T) {
	h, _ := newDataStack(t)
	exempt := map[string]bool{"GET /health": true}
	for _, pattern := range dataRoutes(t) {
		for name, auth := range map[string]string{"no token": "", "wrong token": "Bearer not-the-install-token-not-the-install-token"} {
			r := requestFor(t, pattern)
			r.Host = "127.0.0.1:17374"
			if auth != "" {
				r.Header.Set("Authorization", auth)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if exempt[pattern] {
				if w.Code == http.StatusUnauthorized {
					t.Errorf("%s (%s): exempt route refused", pattern, name)
				}
				continue
			}
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s (%s): code %d, want 401", pattern, name, w.Code)
			}
		}
	}
}

func TestDataRoutesAdmitTheToken(t *testing.T) {
	h, tokens := newDataStack(t)
	for _, pattern := range []string{"GET /tasks", "GET /sessions", "GET /config", "POST /tasks", "GET /health"} {
		r := requestFor(t, pattern)
		r.Host = "127.0.0.1:17374"
		r.Header.Set("Authorization", "Bearer "+tokens.Token())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
			t.Errorf("%s with the token: %d", pattern, w.Code)
		}
	}
	// Unknown paths aren't a way around the door either.
	r := httptest.NewRequest("GET", "/no/such/route", nil)
	r.Host = "127.0.0.1:17374"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unknown path without a token: %d, want 401", w.Code)
	}
}

func TestDataHealthHidesIdentityWithoutTheToken(t *testing.T) {
	h, tokens := newDataStack(t)
	for _, tc := range []struct {
		auth     string
		wantRoot bool
	}{{"", false}, {"Bearer " + tokens.Token(), true}} {
		r := httptest.NewRequest("GET", "/health", nil)
		r.Host = "127.0.0.1:17374"
		if tc.auth != "" {
			r.Header.Set("Authorization", tc.auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("health: %d", w.Code)
		}
		if got := strings.Contains(w.Body.String(), `"root"`); got != tc.wantRoot {
			t.Fatalf("auth=%v: body %s", tc.auth != "", w.Body)
		}
	}
}

// TestInsecureNoAuthIsRefusedOutsideTests: a real build can't turn the
// door off.
func TestInsecureNoAuthIsRefusedOutsideTests(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "data")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "-insecure-no-auth", "-addr", "127.0.0.1:0", "-data-dir", dir, "-mads-home", dir) //nolint:gosec // test-built binary
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "-insecure-no-auth is refused") {
		t.Fatalf("data -insecure-no-auth: err %v\n%s", err, out)
	}
}
