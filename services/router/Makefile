.PHONY: build run test

# Same as `make router` from the repo root: writes bin/router. Safe to run
# while mads is up — see the root Makefile for how each binary reaches a
# running instance.
build:
	go build -o ../../bin/router .

run:
	cd ../.. && go run ./services/router

test:
	go test -race ./...
