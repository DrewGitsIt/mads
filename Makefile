# mads — root Makefile. Every gate here also runs in CI; keep them identical.

GOBIN := $(CURDIR)/bin
LINT  := $(GOBIN)/golangci-lint

.PHONY: build test lint check tidy

build:
	go build ./...
	go build -o $(GOBIN)/mads ./cmd/mads

test:
	go test -race ./...

lint: $(LINT)
	$(LINT) run ./...

check: lint test

tidy:
	go mod tidy

$(LINT):
	GOBIN=$(GOBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
