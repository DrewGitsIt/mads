# HANDOFF — W7.5 Router serves the UI same-origin

Executor stage output. This replaces the architect's plan (preserved in git history —
see `git log -- HANDOFF.md` — this file is the record of what was actually built).

## What was done

Followed the architect's design closely:

- New package `services/router/internal/static` (`static.go` + `static_test.go`,
  package-local tests). `static.New(distDir string) http.Handler` serves the built UI
  at `/`, falling back to `index.html` for any path that isn't a real file on disk (SPA
  client-side routing), and serves a plain "UI not built" placeholder page when the dist
  dir or its `index.html` is missing — Router startup never fails on an unbuilt UI.
- `internal/api.New`'s signature gained a final `staticHandler http.Handler` param
  (nil defaults to `http.NotFoundHandler()`, same nil-degrades-gracefully convention as
  `creds`/`preflightChecker`/`vaultRefs`). `mux.Handle("/", staticHandler)` registered
  right before `return withCORS(mux)`. Go's `net/http.ServeMux` pattern specificity means
  the catch-all `"/"` never shadows method+path API routes (`"GET /health"` etc.)
  regardless of registration order — proved by `TestStaticDoesNotShadowAPIRoutes` in the
  new `services/router/internal/api/static_test.go` integration test file.
- All existing `api.New(...)` call sites updated (`main.go`, `main_test.go`,
  `agents_test.go`, `events_test.go`, `api_test.go`, `credentials_test.go`) — trailing
  arg changed from the old `uiDist string` (`""`) to `nil`, no shared test helper
  introduced, per the plan.
- `main.go`: added `-ui-dist` flag (default `"ui/dist"`, literal — see Deviations),
  wired `static.New(*uiDist)` into `newMux`.
- `ui/electron/main.ts`: added `ROUTER_BASE_URL` const (reads `VITE_ROUTER_URL`, the
  same env var the renderer's `RouterClient` already reads, default
  `http://localhost:8080`); replaced the `DEV_SERVER_URL ? loadURL : loadFile` branch
  with `win.loadURL(DEV_SERVER_URL ?? ROUTER_BASE_URL)` — no `file://` branch kept, since
  `file://` sends `Origin: null` and would fail the Router's CORS policy; removed
  `webSecurity: false` and its comment entirely. `VITE_DEV_SERVER_URL` still wins when
  set, for the Vite dev-server loop.
- `vite.config.ts`'s `base: "./"` and `internal/api/cors.go` untouched, as directed.
- `scripts/app.sh` verified, not changed: it already starts the Data Service, then the
  Router (waiting for `/health`), then `npm --prefix ui start` — Router-before-Electron
  ordering already holds.
- Updated `services/router/README.md` (new "Serving the UI" section, `-ui-dist` flag
  documented in "Run it alone") and `ui/README.md` (launch flow: Electron loads the
  Router's origin, not a bundled `dist/index.html`).
- Tests: `services/router/internal/static/static_test.go` (index served, real asset
  served verbatim, unknown path falls back to index, placeholder when dist missing,
  placeholder when dist path is empty, and a build landing *after* construction is
  picked up on the next request — see Deviations) and
  `services/router/internal/api/static_test.go` (GET / serves the built index, GET
  /assets/app.js serves a real asset, unknown path falls back to index, GET /health is
  unaffected by a real UI dist being configured). `ui/electron/main.ts` exercised via
  `npm --prefix ui run build` (tsc + vite build + electron tsconfig build) — passes.
  `make check` passes (Go tests with `-race`, Go vet, UI lint, UI vitest: 172 files /
  695 tests).

## Deviations from the architect's plan (flagged for review)

1. **Per-request freshness check, not construction-time-only.** The plan said to stat
   `index.html` once inside `static.New` and permanently return a placeholder handler if
   missing. I check on every request instead. Reason: `scripts/app.sh` starts the Router
   (which constructs the static handler) *before* `npm --prefix ui start` builds
   `ui/dist` — a construction-time-only check would leave the Router serving a stale
   placeholder for the rest of its process lifetime even after the real build lands
   moments later, with no recovery short of a Router restart. The per-request check costs
   one extra `os.Stat` per request and is covered by
   `TestPicksUpBuildThatLandsAfterConstruction`. If this tradeoff is wrong for some
   reason I'm not seeing (e.g. `os.Stat` cost at scale, or a preference for the Router to
   fail loudly rather than self-heal), it's a one-line change back to a construction-time
   check plus removing that one test.
2. **`-ui-dist` default reverted to match the plan exactly.** I initially wrote a
   `defaultUIDist()` helper mirroring `defaultChildCmd`'s sibling-of-the-executable
   resolution (extending an existing pattern without being asked to). Caught this against
   the architect's explicit instruction to use a literal `"ui/dist"` default and document
   the cwd caveat rather than solve it, and reverted to the literal default. Final state
   matches the plan.

## What was not done / out of scope (per the plan, untouched)

No `embed.FS` — `ui/dist` is served from disk, rebuilds independently of the Go binary.
No changes to `ui/src/router/client.ts` or the renderer's `VITE_ROUTER_URL` default. No
changes to `internal/api/cors.go`. No custom "try API mux, fall back to static" wrapper —
relies on stdlib `ServeMux` pattern specificity instead.

## Review round 2

code-reviewer failed the first pass: `services/router/README.md` and `-ui-dist`'s help
text both claim `make run` launches from the repo root, but `services/router/Makefile`'s
`run` target was `go run .` — Make executes recipes with cwd set to the Makefile's own
directory, so that's `services/router`, not the repo root. `-ui-dist`'s cwd-relative
default (`ui/dist`) then resolved to `services/router/ui/dist` (never exists), silently
serving the "UI not built" placeholder even with a real `ui/dist` built at the repo root.

Fixed the Makefile target instead of the claim, since the claim is what every other
launch path (`scripts/app.sh`, a manually-run `bin/router` from repo root) actually
satisfies — the Makefile target was the outlier:

```make
run:
	cd ../.. && go run ./services/router
```

Verified by hand: built `ui/dist` at the repo root, ran `cd services/router && make run`
(on an alternate port to avoid clashing with an already-running router in this
environment), and confirmed `GET /` returns the real built `index.html`, not the
placeholder. `README.md`/`main.go`'s help text needed no wording change — the claim is now
true. Re-ran `make check` (root) and `npm --prefix ui run build` — both still green.

## For the reviewer to scrutinize

- Deviation 1 above (per-request vs. construction-time check) is the one substantive
  design departure — everything else matches the plan as written.
- `TestStaticDoesNotShadowAPIRoutes` is the load-bearing proof that mounting `"/"` on the
  same mux as the API routes is safe; worth independently confirming against the Go 1.26
  `ServeMux` docs if in doubt.
- `filepath.Clean(r.URL.Path)` joined into `distDir` is the path-traversal guard in
  `static.go` — `r.URL.Path` is always rooted (`/...`), so `Clean` can't produce a `..`
  that escapes above `distDir`. No explicit traversal test was added; this is standard
  Go stdlib behavior, not custom logic, but flagging it since it's security-relevant.
