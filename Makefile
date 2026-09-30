# mads — root Makefile. Every gate here also runs in CI; keep them identical.

GOBIN := $(CURDIR)/bin
LINT  := $(GOBIN)/golangci-lint

.PHONY: build test lint check tidy ui-deps app e2e-smoke ui-smoke release-gate
.PHONY: services ui mads data router session pipeline

build: ui-deps
	go build ./...
	go build -o $(GOBIN)/mads ./cmd/mads
	go build -o $(GOBIN)/data ./services/data
	go build -o $(GOBIN)/router ./services/router
	go build -o $(GOBIN)/session ./services/session
	go build -o $(GOBIN)/pipeline ./services/pipeline
	npm --prefix ui run build

test: ui-deps
	go test -race ./...
	npm --prefix ui run lint
	npm --prefix ui run test

lint: $(LINT)
	$(LINT) run ./...

check: lint test

tidy:
	go mod tidy

ui-deps:
	npm --prefix ui ci

# --- Targeted rebuilds ------------------------------------------------------
#
# `make build` is the all-or-nothing gate (and drags in `npm ci`). These
# targets rebuild one piece at a time against a *running* instance, which is
# the normal inner loop. They deliberately skip `ui-deps` — run `make ui-deps`
# once after a package-lock.json change, not on every rebuild.
#
# All of them are safe to run while mads is up: `go build` replaces the output
# file by unlink+write, so a live process keeps the binary it started with.
# What differs is how each one reaches a running instance.

# Every Go binary, no UI, no npm. The whole set is ~2s warm.
services: mads data router session pipeline

mads:
	go build -o $(GOBIN)/mads ./cmd/mads

# Picked up by the *next* pipeline run — the Router execs -pipeline-cmd fresh
# per spawn, so nothing needs restarting.
pipeline:
	go build -o $(GOBIN)/pipeline ./services/pipeline

# Picked up by the *next* session spawn (-session-cmd is exec'd fresh too).
# To move an already-running session onto the new binary without disturbing
# any other session:  curl -sX POST localhost:17373/sessions/<id>/bounce
session:
	go build -o $(GOBIN)/session ./services/session

# Needs a restart to take effect, and that restart is the disruptive one: the
# Router's SIGTERM handler terminates every session and pipeline child it
# supervises. SIGTERM only — a SIGKILL'd Router leaves its children alive as
# orphans (they run in their own process groups) while the next boot's orphan
# sweep marks their sessions inactive. Relaunch from the repo root: -ui-dist
# defaults to a cwd-relative ui/dist.
#   kill "$$(cat ~/.mads/router.pid)"
#   ./bin/router -addr localhost:17373 -session-cmd ./bin/session
router:
	go build -o $(GOBIN)/router ./services/router

# Needs a restart, but a survivable one: state lives in ~/.mads/mads.db and
# the Router's watchers re-subscribe with backoff. In-flight SSE streams break
# and resume from their last sequence number.
data:
	go build -o $(GOBIN)/data ./services/data

# No restart at all. The Router serves ui/dist off disk (it is not embedded in
# the binary) and re-checks it per request, so this plus a window reload
# (Ctrl+R / Cmd+R / View -> Reload) is the entire loop. Note that vite empties
# ui/dist before writing it: reloading *during* the build briefly shows the
# "UI not built" placeholder.
ui:
	npm --prefix ui run build

$(LINT):
	GOBIN=$(GOBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

# Local end-to-end entry point: backend up (if not already) + Electron app.
app:
	./scripts/app.sh

# Full-topology smoke: real data/router/session/mads binaries, a scripted
# fake-provider pipeline run, retro + cost metrics, zero orphan processes.
# Kept out of `check`/CI's fast `test` job (real OS processes, not sub-second)
# — see scripts/e2esmoke's package doc.
e2e-smoke:
	go test -tags e2e_smoke -count=1 -v ./scripts/e2esmoke/...

# Playwright journey suite: real data/router/session binaries on ephemeral
# ports, real UI build, chromium only. Separate from `make check`/CI's fast
# test job and from `make e2e-smoke` (Go-only, no browser) — see
# ui/e2e/HANDOFF.md and ui/README.md's "test layers" section. Browsers are
# cached at ~/.cache/ms-playwright on every OS: that is Playwright's Linux
# default, and PLAYWRIGHT_BROWSERS_PATH pins macOS there too instead of its
# ~/Library/Caches/ms-playwright default (an explicit PLAYWRIGHT_BROWSERS_PATH
# in the environment wins). Key CI's cache on that path plus the pinned
# Playwright version in ui/package-lock.json.
# VITE_ROUTER_URL="" bakes a relative (same-origin) router base into ui/dist
# instead of the localhost:17373 default (client.ts's ROUTER_BASE) — the
# Router serves this build itself on an ephemeral port, never 17373.
# `flock` around the install: the shared ~/.cache/ms-playwright browser
# cache is safe to reuse concurrently once populated (Playwright checks a
# marker file and no-ops), but a concurrent *cold* install from two
# worktrees races two extractions into the same target directory with no
# lock of its own — see playwright-core's downloadBrowserWithProgressBar.
# Serializing just this step (not the whole test run) avoids that without
# giving up the warm-cache fast path on every other run. macOS ships no
# flock(1), so there the install runs unlocked: the only exposure is two
# worktrees cold-installing at once, which a warm cache already rules out.
ui-smoke: export PLAYWRIGHT_BROWSERS_PATH ?= $(HOME)/.cache/ms-playwright
ui-smoke: ui-deps
	VITE_ROUTER_URL="" npm --prefix ui run build
	mkdir -p "$$PLAYWRIGHT_BROWSERS_PATH"
	if command -v flock >/dev/null 2>&1; then \
		flock "$$PLAYWRIGHT_BROWSERS_PATH/.install.lock" npx --prefix ui playwright install chromium; \
	else \
		npx --prefix ui playwright install chromium; \
	fi
	npm --prefix ui run test:e2e

# Release gate: cross-platform build matrix + install/update smokes against
# the native candidate. Kept out of `check`/CI's fast `test` job (multi-
# minute cross-compile) — see scripts/release-gate.sh.
release-gate:
	sh scripts/release-gate.sh
	go test -tags release_gate -count=1 -v ./scripts/releasegate/...
