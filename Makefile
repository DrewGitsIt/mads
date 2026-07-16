# mads — root Makefile. Every gate here also runs in CI; keep them identical.

GOBIN := $(CURDIR)/bin
LINT  := $(GOBIN)/golangci-lint

.PHONY: build test lint check tidy ui-deps app e2e-smoke release-gate

build: ui-deps
	go build ./...
	go build -o $(GOBIN)/mads ./cmd/mads
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

# Release gate: cross-platform build matrix + install/update smokes against
# the native candidate. Kept out of `check`/CI's fast `test` job (multi-
# minute cross-compile) — see scripts/release-gate.sh.
release-gate:
	sh scripts/release-gate.sh
	go test -tags release_gate -count=1 -v ./scripts/releasegate/...
