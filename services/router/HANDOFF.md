# HANDOFF: Static fallback must not swallow API paths

## Problem

`internal/static.New`'s fallback unconditionally serves `index.html` with
`200 text/html` for *any* request path that isn't a real file under
`ui/dist` — including a typo'd or not-yet-built API route. A missing
`GET /sessions/{id}` (before it landed) 200'd as the SPA shell instead of
404ing, which poisoned JSON parsing in the UI client and hid the real
problem behind a misleading status code. `internal/static/static_test.go`'s
`TestUnknownPathFallsBackToIndex` and `internal/api/static_test.go`'s
`TestStaticSPAFallback`/`TestStaticServesIndexAtRoot` currently assert
exactly this unconditional behavior and issue bare `http.Get` calls (no
`Accept` header) — they will need to change along with the production code.

## Decision: gate the fallback on "does this look like a browser navigating"

Serve `index.html` only for **navigation-shaped** requests: `GET`/`HEAD`,
`Accept` header contains `text/html`, and the path has no file extension.
Everything else that reaches the fallback (wrong method, no/non-html
`Accept`, or an extensioned path with no matching file) gets a JSON 404:
`{"error":"not found","path":"<r.URL.Path>"}`.

Why `Accept` must explicitly contain `text/html`, not just be present/`*/*`:
a bare `fetch()` (which is what every `RouterClient` method uses today, and
was the actual failure mode) sends `Accept: */*` by default, not
`text/html`. If `*/*` counted as "prefers html", the fix would be a no-op
for the exact bug it's meant to close. Real browser navigations always send
an explicit `text/html` token near the front of `Accept`; this is a
substring check (`strings.Contains`), not full RFC 7231 q-value parsing —
proportionate to this codebase's existing style, and sufficient for both
acceptance-criteria cases (`fetch` sets `Accept: application/json`; a
navigation's `Accept` always contains `text/html`).

## Design

### 1. `internal/static/static.go`

Add an `isNavigationRequest` gate and a JSON-404 writer; branch on it where
the fallback currently always calls `http.ServeFile(w, r, index)`.

```go
package static

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func New(distDir string) http.Handler {
	if distDir == "" {
		log.Printf("static: no UI dist path configured, serving placeholder page")
		return http.HandlerFunc(serveNotBuilt)
	}
	fileServer := http.FileServer(http.Dir(distDir))
	index := filepath.Join(distDir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info, err := os.Stat(index); err != nil || info.IsDir() {
			serveNotBuilt(w, r)
			return
		}
		fsPath := filepath.Join(distDir, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(fsPath); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		if isNavigationRequest(r) {
			http.ServeFile(w, r, index)
			return
		}
		writeNotFoundJSON(w, r)
	})
}

// isNavigationRequest reports whether r looks like a browser navigating to a
// client-side SPA route, as opposed to a caller (fetch, curl, a missing/
// typo'd API route) probing for a resource. Only GET/HEAD requests for an
// extensionless path whose Accept header explicitly names text/html
// qualify — a bare fetch() (Accept defaults to "*/*", not "text/html") or a
// request for a path with an extension never does, so an absent API route
// 404s as JSON instead of silently 200'ing as the SPA shell.
func isNavigationRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if filepath.Ext(r.URL.Path) != "" {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func writeNotFoundJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found", "path": r.URL.Path})
}

const notBuiltPage = `...` // unchanged
func serveNotBuilt(w http.ResponseWriter, _ *http.Request) { /* unchanged */ }
```

Rename the existing local variable `path` (shadowing the stdlib `path`
package) to `fsPath` as shown — avoids a name collision if a future edit
needs `path.Ext` instead of `filepath.Ext`; not required for this change but
cheap to do while touching this line. `serveNotBuilt` (the "UI not built"
dev placeholder) is **untouched** — it's a bring-up signal page, not part of
the routing contract this task fixes, and no acceptance criterion mentions
it.

Update the package doc comment (currently "no dataclient, no JSON, just
files") and `New`'s doc comment (currently "falling back to index.html for
any request path that isn't a real file") to describe the new conditional
behavior — both are now slightly inaccurate.

### 2. `internal/static/static_test.go` — fix 3 existing tests, add new ones

**Existing tests that break** because they call the existing `get(t, srv,
path)` helper, which issues a bare `http.Get` with no `Accept` header — that
now means "not navigation-shaped" and gets a JSON 404, not `index.html`:

- `TestServesIndexAtRoot` (`GET /`)
- `TestUnknownPathFallsBackToIndex` (`GET /some/unknown/route`)
- `TestPicksUpBuildThatLandsAfterConstruction`'s *second* `get(t, srv, "/")`
  call (the one asserting the real build is now served) — its first call,
  before the build lands, hits `serveNotBuilt` unconditionally and is
  unaffected.

Fix: add a second helper alongside `get` that sets an explicit header, e.g.
`getWithAccept(t, srv, path, accept string) (int, string)`, and switch these
three call sites to `getWithAccept(t, srv, path, "text/html")` to simulate a
real browser navigation. `TestServesRealAssetVerbatim`,
`TestPlaceholderWhenDistMissing`, `TestPlaceholderWhenDistPathEmpty` are
unaffected (no Accept-dependent branch reached) — leave them on `get`.

**New tests to add** (package `static_test`):

- `TestNonHTMLAcceptGetsJSON404` — `GET /some/unknown/route`,
  `Accept: application/json` → 404, `Content-Type: application/json`, body
  decodes to `{"error":"not found","path":"/some/unknown/route"}`.
- `TestBareFetchDefaultAcceptGetsJSON404` — same path, `Accept: */*`
  (real browsers' default `fetch()` header — this is the case that actually
  masked the original bug; explicitly pin it, don't just rely on "no header
  at all") → 404 JSON, not `index.html`.
- `TestExtensionedPathWithHTMLAcceptStillGetsJSON404` — `GET
  /assets/missing.js` (extension present, no real file), `Accept: text/html`
  → 404 JSON. Proves the extension check wins even when `Accept` would
  otherwise qualify — pins "static assets with extensions keep today's
  behavior (real file or 404)".
- `TestPostToUnknownPathGetsJSON404` — `POST /some/unknown/route` (any
  `Accept`) → 404 JSON. Pins the method gate.

### 3. `internal/api/static_test.go` — fix 2 existing tests, add the acceptance-criteria cases

Same bare-`http.Get`-has-no-`Accept`-header problem, at the full-stack
(`api.New`) level:

- `TestStaticServesIndexAtRoot` and `TestStaticSPAFallback` both need an
  explicit `Accept: text/html` header (switch from `http.Get` to
  `http.NewRequest` + `client.Do`, or add a small local helper). `
  TestStaticServesRealAsset` and `TestStaticDoesNotShadowAPIRoutes` are
  unaffected.

Add the two literal acceptance-criteria scenarios here (this is the
integration point — full CORS + API-route-priority + static stack):

- `TestAPIShapedUnregisteredPathGetsJSON404` — `GET /sessions/nope/what`
  (nested, API-shaped, no such route registered), `Accept: application/json`
  → 404, body `{"error":"not found","path":"/sessions/nope/what"}`.
- `TestBrowserNavigationToDeepLinkGetsIndex` — `GET /deep/link`, `Accept:
  text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8` (a real
  browser's actual navigation header, not just the bare token) → 200,
  `index.html` body.

### 4. Route-manifest test — `internal/api/route_manifest_test.go` (new)

**Do not implement this as "every method on the `RouterClient` TS interface
must resolve to a registered route."** I checked: it doesn't hold today, and
not because of a bug. `ui/src/router/client.ts`'s `FetchRouterClient` has 8
methods that call routes which are **not** registered in `api.go`, and the
file's own comments say so explicitly — they're documented forward-built
contracts (same pattern as the session-detail gap this task's predecessor
task closed; see git history of this file and its own "Executor notes"
section below, which independently flagged one of these same 8 — `POST
/sessions/{id}/messages` — as an out-of-scope pre-existing gap). Verified by
grep (`grep -rn "mentionables\|/messages\|/interrupt\|/stream\"\|/commits\|
/changes\|open-editor\|/merge\"" internal/api/*.go`, excluding `_test.go`):
zero matches. The 8:

| Method | Path client.ts calls | Why it's excluded |
|---|---|---|
| `streamSession` | `GET /sessions/{id}/stream` | conversation streaming, not built |
| `sendMessage` | `POST /sessions/{id}/messages` | flagged in this file's prior revision, still open |
| `interrupt` | `POST /sessions/{id}/interrupt` | conversation control, not built |
| `getMentionables` | `GET /mentionables` | not built |
| `getCommits` | `GET /sessions/{id}/commits` | Files-tab/merge-back, not built |
| `getFileDiff` | `GET /sessions/{id}/changes` | Files-tab/merge-back, not built |
| `openInEditor` | `POST /system/open-editor` | Files-tab/merge-back, not built |
| `mergeSession` | `POST /sessions/{id}/merge` | Files-tab/merge-back, not built (only the per-session unproxied `POST /merge` exists) |

If the literal instruction were followed (enumerate all, assert all
registered), this test fails 8 ways immediately and `make check` goes red —
violating this task's own acceptance criteria. Instead: two tables, per the
established repo convention (flag real gaps explicitly rather than silently
expanding scope or silently omitting them):

- `routeManifest` — every *other* real fetch call site in
  `FetchRouterClient` (dedupe multiple methods hitting the same
  method+pattern, e.g. `getSkillCapabilities`/`listCapabilities` both hit
  `GET /capabilities`). I enumerated all ~91 methods against the current
  `api.go` mux table by hand for this design; roughly 78 unique
  (method, pattern) pairs remain after dedup and excluding the 8 above.
  Verify against `client.ts` directly when building the table — don't just
  transcribe this doc uncritically, the two files can drift.
- `knownGapRoutes` — the 8 above, in a **second test** that asserts they're
  *still* unregistered and fails loudly (telling the reader to move the row
  into `routeManifest`) the day one of them lands. This keeps the exclusion
  list honest instead of letting it silently rot into a stale lie.

**Implementation approach — introspect the mux directly, don't go over
HTTP.** Sending real HTTP requests for this (e.g. via `httptest.Server` +
matching the JSON-404 sentinel body) has a sharp edge: `GET /events` is a
real registered route several client methods hit
(`streamTaskEvents`/`streamControlEvents`), and its handler
(`handleStreamEvents`) streams SSE indefinitely — `for env := range ch`
never returns on its own. A body-based check would hang reading that
response. Go's `net/http.ServeMux` already exposes exactly the primitive
this test needs, with no handler execution and no network:
`(*ServeMux).Handler(r *http.Request) (h http.Handler, pattern string)`
returns the registered pattern string (e.g. `"GET /sessions/{id}"`) for a
matching request, or `""` if nothing matches — instant, exact, and it's
literally what "must be a registered mux route" means.

This requires one small, backward-compatible seam in `api.go`: split `New`
so the mux-building half is reachable without the CORS wrapper.

```go
// New returns the Router's HTTP handler. See Options's doc for the
// nil-tolerant behavior of individual fields.
func New(opts Options) http.Handler {
	return withCORS(NewMux(opts))
}

// NewMux builds the Router's route table without the CORS wrapper —
// exported so tests (e.g. the route-manifest test) can check which
// patterns are registered via (*http.ServeMux).Handler, without an HTTP
// round trip or executing any handler body. Application code should use
// New; this exists for tests.
func NewMux(opts Options) *http.ServeMux {
	if opts.Preflight == nil {
		opts.Preflight = preflight.StubChecker{}
	}
	if opts.StaticHandler == nil {
		opts.StaticHandler = http.NotFoundHandler()
	}
	s := &server{ /* unchanged field list */ }
	mux := http.NewServeMux()
	// ... every existing mux.HandleFunc / mux.Handle call, unchanged ...
	return mux
}
```

Move the entire existing body of `New` (defaulting + all `mux.HandleFunc`
calls + `mux.Handle("/", ...)` + `return mux`) into `NewMux` verbatim; `New`
becomes the two-line wrapper above. No behavior change to `New` — same
registration order, same defaults, same final `withCORS` wrap.

Test file:

```go
package api_test

import (
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/services/router/internal/api"
)

// routeManifest mirrors every real (currently-registered) fetch call site
// in ui/src/router/client.ts's FetchRouterClient — kept in one place, by
// hand, so a route the UI client calls without a matching Router route
// fails here instead of silently 200'ing through the static SPA fallback
// (services/router/internal/static). See knownGapRoutes below for the
// client.ts methods deliberately excluded because they call routes that
// don't exist yet, by design.
var routeManifest = []struct{ method, path, wantPattern string }{
	{"GET", "/sessions/x", "GET /sessions/{id}"},
	{"GET", "/sessions/x/transcript", "GET /sessions/{id}/transcript"},
	{"POST", "/sessions/x/bounce", "POST /sessions/{id}/bounce"},
	{"GET", "/sessions/x/files", "GET /sessions/{id}/files"},
	{"GET", "/sessions", "GET /sessions"},
	{"POST", "/sessions", "POST /sessions"},
	{"GET", "/events", "GET /events"},
	// ... continue for every other unique (method, pattern) pair client.ts
	// hits, per the table above and a fresh read of client.ts. This is the
	// bulk of the file; not fully transcribed here.
}

// knownGapRoutes: client.ts methods that call routes not registered today,
// by design (see services/router/HANDOFF.md's route-manifest section for
// why each is excluded from routeManifest). This test fails the day one of
// these lands as a real route — move its row into routeManifest instead of
// deleting it here.
var knownGapRoutes = []struct{ method, path string }{
	{"GET", "/sessions/x/stream"},
	{"POST", "/sessions/x/messages"},
	{"POST", "/sessions/x/interrupt"},
	{"GET", "/mentionables"},
	{"GET", "/sessions/x/commits"},
	{"GET", "/sessions/x/changes"},
	{"POST", "/system/open-editor"},
	{"POST", "/sessions/x/merge"},
}

func TestRouteManifestMatchesRouterClient(t *testing.T) {
	mux := api.NewMux(api.Options{})
	for _, rt := range routeManifest {
		req := httptest.NewRequest(rt.method, rt.path, nil)
		if _, pattern := mux.Handler(req); pattern != rt.wantPattern {
			t.Errorf("%s %s: mux pattern = %q, want %q (ui/src/router/client.ts calls this route)", rt.method, rt.path, pattern, rt.wantPattern)
		}
	}
}

func TestKnownGapRoutesStillUnregistered(t *testing.T) {
	mux := api.NewMux(api.Options{})
	for _, rt := range knownGapRoutes {
		req := httptest.NewRequest(rt.method, rt.path, nil)
		if _, pattern := mux.Handler(req); pattern != "" {
			t.Errorf("%s %s: now registered as %q — move this row from knownGapRoutes to routeManifest in this file", rt.method, rt.path, pattern)
		}
	}
}
```

Note this test needs none of the other `api_test.go` fixture machinery
(`datatest.Start`, `supervisor.New`, `dataclient`) — `api.NewMux(api.Options{})`
with every field nil is enough to build the route table, since registration
never calls a handler. That's a deliberate benefit of the `NewMux` split:
this test is fast and has zero external dependencies, unlike almost every
other test in this package.

### 5. `services/router/README.md`

Update the "Serving the UI" section's bullet "Any request path that isn't a
real file under the dist directory falls back to `index.html`" — it's no
longer accurate. Replace with something describing the navigation-shaped
gate and pointing at the new tests (`internal/static`'s
`TestNonHTMLAcceptGetsJSON404`/`TestBareFetchDefaultAcceptGetsJSON404`,
`internal/api`'s `TestAPIShapedUnregisteredPathGetsJSON404`), and add a
one-line mention of the route-manifest test's existence and purpose (a
reader debugging "why did my new UI call 404" should find it from here).

## Risks / things the reviewer should scrutinize

- **The `Accept: text/html` substring check, not full q-value parsing.**
  Deliberately simple — see Decision section. A request with `Accept:
  application/json, text/html;q=0.01` would be (mis)treated as
  navigation-shaped under this design, since it contains the substring
  regardless of quality. No real caller in this codebase constructs such a
  header today; flagging as a known simplification, not fixing pre-emptively.
- **`knownGapRoutes` is hand-maintained and can drift from `client.ts`
  independently of `routeManifest`.** `TestKnownGapRoutesStillUnregistered`
  catches the "a gap route landed but nobody moved its row" direction; it
  does *not* catch "a client.ts method's path string changed" (e.g. a typo
  fix) unless someone also updates the table. Same limitation `routeManifest`
  itself has — inherent to a hand-maintained mirror, not something to solve
  in this task.
- **`api.NewMux` is a new exported symbol whose only real caller is a
  test.** Acceptable here (same pattern as exporting a constructor for
  testability elsewhere in Go), but flag it in review — if unwanted, the
  alternative is an HTTP-based check with an explicit `context.WithTimeout`
  + immediate `resp.Body.Close()` without reading to avoid the `GET /events`
  hang, but that's strictly more code and slower for no behavioral gain.

## Verification

1. `make check` (root) — lint + `go test -race ./...` + UI lint + UI test.
2. Manually confirm the two acceptance-criteria requests against a running
   Router (`scripts/app.sh` or `make run`): `curl -H 'Accept:
   application/json' localhost:8080/sessions/nope/what` → JSON 404; a real
   browser navigation to a deep link (or `curl -H 'Accept: text/html'
   localhost:8080/deep/link`) → `index.html`.

## Executor notes

Implemented the design above as written, with no deviations from the
production-code decisions (navigation gate, JSON 404 shape, `New`/`NewMux`
split). Touched exactly the five files this doc names plus the new
`internal/api/route_manifest_test.go`.

- `internal/static/static.go`: added `isNavigationRequest` and
  `writeNotFoundJSON`, branched the fallback on the gate. Renamed the
  loop-local `path` var to `fsPath` since it shadowed the `path/filepath`
  import once `writeNotFoundJSON` needed `r.URL.Path` in the same closure.
- `internal/static/static_test.go` and `internal/api/static_test.go`: added
  a `getWithAccept` helper (bare `get`/`http.Get` calls default to
  `Accept: */*`, which no longer falls back to `index.html`) and rewrote the
  two pre-existing fallback tests to send an explicit `text/html` Accept
  header. Added the four/two new tests this doc specifies.
- `internal/api/api.go`: split `New` into a thin CORS-wrapped `New` plus an
  exported `NewMux` that builds the route table, unchanged otherwise.
- `internal/api/route_manifest_test.go`: built `routeManifest` (78 rows) by
  hand-walking every `FetchRouterClient` method in `ui/src/router/client.ts`
  against `api.go`'s route table, deduping methods that share a route (e.g.
  `listTasks`/`listAllTasks` → `GET /tasks`). `knownGapRoutes` holds the 8
  documented forward-built gaps.
- `README.md`: replaced the stale "Serving the UI" bullet per the doc's
  Design §5.

**Deviation to scrutinize**: `TestKnownGapRoutesStillUnregistered` checks
`gotPattern == tc.pattern` rather than `gotPattern == ""` as this doc's
sketch does. `NewMux` always registers `mux.Handle("/", opts.StaticHandler)`
as a catch-all, so `(*http.ServeMux).Handler` never returns an empty
pattern for *any* path, registered or not — it returns `"/"` for anything
unmatched. The doc's sketch as written would make this test always pass
regardless of whether a gap route became real, silently defeating its own
purpose. Checking against the specific expected dedicated pattern instead
preserves the intended guarantee: the test only fails when a gap route's
exact pattern becomes registered.

`make check` passed clean: `go test -race ./...` (all packages, including
the new route-manifest tests against all 78 manifest + 8 gap rows),
`golangci-lint run ./...`, UI lint, and 707 UI tests. Did not additionally
verify the acceptance criteria against a live `scripts/app.sh` Router
(no browser available in this environment) — covered instead by
`TestAPIShapedUnregisteredPathGetsJSON404` and
`TestBrowserNavigationToDeepLinkGetsIndex` in `internal/api/static_test.go`,
which exercise the same two requests through the real `api.New` + CORS
wrapper + a real UI dist fixture end-to-end.
