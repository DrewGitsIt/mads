#!/usr/bin/env python3
"""Probe an ACP agent's native compaction: does a "/compact" prompt (the
command its available_commands_update advertises) run a real compaction pass,
and what does "/compact <focus text>" do?

  probe_compact.py <agent-cmd...> -- <abs-cwd> <out.jsonl>

Wire: initialize -> session/new -> session/prompt (a short warm-up turn, so
there is history to compact) -> session/prompt "/compact" -> session/prompt
"/compact keep only the word PINEAPPLE" -> session/close. Every line, both
directions, is logged as {"dir":"c2s"|"s2c","msg":{...}} like the sibling
capture tools. Permission requests are cancelled (the warm-up asks for no
tools). Point it only at an agent wired to a free (local) model.
"""
import json
import queue
import subprocess
import sys
import threading

TURN_TIMEOUT = 300


def main():
    argv = sys.argv[1:]
    sep = argv.index("--")
    cmd, (cwd, out_path) = argv[:sep], argv[sep + 1:]
    log = open(out_path, "w")
    lock = threading.Lock()
    proc = subprocess.Popen(cmd, cwd=cwd, stdin=subprocess.PIPE,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    pending = {}

    def write_log(d, msg):
        with lock:
            log.write(json.dumps({"dir": d, "msg": msg}) + "\n")
            log.flush()

    def send(msg):
        write_log("c2s", msg)
        proc.stdin.write(json.dumps(msg) + "\n")
        proc.stdin.flush()

    def reader():
        for line in proc.stdout:
            line = line.strip()
            if not line:
                continue
            msg = json.loads(line)
            write_log("s2c", msg)
            if "id" in msg and "method" in msg:
                send({"jsonrpc": "2.0", "id": msg["id"],
                      "result": {"outcome": {"outcome": "cancelled"}}})
            elif "id" in msg and msg["id"] in pending:
                pending.pop(msg["id"]).put(msg)

    threading.Thread(target=reader, daemon=True).start()
    seq = [0]

    def request(method, params, timeout=TURN_TIMEOUT):
        seq[0] += 1
        q = queue.Queue()
        pending[seq[0]] = q
        send({"jsonrpc": "2.0", "id": seq[0], "method": method, "params": params})
        return q.get(timeout=timeout)

    def prompt(sid, text):
        r = request("session/prompt", {"sessionId": sid,
                                       "prompt": [{"type": "text", "text": text}]})
        print(f"prompt {text!r} -> {json.dumps(r.get('result') or r.get('error'))}")

    try:
        request("initialize", {"protocolVersion": 1, "clientCapabilities": {
            "fs": {"readTextFile": False, "writeTextFile": False}}})
        sid = request("session/new", {"cwd": cwd, "mcpServers": []})["result"]["sessionId"]
        prompt(sid, "Reply with exactly: hello. Use no tools.")
        prompt(sid, "/compact")
        prompt(sid, "/compact keep only the word PINEAPPLE")
        try:
            request("session/close", {"sessionId": sid}, timeout=15)
        except Exception:
            pass
    finally:
        proc.stdin.close()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
        log.close()


if __name__ == "__main__":
    main()
