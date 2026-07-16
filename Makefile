# mads — root Makefile. Every gate here also runs in CI; keep them identical.

GOBIN := $(CURDIR)/bin
LINT  := $(GOBIN)/golangci-lint

.PHONY: build test lint check tidy ui-deps

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
