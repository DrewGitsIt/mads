# HANDOFF: Interactive permission prompts in chat mode (tsk_5ba67d309d339c47)

## Executor status: blocked, not implemented

The architect brief's entire mechanism rests on a flag that does not exist
on the installed `claude` CLI. This is the **second** time this exact task
has been handed to an executor with this fabrication (see prior attempt's
findings, not preserved in-branch — recorded in pipeline memory as
`mads-permission-prompt-tool-fabricated`). I re-verified independently
rather than trusting that record, and confirm it still holds.

### The claim

> claude -p supports delegating permission decisions to an MCP tool
> (`--permission-prompt-tool`).

### The verification

```sh
$ claude --version
2.1.215 (Claude Code)
$ claude --help 2>&1 | grep -i permission
  --allow-dangerously-skip-permissions  Enable bypassing all permission checks ...
  --dangerously-skip-permissions        Bypass all permission checks. ...
  --permission-mode <mode>              Permission mode to use for the session
                                        (choices: "acceptEdits", "auto",
                                        "bypassPermissions", "manual",
                                        "dontAsk", "plan")
$ claude -p --help 2>&1 | grep -i permission
# identical output — no --permission-prompt-tool anywhere
$ grep -rn "permission-prompt-tool" . --include="*.go" --include="*.md"
# no hits outside this task's own brief
```

`--permission-prompt-tool` does not exist in `claude --help` or
`claude -p --help` output (v2.1.215), and no code, test, fixture, or README
in this repo ever exercised it. There is no MCP-delegated permission
mechanism on this CLI at all — full stop, not a syntax nuance.

### Why I did not just build against the real mechanism instead

I did identify what's actually real (below), and it *would* let the
acceptance criteria be met. I chose not to implement it in this pass
because the corrected mechanism changes the shape of nearly every file the
original design touched — this needs an architect pass, not an executor
improvising a new cross-process wire contract unreviewed. Concretely:

- The original design parks the MCP response in an **in-memory Go channel**
  inside the long-running session process. The real mechanism has no
  persistent connection at all to park a response in.
- Chasing this alone without review risks a third fabrication in the same
  slot (an invented HTTP endpoint shape presented as fact) — exactly the
  failure this file exists to stop.

### The real mechanism (verified, not asserted)

