.PHONY: build run test

# Same as `make session` from the repo root: writes bin/session. Safe to run
# while mads is up — see the root Makefile for how each binary reaches a
# running instance.
build:
	go build -o ../../bin/session .

run:
	go run .

test:
	go test -race ./...
