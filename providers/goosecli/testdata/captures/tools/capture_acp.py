#!/usr/bin/env python3
"""Capture an ACP stdio transcript from `goose acp` for goosecli testdata.

Speaks newline-delimited JSON-RPC 2.0 to a spawned `goose acp --with-builtin
developer` and logs every wire line, both directions, to a JSONL file as
{"dir":"c2s"|"s2c","msg":{...}} — machine-replayable by later tasks
(fakegoose scripting, wire.go verification).

Two modes:

  capture_acp.py full <abs-cwd> <out.jsonl>
      initialize -> session/new -> session/set_mode(approve)
      -> session/set_config_option(model) -> _goose/unstable system-prompt/set
      -> session/prompt (tool-using; answers the session/request_permission
         server request by selecting the offered option whose kind is
         allow_once) -> session/prompt #2 + session/cancel mid-turn
      -> session/close. Prints the sessionId on stdout.

  capture_acp.py load <abs-cwd> <out.jsonl> <sessionId>
      Fresh process: initialize -> session/load (replayed session/update
      notifications arrive before the response resolves) -> session/close.

Environment: GOOSE_BIN (default ~/.local/bin/goose), GOOSE_ACP_MODEL
(default qwen3.8). Requires a working goose provider; run turns take
10-60s+ on a local ollama model, hence the generous timeouts.
"""
import json
import os
import queue
import subprocess
import sys
import threading
import time

GOOSE_BIN = os.environ.get("GOOSE_BIN", os.path.expanduser("~/.local/bin/goose"))
MODEL = os.environ.get("GOOSE_ACP_MODEL", "qwen3.8")
TURN_TIMEOUT = 300  # seconds; local model turns are slow
RPC_TIMEOUT = 120