`claude` has **no MCP permission-delegation flag**. What it has is a
**PreToolUse command hook**, configured via `--settings <json-or-path>`
(confirmed present: `claude --help` lists `--settings <file-or-json>`,
"Path to a settings JSON file or a JSON string to load additional settings
from"). Verified against the bundled hook-development skill doc
(`~/.claude/plugins/marketplaces/claude-plugins-official/plugins/plugin-dev/skills/hook-development/SKILL.md`,
shipped with this Claude Code install, not this repo — cite it in any
future design doc but don't assume it's pinned to this CLI version without
re-checking):

- Settings shape (direct, non-plugin form): `{"PreToolUse": [{"matcher":
  "Write|Edit|Bash", "hooks": [{"type": "command", "command": "<path>",
  "timeout": 600}]}]}`.
- **The hook is a fresh subprocess per matched tool call**, not a
  persistent connection. It receives one JSON object on stdin:
  `{"session_id", "transcript_path", "cwd", "permission_mode",
  "hook_event_name": "PreToolUse", "tool_name", "tool_input"}`.
- It must print one JSON object on stdout and exit 0 to decide the call:
  `{"hookSpecificOutput": {"permissionDecision": "allow|deny|ask"},
  "systemMessage": "shown to the agent, e.g. a deny reason"}`.
- Default command-hook timeout is 60s; the `timeout` field is
  configurable up to whatever bound the design needs (the task's 10-minute
  bound is expressible directly as `"timeout": 600`).
- Hooks "load at session start" per that doc — a `--settings` value must
  be supplied at `claude -p ... --settings ...` launch time
  (`services/session/internal/runner/runner.go`'s `p.Start(ctx, opts)`
  call, around line 284), not injected later.

This means a workable design shape is: the session service writes a
per-session hook script (or a small shared `mads` subcommand) that, on
each PreToolUse invocation, POSTs `{tool_name, tool_input}` to a new
session-service-local HTTP endpoint and **blocks reading the response**
(this is what makes the hook subprocess itself the thing that "parks" —
there is no separate response-parking data structure to build in-process,
the parking *is* the subprocess's own blocked HTTP call). The session
service's HTTP handler is what publishes the `permission_request` event on
the session's live stream and resolves the pending HTTP response when a
`POST /sessions/{id}/permission/{requestId}` (allow/deny) arrives from the
chat UI's card. Concretely this needs:

- A new field on `provider.StartOptions` (or an adapter-internal
  mechanism) to carry the `--settings` hook config — `MCPServers` at
  `provider.go:80` is the closest existing analog for "extra launch-time
  config the adapter must materialize as a temp file," per
  `providers/claudecode/README.md`'s "MCP servers" section
  (`writeMCPConfig`/`--mcp-config` is the pattern to mirror for a
  `--settings` temp file).
- The hook's own request/response HTTP contract on the session service —
  not designed here; needs explicit review given it's a new
  externally-reachable-from-a-subprocess surface.
- `TrustLimited`/`TrustReadOnly` are exactly where this is needed:
  `providers/claudecode/claudecode.go`'s `trustArgs()` only sets
  `--disallowedTools` for those two levels (removes some tools from the
  palette entirely, no ask) but leaves claude's *default* ask-before-every-
  call behavior live for whatever tools remain (`Write`/`Edit` under
  `TrustLimited`) — confirmed against `providers/claudecode/README.md`
  lines 85-103. `TrustFull` already sets `--permission-mode
  bypassPermissions` and must stay untouched (no hook, no cards) per the
  acceptance criteria.

### What I did not do

- No code changes. No tests. Nothing under `services/session` or
  `providers/claudecode` was touched beyond reading.
- Did not design the HTTP request/response contract between the hook
  subprocess and the session service, the `permission_request` event
  schema, or the chat UI card component — all of that is unreviewed and
  should not be taken as decided.

### What the next architect pass should scrutinize

- Re-verify `--settings`'s exact hook-loading behavior against a live
  `claude -p` run with a real fixture (the original brief asked for this
  and it still hasn't been done against the true mechanism) — this file's
  "hooks load at session start" claim comes from a bundled Claude Code
  plugin doc, not from an empirical test run inside this repo.
  `MADS_CLAUDE_SMOKE=1`-style opt-in tests
  (`providers/claudecode/claudecode_test.go`) are the existing pattern for
  this kind of live-binary check.
  - Note on running that check from inside a nested Claude Code session:
    unreliable — a previous attempt found `--permission-mode manual` had
    zero observable effect from inside this sandbox (the CLI runs under a
    managed policy wrapper that overrides permission flags here). A clean,
    unwrapped environment is needed for an empirical behavioral test, not
    just a static `--help`/doc read.
- Whether the hook subprocess's blocking HTTP call is an acceptable
  parking mechanism (it ties up a subprocess + one hook-timeout slot per
  in-flight prompt) versus some other shape.
- The `permission_request` event's wire schema and how the chat UI
  reconstructs card state on transcript refetch (acceptance criterion 3) —
  needs to be designed against the real event, not assumed from the old
  MCP-channel model.

---

## Third pass: implemented

Built directly on the "real mechanism" section above — no new fabrication,
confirmed by building against the fake-claude stand-in binary rather than
trusting doc claims a third time. Full Go suite (`go test ./...`) and full
UI suite (`npx vitest run`, `npx tsc --noEmit`) pass with no regressions.

### What was built

- `providers/provider/event.go`: additive `EventPermissionRequest` +
  `PermissionRequest{ID, Tool, Input, Status}`. `provider.go`:
  `StartOptions.PermissionPromptURL`.
- `providers/claudecode/permission.go`: `needsPermissionHook(trust)` (true
  only for `TrustLimited`/`TrustReadOnly`), `writePermissionSettings(url)`
  (writes a `--settings` temp JSON file wiring a PreToolUse hook for `"*"`
  whose command is `curl -X POST --max-time 640 -d @- <url> ||
  echo '{"hookSpecificOutput":{"permissionDecision":"deny"},...}'`, hook
  `"timeout": 660`), `permissionArgs(trust, url)` gating both on trust and
  on url being non-empty. `session.go`'s `startStream` (chat/ACP transport
  only) appends these args and cleans up the temp file on close/spawn
  failure. `startPTY` (terminal transport) was **not touched** — it never
  reads `PermissionPromptURL`, so terminal mode is provably unaffected.
- `services/session/internal/runner/permissions.go`: an in-memory
  (non-persisted) pending-request registry on `Runner`.
  `RequestPermission(tool, input)` broadcasts a `permission_request` event,
  blocks on a channel until `ResolvePermission(id, decision)` answers or
  `r.PermissionTimeout` (default 10 min) elapses, in which case it resolves
  to `"deny"` itself and broadcasts a terminal `"expired"` event.
  `finishPermission`'s delete-under-mutex is what arbitrates the
  resolve-vs-timeout race — no `sync.Once` needed. `main.go` now binds the
  listener *before* `r.Start`, so `r.SelfURL` (and therefore
  `opts.PermissionPromptURL = SelfURL + "/permission-hook"`) is known before
  the provider subprocess is spawned.
- `services/session/internal/api/api.go`: `POST /permission-hook` (the
  hook's curl target — response body IS the hook's required stdout
  verbatim), `GET /permission-requests`, `POST
  /permission-requests/{id}/resolve`.
- `services/router`: proxies `GET/POST /sessions/{id}/permission-requests[/...]`
  to the per-session child, same `proxyTo` pattern as every other
  session-scoped route. Added to `route_manifest_test.go`.
- UI: `ui/src/router/types.ts` (`PermissionRequest`, `permission_request`
  stream event), `client.ts`/`fixtures.ts` (`getPermissionRequests`,
  `resolvePermissionRequest`, `FakeRouterClient.seedPermissionRequest`),
  `ChatRenderer/PermissionCard.tsx` (Allow/Deny card, disables both buttons
  mid-resolve, shows pending/allowed/denied/expired), wired into
  `ConversationTab.tsx`: fetched on mount (survives transcript refetch —
  acceptance criterion 3), upserted live off the `permission_request` SSE
  event, reset on session switch.

### Tests added (all passing)

- `providers/claudecode/permission_test.go`: unit tests for
  `needsPermissionHook`, `shellSingleQuote`, `writePermissionSettings`'s
  exact JSON shape (including the timeout-ordering invariant), and
  `permissionArgs`' gating table.
- `providers/claudecode/fakeclaude_test.go` +
  `TestPermissionHookWiredOnChatTransportOnly` (claudecode_test.go): a new
  `settings-echo` stand-in dialect reads back the real `--settings` file
  the adapter wrote, proving `--settings` is present with the right URL for
  TrustLimited/TrustReadOnly and absent for TrustFull and for an empty
  `PermissionPromptURL`.
- `services/session/internal/runner/permissions_test.go`: registry tests —
  blocking request/resolve for both allow and deny, timeout-to-deny with an
  overridden short `PermissionTimeout`, resolve-after-timeout is a no-op,
  invalid decision strings are rejected, `PermissionRequests()` only lists
  pending entries.
- `services/session/internal/api/permission_test.go`: real HTTP round trip
  (`httptest.Server` + real `runner.Runner` + `providers/fake`) — hook POST
  blocks, `GET /permission-requests` shows it pending, resolve unblocks it
  with the exact `hookSpecificOutput` JSON, plus a timeout/expired variant.
- `ui/src/conversation/ChatRenderer/PermissionCard.test.tsx`: rendering,
  Allow/Deny calling `onResolve`, disabled-while-resolving, and the three
  non-pending statuses.
- `ui/src/conversation/ConversationTab.test.tsx`: two new tests — a live
  `permission_request` stream event renders a card and clicking Allow calls
  `resolvePermissionRequest`; a request seeded via `getPermissionRequests`
  (not the stream) still renders on mount, proving the refetch-survival
  criterion.

### What was NOT done — reviewer should scrutinize

- **No live-`claude` manual verification was performed or claimed.** Per
  pipeline memory, nested-Claude-Code-session behavioral testing of the
  real CLI is unreliable (a managed policy wrapper overrides some
  permission flags in that sandbox). All backend proof here is against the
  `fakeclaude_test.go` stand-in binary, which faithfully re-implements the
  documented flag surface but cannot prove the *hook subprocess itself*
  behaves as `--settings`'s doc claims on a live CLI. A follow-up manual
  run (`MADS_CLAUDE_SMOKE=1`-style, per `claudecode_test.go`'s existing
  live-binary opt-in pattern) against a real `claude` binary, from a clean
  non-nested environment, is still owed and should happen before this is
  fully trusted in production.
- **In-memory-only registry**: a session-service process restart mid-prompt
  silently loses the pending request (the blocked hook's curl eventually
  hits its own `--max-time`/`timeout` and denies). Acceptance criteria only
  require surviving a transcript *refetch*, not a process bounce, so this
  was accepted rather than adding persistence.
- **Three-layer timeout ordering** (hook `timeout` 660s > curl `--max-time`
  640s > Runner `PermissionTimeout` 600s) is deliberate so the
  server-side bound always wins the race and the UI's "expired" state is
  the one the agent actually observes — worth double-checking the deny
  fallback text is what you want the agent to see verbatim if that
  ordering assumption ever needs to change.
- **Resolved cards are not pruned from `ConversationTab`'s local state** —
  they persist (as allowed/denied) for the rest of that mount rather than
  disappearing, an intentional UX choice (a card silently vanishing after
  being answered seemed worse than a static resolved chip) but not
  explicitly required by the acceptance criteria.
- `providers/claudecode/README.md`'s trust-enforcement table and a new
  "Answering the prompt itself" subsection were updated in this pass to
  document the hook — worth a fresh read against the final code shape
  since it was written before the test suite above was finished.
