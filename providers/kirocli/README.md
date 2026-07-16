# providers/kirocli

The second provider adapter (spec §11, work item W7.1): kiro-cli driven
through its `acp` JSON-RPC agent for interactive sessions, `kiro-cli chat
--no-interactive` for one-shots, `--resume-id` for reattach, and ACP's
`session/load` replay for normalized transcript history.

**Every claim below is sourced from kiro-cli's public docs
(kiro.dev/docs/cli/*), its GitHub issues, and agentclientprotocol.com — not
from running the binary.** kiro-cli is not installed on this machine. Until
someone runs the real-binary smoke test (see bottom), treat this as
best-effort research, not fact.

## Matrix, row by row

| Behavior | Support | Why |
|---|---|---|
| Interactive | **Native** | `kiro-cli chat` (interactive REPL, PTY) and `kiro-cli acp` (structured JSON-RPC agent over stdio) are both real, documented, first-class entry points. |
| Interrupt | **Native** | Ctrl-C is the standard REPL interrupt for `chat`. For ACP, the standard protocol's `session/cancel` notification is the documented mechanism for stopping an in-flight turn; Kiro's ACP docs describe `session/new`/`session/prompt`/`session/load` as "stable" core methods it implements per the public spec. |
| OneShot | **Native** | `kiro-cli chat --no-interactive --trust-all-tools "<prompt>"` is a real, documented non-interactive invocation flag. |
| Resume | **Native** | `kiro-cli chat --resume` / `--resume-id <ID>` / `--resume-picker` / `--list-sessions` are real, documented flags; ACP's `session/load` is the structured-transport equivalent. Sessions are auto-saved every turn. |
| Transcript | **Native** | Kiro's ACP agent advertises `loadSession: true`. See "Least-certain row" below. |
| ModelDiscovery | **Native** | `kiro-cli chat --list-models --format json` is a real, scriptable, structured command — a genuine improvement over claude-code's pinned-list Emulated declaration. |
| CostReporting | **Unsupported** | Kiro's billing is credit-based, not token-based ("you pay by request, not by token"). No documented flag or ACP field surfaces per-turn token counts; `/context` only shows *percentage* usage, not raw counts or cost. A third-party tool (`kiro-usage`) exists specifically because Kiro exposes no token/cost API and has to reverse-engineer a local SQLite store and approximate input tokens from character count. This is a genuine, citable gap — declared, not emulated with an invented number. |
| ACP | **Native** | `kiro-cli acp` is a real, documented, standards-compliant JSON-RPC 2.0 agent over stdio (newline-delimited JSON, matching the public Agent Client Protocol transport spec). |
| PTY | **Native** | `kiro-cli chat`'s interactive terminal REPL. |
| Branching | **Unsupported** | `/rewind` is real and does create a new conversation branch, but it is an interactive-only TUI turn-picker (arrow-key selection UI), not a scriptable flag or stable API. A GitHub issue on Kiro's ACP crate notes session-fork support is behind an `unstable_session_fork` feature flag and is not currently active (`sessionCapabilities` reports empty). No stable, scriptable surface exists to declare this Native or Emulated honestly. |
| CwdReroot | **Unsupported** | No documented flag or command changes a live session's working directory mid-session. |

Net shape vs. claude-code: kiro-cli is **more capable** on ModelDiscovery,
Interrupt, and Transcript (all Native here vs. claude-code's
Emulated/Native mix), and **less capable** on CostReporting (Unsupported vs.
claude-code's Native). Branching and CwdReroot are Unsupported for both. This
asymmetry is the point of W7.1 — the matrix is not a copy of claude-code's.

## Real, citable gaps beyond the Matrix

- **PTY sessions never learn their own ref.** kiro-cli does not print its
  session ID on exit from the `chat` REPL (kirodotdev/Kiro#7788, "Display
  session ID and resume command on session exit") — the same limitation
  claude-code's PTY transport has. `ptySession.Ref()` returns
  `StartOptions.Resume` when resuming, empty otherwise.
- **Tool permission prompts are auto-approved, not surfaced.** The real ACP
  protocol includes `session/request_permission` (a request the agent can
  send mid-turn to ask about a tool call). This adapter always responds
  `allow` — equivalent to running with `--trust-all-tools` at all times —
  rather than surfacing a platform-level approval prompt. That is out of
  scope for this pass, but it is a real, permanent behavior difference from
  a human using kiro-cli interactively, not just a testing shortcut.
- **Least-certain row: Transcript.** ACP's `loadSession: true` capability is
  generally understood to mean `session/load` replays a session's prior
  turns as `session/update` notifications before its own response resolves —
  that is the convention this adapter's `transcript.go` commits to. It is
  not directly confirmed for kiro-cli specifically. If kiro's `session/load`
  turns out not to replay history that way, this row should be downgraded to
  Unsupported, not left as a silently-broken Native claim.
- **Design-not-binary risk applies to every Native claim above.** A live
  GitHub issue already shows kiro's documented `session/prompt` field name
  was wrong at one point (`content` vs. `prompt`) — if kiro-cli's real ACP
  implementation has drifted from its own docs elsewhere, this design could
  be wrong in ways only a real binary would catch.

## Testing without kiro-cli installed

`fakekiro_test.go` re-execs the test binary as a scripted ACP/chat stand-in
(`MADS_FAKE_KIRO=1`, the standard `os/exec` TestMain trick) — real subprocess
spawn, real JSON-RPC request/response correlation, real `session/cancel`
mid-turn, real `session/load` replay across separate process spawns via a
shared `MADS_FAKE_KIRO_DIR`. `providers/conformance`'s full suite
(`TestConformanceAgainstScriptedBinary`) runs against that stand-in in CI —
no kiro-cli required, and no live LLM is ever called.

`TestRealKiroSmoke` is opt-in and exercises the actual binary:

```sh
MADS_KIRO_SMOKE=1 go test ./providers/kirocli -run Smoke
```

It skips cleanly if `kiro-cli` isn't on `PATH`. Running it with a real
install is the only way to confirm or refute this README's Matrix claims.