class AcpClient:
    def __init__(self, cwd, log_path):
        self.log = open(log_path, "w")
        self.log_lock = threading.Lock()
        self.proc = subprocess.Popen(
            [GOOSE_BIN, "acp", "--with-builtin", "developer"],
            cwd=cwd,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
        )
        self.next_id = 0
        self.pending = {}  # request id -> queue of the one response
        self.notifications = queue.Queue()
        self.reader = threading.Thread(target=self._read_loop, daemon=True)
        self.reader.start()

    def _write_log(self, direction, msg):
        with self.log_lock:
            self.log.write(json.dumps({"dir": direction, "msg": msg}) + "\n")
            self.log.flush()

    def _send(self, msg):
        self._write_log("c2s", msg)
        self.proc.stdin.write(json.dumps(msg) + "\n")
        self.proc.stdin.flush()

    def _read_loop(self):
        for line in self.proc.stdout:
            line = line.strip()
            if not line:
                continue
            msg = json.loads(line)
            self._write_log("s2c", msg)
            if "id" in msg and "method" in msg:
                self._handle_server_request(msg)
            elif "id" in msg:
                q = self.pending.pop(msg["id"], None)
                if q is not None:
                    q.put(msg)
            else:
                self.notifications.put(msg)

    def _handle_server_request(self, msg):
        # The only server->client request we expect is session/request_permission
        # (fs/terminal callbacks can't come: we advertise those capabilities
        # false). Select by option *kind*, never by hardcoded optionId.
        if msg["method"] == "session/request_permission":
            options = msg["params"]["options"]
            chosen = next(
                (o for o in options if o.get("kind") == "allow_once"), None
            ) or next((o for o in options if o.get("kind") == "allow_always"), None)
            if chosen is None:
                result = {"outcome": {"outcome": "cancelled"}}
            else:
                result = {
                    "outcome": {"outcome": "selected", "optionId": chosen["optionId"]}
                }
            self._send({"jsonrpc": "2.0", "id": msg["id"], "result": result})
        else:
            self._send(
                {
                    "jsonrpc": "2.0",
                    "id": msg["id"],
                    "error": {"code": -32601, "message": "method not found"},
                }
            )

    def request(self, method, params, timeout=RPC_TIMEOUT):
        self.next_id += 1
        rid = self.next_id
        q = queue.Queue()
        self.pending[rid] = q
        self._send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
        return q.get(timeout=timeout)

    def request_async(self, method, params):
        self.next_id += 1
        rid = self.next_id
        q = queue.Queue()
        self.pending[rid] = q
        self._send({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
        return q

    def notify(self, method, params):
        self._send({"jsonrpc": "2.0", "method": method, "params": params})

    def wait_for_update(self, session_id, kinds, timeout=TURN_TIMEOUT):
        """Block until a session/update notification for session_id whose
        update.sessionUpdate is in kinds arrives (reader logs everything
        regardless)."""
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                msg = self.notifications.get(timeout=deadline - time.time())
            except queue.Empty:
                break
            if msg.get("method") != "session/update":
                continue
            p = msg.get("params", {})
            if p.get("sessionId") != session_id:
                continue
            if p.get("update", {}).get("sessionUpdate") in kinds:
                return msg
        raise TimeoutError(f"no {kinds} update within {timeout}s")

    def close(self, session_id=None):
        try:
            if session_id:
                self.request("session/close", {"sessionId": session_id}, timeout=15)
        except Exception:
            pass
        try:
            self.proc.stdin.close()
        except Exception:
            pass
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()
        self.log.close()


def initialize(c):
    resp = c.request(
        "initialize",
        {
            "protocolVersion": 1,
            "clientCapabilities": {
                "fs": {"readTextFile": False, "writeTextFile": False}
            },
        },
    )
    assert "result" in resp, resp
    return resp


def run_full(cwd, out_path):
    c = AcpClient(cwd, out_path)
    session_id = None
    try:
        initialize(c)

        resp = c.request("session/new", {"cwd": cwd, "mcpServers": []})
        session_id = resp["result"]["sessionId"]
        time.sleep(3)  # collect any unsolicited post-new updates

        c.request("session/set_mode", {"sessionId": session_id, "modeId": "approve"})

        # Flat value first; if the server rejects it, the error line is
        # evidence — retry with the nested shape.
        resp = c.request(
            "session/set_config_option",
            {"sessionId": session_id, "configId": "model", "value": MODEL},
        )
        if "error" in resp:
            c.request(
                "session/set_config_option",
                {
                    "sessionId": session_id,
                    "configId": "model",
                    "value": {"value": MODEL},
                },
            )

        c.request(
            "_goose/unstable/session/system-prompt/set",
            {
                "sessionId": session_id,
                "mode": "append",
                "key": "mads-agent",
                "text": "You are being driven by an automated capture harness.",
            },
        )

        # Turn 1: tool-using prompt in approve mode -> expect the
        # session/request_permission round trip (answered by the reader).
        q = c.request_async(
            "session/prompt",
            {
                "sessionId": session_id,
                "prompt": [
                    {
                        "type": "text",
                        "text": "Use the shell tool to run exactly this command: "
                        "echo acp-capture-ok. Then tell me what it printed.",
                    }
                ],
            },
        )
        resp = q.get(timeout=TURN_TIMEOUT)
        print(
            f"turn1 stopReason={resp.get('result', {}).get('stopReason')!r} "
            f"usage={'usage' in resp.get('result', {})}",
            file=sys.stderr,
        )

        # Turn 2: cancel mid-turn. Drain stale turn-1 notifications first so we
        # wait on *fresh* chunks — cancelling before the run is active is
        # silently ignored by goose. Cancel only after several chunks: goose
        # sets stopReason:"cancelled" only if another stream event arrives
        # after the cancel token trips (server.rs prompt loop); cancelling on
        # the very first chunk can end the stream with no further event and
        # resolve as "end_turn" (observed — see README).
        while not c.notifications.empty():
            c.notifications.get_nowait()
        q = c.request_async(
            "session/prompt",
            {
                "sessionId": session_id,
                "prompt": [
                    {
                        "type": "text",
                        "text": "Count slowly from 1 to 100, one number per line.",
                    }
                ],
            },
        )
        for _ in range(12):
            c.wait_for_update(
                session_id, {"agent_message_chunk", "agent_thought_chunk"}
            )
        c.notify("session/cancel", {"sessionId": session_id})
        resp = q.get(timeout=TURN_TIMEOUT)
        print(
            f"turn2 stopReason={resp.get('result', {}).get('stopReason')!r}",
            file=sys.stderr,
        )
    finally:
        c.close(session_id)
    print(session_id)


def run_load(cwd, out_path, session_id):
    c = AcpClient(cwd, out_path)
    try:
        initialize(c)
        # Deliberately try the minimal shape first: at 1.48.0 it is rejected
        # ("missing field `mcpServers`") — the error line is committed evidence
        # that session/load requires mcpServers, unlike what the ACP docs imply.
        resp = c.request(
            "session/load",
            {"sessionId": session_id, "cwd": cwd},
            timeout=TURN_TIMEOUT,
        )
        if "error" in resp:
            resp = c.request(
                "session/load",
                {"sessionId": session_id, "cwd": cwd, "mcpServers": []},
                timeout=TURN_TIMEOUT,
            )
        print(f"load response keys: {sorted(resp)}", file=sys.stderr)
        time.sleep(2)
    finally:
        c.close(session_id)


def main():
    mode = sys.argv[1]
    cwd, out_path = sys.argv[2], sys.argv[3]
    if mode == "full":
        run_full(cwd, out_path)
    elif mode == "load":
        run_load(cwd, out_path, sys.argv[4])
    else:
        sys.exit(f"unknown mode {mode!r}")


if __name__ == "__main__":
    main()
