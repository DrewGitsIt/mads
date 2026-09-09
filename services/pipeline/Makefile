.PHONY: build run test

# Same as `make pipeline` from the repo root: writes bin/pipeline. Safe to run
# while mads is up — see the root Makefile for how each binary reaches a
# running instance.
build:
	go build -o ../../bin/pipeline .

run:
	go run .

test:
	go test -race ./...
