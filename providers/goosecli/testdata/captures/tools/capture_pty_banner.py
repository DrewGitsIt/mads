#!/usr/bin/env python3
"""Capture the raw startup banner bytes of `goose session` under a PTY.

Usage: capture_pty_banner.py <abs-cwd> <out.raw>

Spawns `goose session` with a pseudo-terminal as stdin/stdout/stderr and
records everything the process writes for READ_SECONDS (or until the output
goes quiet after the input prompt renders), byte-for-byte — ANSI escapes
intact. The committed bytes are what the pty.go banner-regex task pins
against. Environment: GOOSE_BIN (default ~/.local/bin/goose).
"""
import os
import pty
import select
import signal
import subprocess
import sys
import time

GOOSE_BIN = os.environ.get("GOOSE_BIN", os.path.expanduser("~/.local/bin/goose"))
READ_SECONDS = 15
QUIET_AFTER = 3  # stop early once output has been silent this long


def main():
    cwd, out_path = sys.argv[1], sys.argv[2]
    master, slave = pty.openpty()
    proc = subprocess.Popen(
        [GOOSE_BIN, "session"],
        cwd=cwd,
        stdin=slave,
        stdout=slave,
        stderr=slave,
        env={**os.environ, "TERM": "xterm-256color", "COLUMNS": "120", "LINES": "40"},
        start_new_session=True,
    )
    os.close(slave)

    chunks = []
    deadline = time.time() + READ_SECONDS
    last_data = time.time()
    while time.time() < deadline:
        r, _, _ = select.select([master], [], [], 0.5)
        if r:
            try:
                data = os.read(master, 65536)
            except OSError:
                break
            if not data:
                break
            chunks.append(data)
            last_data = time.time()
        elif chunks and time.time() - last_data > QUIET_AFTER:
            break

    with open(out_path, "wb") as f:
        f.write(b"".join(chunks))

    try:
        os.write(master, b"\x04")  # Ctrl-D: ask the REPL to exit
        time.sleep(1)
    except OSError:
        pass
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGKILL)
    os.close(master)
    print(f"captured {sum(len(c) for c in chunks)} bytes", file=sys.stderr)


if __name__ == "__main__":
    main()
