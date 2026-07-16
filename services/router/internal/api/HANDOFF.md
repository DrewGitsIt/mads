# HANDOFF: Router CORS for the browser dev loop

## What was done

Implemented per the architect's plan in this file (now superseded by this
executor summary) with no deviations:

1. **`services/router/internal/api/cors.go`** (new) — `withCORS` middleware.
   Matches `Origin` against `^https?://(localhost|127\.0\.0\.1)(:[0-9]+)?$`;
   on match, echoes the origin in `Access-Control-Allow-Origin`, sets `Vary:
   Origin`, `Access-Control-Allow-Methods: GET, POST, PUT, DELETE, OPTIONS`,
   `Access-Control-Allow-Headers: Content-Type, X-Mads-Actor`. Non-matching
   origins get none of these headers. `OPTIONS` requests always get a bare
   204 (matching or not — no route in the mux handles OPTIONS on any
   pattern, so this replaces what would otherwise be a 404).
2. **`services/router/internal/api/api.go`** — `New()`'s final line changed
   from `return mux` to `return withCORS(mux)`. No other change.
3. **`services/router/internal/api/cors_test.go`** (new) — six tests:
   preflight (204 + all three headers correct), simple GET, simple POST
   (header present on the actual response, not just preflight), a
   non-5173 port variation (`127.0.0.1:5173` and `localhost:44123`) to prove
   "any port" isn't hardcoded, a non-localhost origin getting no
   `Access-Control-Allow-Origin` header at all (checked via presence in
   `resp.Header`, not empty-string equality), and cross-origin `GET /events`
   (SSE) carrying the header on the initial response.
4. **`services/router/README.md`** — new `## CORS` section between `## API
   contract` and `## Critical paths`, covering why, the policy (no
   wildcard, no flag), and the SSE header-timing note.

No `-cors` flag/env was added — the task said to add one only if a
default-on localhost policy is contentious, and nothing surfaced during
implementation that warranted it.

## Verification

- `go test -race ./internal/api/... -run TestCORS -v` — all 6 new tests
  pass.
- `go test -race ./...` (router module) — all existing packages still pass,
  no regressions.
- `make lint` — 0 issues.
- `make check` (root) — lint + `go test -race ./...` + UI lint + UI test —
  exit 0, 171 UI test files / 676 UI tests passed, no Go test failures.
- Grepped the whole repo for `MethodOptions`/`"OPTIONS"` before wiring the
  unconditional-204 behavior — the only hits are in the new `cors.go`/
  `cors_test.go` themselves, confirming nothing else depends on OPTIONS
  404ing.

## Things the reviewer should scrutinize

- **Unconditional OPTIONS 204, even for non-localhost origins and
  non-existent paths.** This is intentional per the plan (the mux has no
  OPTIONS route, so an unhandled preflight would otherwise 404), but it's a
  behavior change beyond "add CORS": any `OPTIONS` request to the Router,
  from anywhere, on any path, now returns 204 instead of 404. Confirmed via
  repo-wide grep that nothing currently depends on the old 404, but that's
  a point-in-time check, not a guarantee.
- **Header timing on SSE** relies on `withCORS` wrapping the *entire* mux
  and setting headers before calling `next.ServeHTTP` — verified by the new
  `TestCORSStreamEventsCrossOrigin` test, but if `handleStreamEvents` (or
  any future handler) is ever moved in front of this middleware, that
  invariant breaks silently (headers would still get set, just via
  `w.Header().Set` after `WriteHeader` — a no-op in Go's `net/http`, so it
  would fail quietly rather than erroring).
- No `-cors` flag was added. If a reviewer wants an escape hatch (e.g. to
  fully disable CORS in some deployment), that's a follow-up, not done
  here.
