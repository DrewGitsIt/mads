#!/usr/bin/env python3
"""Capture an ACP stdio transcript from an arbitrary ACP agent, for the
acprpc knob-fit validation (docs/DECLARATIVE-PROVIDERS-DESIGN.md, the
"second-agent validation" task in docs/goose-adapter-tasks.yaml).

Generalizes providers/goosecli/testdata/captures/tools/capture_acp.py:
same wire log format — every line, both directions, as
{"dir":"c2s"|"s2c","msg":{...}} JSONL, machine-replayable — but the agent
command, environment removals, mode selection, and scenario are flags.

  capture_acp_agent.py full  <abs-cwd> <out.jsonl> [opts] -- <agent argv...>
      initialize -> session/new (result pins modes/models/configOptions)
      [-> session/set_mode <--mode>] -> session/prompt (tool-using probe;
      answers session/request_permission by selecting the offered option
      whose kind is allow_once — never by hardcoded optionId)
      [-> session/prompt #2 + session/cancel mid-turn unless --skip-cancel]
      -> session/close (only if advertised). Prints sessionId on stdout.

  capture_acp_agent.py load <abs-cwd> <out.jsonl> --session <id> -- <argv...>
      Fresh process: initialize -> session/load -> session/close.

Options:
  --env-remove VAR   (repeatable) strip VAR from the child env — e.g.
                     CLAUDECODE, which makes claude-agent-acp misdetect a
                     nested session (the env_remove knob's evidence).
  --mode ID          session/set_mode after session/new.
  --prompt TEXT      override the tool-using probe prompt.
  --skip-cancel      omit the second-prompt + session/cancel scenario.
  --turn-timeout N   seconds per turn (default 240).

fs/terminal client capabilities are advertised false, so the only expected
server->client request is session/request_permission; anything else gets
-32601, and the log line is the evidence.
"""
import argparse
import json
import os
import queue
import subprocess
import sys
import threading
import time


class AcpClient:
    def __init__(self, argv, cwd, log_path, env_remove):
        self.log = open(log_path, "w")
        self.log_lock = threading.Lock()
        env = {k: v for k, v in os.environ.items() if k not in env_remove}
        self.proc = subprocess.Popen(
            argv,
            cwd=cwd,
            env=env,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=open(log_path + ".stderr.txt", "w"),
            text=True,
        )
        self.next_id = 0
        self.pending = {}
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
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                self._write_log("s2c-nonjson", {"raw": line[:2000]})
                continue
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

    def request(self, method, params, timeout=120):
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

    def wait_for_update(self, session_id, kinds, timeout):
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                msg = self.notifications.get(timeout=max(0.1, deadline - time.time()))
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

    def close(self, session_id=None, supports_close=True):
        try:
            if session_id and supports_close:
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
    return resp["result"]


def run_full(args):
    c = AcpClient(args.agent_argv, args.cwd, args.out, set(args.env_remove))
    session_id = None
    caps = {}
    try:
        caps = initialize(c).get("agentCapabilities", {}) or {}
        resp = c.request("session/new", {"cwd": args.cwd, "mcpServers": []}, timeout=180)
        if "error" in resp:
            print(f"session/new error (captured): {resp['error']}", file=sys.stderr)
            return 1
        session_id = resp["result"]["sessionId"]
        time.sleep(3)  # collect unsolicited post-new updates

        if args.mode:
            c.request("session/set_mode", {"sessionId": session_id, "modeId": args.mode})

        q = c.request_async(
            "session/prompt",
            {
                "sessionId": session_id,
                "prompt": [{"type": "text", "text": args.prompt}],
            },
        )
        resp = q.get(timeout=args.turn_timeout)
        print(f"turn 1 stopReason: {resp.get('result', resp).get('stopReason')}",
              file=sys.stderr)

        if not args.skip_cancel:
            q = c.request_async(
                "session/prompt",
                {
                    "sessionId": session_id,
                    "prompt": [
                        {
                            "type": "text",
                            "text": "Count slowly from 1 to 50, one number per line.",
                        }
                    ],
                },
            )
            try:
                c.wait_for_update(
                    session_id,
                    {"agent_message_chunk", "agent_thought_chunk", "tool_call"},
                    timeout=60,
                )
            except TimeoutError:
                pass
            c.notify("session/cancel", {"sessionId": session_id})
            resp = q.get(timeout=60)
            print(
                f"cancel stopReason: {resp.get('result', resp).get('stopReason')}",
                file=sys.stderr,
            )

        print(session_id)
        return 0
    finally:
        sc = caps.get("sessionCapabilities") or {}
        c.close(session_id, supports_close=bool(sc.get("close", True)))


def run_load(args):
    c = AcpClient(args.agent_argv, args.cwd, args.out, set(args.env_remove))
    try:
        initialize(c)
        resp = c.request(
            "session/load",
            {"sessionId": args.session, "cwd": args.cwd, "mcpServers": []},
            timeout=120,
        )
        print(f"load: {'result' if 'result' in resp else resp.get('error')}",
              file=sys.stderr)
        time.sleep(5)  # drain replayed updates into the log
        return 0
    finally:
        c.close(args.session)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("scenario", choices=["full", "load"])
    ap.add_argument("cwd")
    ap.add_argument("out")
    ap.add_argument("--env-remove", action="append", default=[])
    ap.add_argument("--mode")
    ap.add_argument("--session")
    ap.add_argument("--skip-cancel", action="store_true")
    ap.add_argument("--turn-timeout", type=int, default=240)
    ap.add_argument(
        "--prompt",
        default="Create a file named probe.txt in the current directory containing "
        "exactly the text acp-probe, then tell me you did it.",
    )
    ap.add_argument("agent_argv", nargs="+")
    args = ap.parse_args()
    if args.scenario == "load" and not args.session:
        ap.error("load requires --session")
    sys.exit(run_full(args) if args.scenario == "full" else run_load(args))


if __name__ == "__main__":
    main()
