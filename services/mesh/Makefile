.PHONY: build build-linux run test

# Same as `make mesh` from the repo root: writes bin/mesh. A new process, so
# deploying it never restarts the Router or its sessions.
build:
	go build -o ../../bin/mesh .

# Cross-compile for a Linux peer that has no Go toolchain of its own.
build-linux:
	GOOS=linux GOARCH=amd64 go build -o ../../bin/linux-amd64/mesh .

run:
	go run .

test:
	go test -race ./...
