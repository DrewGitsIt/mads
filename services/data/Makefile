.PHONY: build run test

# Same as `make data` from the repo root: writes bin/data. Safe to run
# while mads is up — see the root Makefile for how each binary reaches a
# running instance.
build:
	go build -o ../../bin/data .

run:
	go run .

test:
	go test -race ./...
