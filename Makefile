# mads — root Makefile. Every gate here also runs in CI; keep them identical.

GOBIN := $(CURDIR)/bin
LINT  := $(GOBIN)/golangci-lint

.PHONY: build test lint check tidy ui-deps app e2e-smoke ui-smoke release-gate

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
# cached at ~/.cache/ms-playwright (Linux default) — key CI's cache on that
# path plus the pinned Playwright version in ui/package-lock.json.
# VITE_ROUTER_URL="" bakes a relative (same-origin) router base into ui/dist
# instead of the localhost:8080 default (client.ts's ROUTER_BASE) — the
# Router serves this build itself on an ephemeral port, never 8080.
# `flock` around the install: the shared ~/.cache/ms-playwright browser
# cache is safe to reuse concurrently once populated (Playwright checks a
# marker file and no-ops), but a concurrent *cold* install from two
# worktrees races two extractions into the same target directory with no
# lock of its own — see playwright-core's downloadBrowserWithProgressBar.
# Serializing just this step (not the whole test run) avoids that without
# giving up the warm-cache fast path on every other run.
ui-smoke: ui-deps
	VITE_ROUTER_URL="" npm --prefix ui run build
	mkdir -p ~/.cache/ms-playwright
	flock ~/.cache/ms-playwright/.install.lock npx --prefix ui playwright install chromium
	npm --prefix ui run test:e2e

# Release gate: cross-platform build matrix + install/update smokes against
# the native candidate. Kept out of `check`/CI's fast `test` job (multi-
# minute cross-compile) — see scripts/release-gate.sh.
release-gate:
	sh scripts/release-gate.sh
	go test -tags release_gate -count=1 -v ./scripts/releasegate/...
