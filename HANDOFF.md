# Executor handoff: Linked sessions L2c — leader capability (cross-session tool)

## What shipped

**Router endpoints** (`services/router/internal/api/orchestrate.go`, wired
in `api.go`/`links.go`), all gated by `requireLinkedSessions` (flag off →
404):
- `GET /sessions/{id}/orchestrate/subjects` — links where `id` is the
  granting session and holds `orchestrate`; shape `{link_id,
  subject_session_id, subject_name, grants, activity}`.
- `GET /sessions/{id}/orchestrate/summary?for={subjectID}` — 403 without an
  orchestrate link over that subject; prefers the subject's stored summary,
  falls back to a transcript tail.
- `POST /links/{id}/orchestrate/message` — reuses L2a's exact delivery path
  (`postJSONToChild` → subject's own `/inject`), so trust-capping and
  attribution (`resolveLinkAttribution`, now shared with `handleLinkInject`)
  come for free. Optional `wait_ms` (capped at 30s) polls the leader's own
  event log for a `link.message_injected` envelope newer than send-time;
  times out to `{"note": "still working; reply will arrive as a digest"}`.

**`pkg/orchestratemcp`** — a hand-rolled MCP-over-stdio server (JSON-RPC 2.0
line framing; `initialize`/`tools/list`/`tools/call` only). No SDK
dependency added, per the architect's explicit guidance. Exposes
`list_links`, `get_summary`, `send_message` as thin HTTP calls to the three
Router endpoints above, stamping `X-Mads-Actor: session-<leaderSessionID>`.

**`cmd/mads internal orchestrate-mcp`** — hidden subcommand
(`cmd/mads/orchestrate_mcp.go`) that starts the server on stdio, reading
`MADS_ROUTER_URL`/`MADS_SESSION_ID` from its environment. Not listed in
`usage()`.

**`Runner.setupOrchestrate`** (`services/session/internal/runner/capability.go`)
— materializes a synthetic, never-persisted `contracts.Capability` and
starts it via the existing `pkg/capability.Registry.EnsureStarted` (same
mechanism `setupSkill`/`setupMCP` use), only when: `MCPRegistry` is set, the
`linked-sessions` flag is on, and this session holds at least one
`orchestrate` grant as granting session. Checked once at `Start()`; a grant
acquired mid-session doesn't retroactively spawn the tool. Wired into
`provider.StartOptions.MCPServers` (`providers/provider/provider.go`), a new
descriptor field; `providers/fake` records and exposes what it receives so
tests can assert on it end-to-end.

**CLI plumbing**: `services/session/main.go` gained a `-router-url` flag
(mirrors `-data-url`'s convention, default `http://localhost:8080`) feeding
`r.RouterURL`. `services/router/main.go` appends `-router-url http://<addr>`
to `sessionArgv` before it's handed to `supervisor.New` — sourced from the
`*addr` flag value, not `ln.Addr()`, since `sessionArgv` is built before
`net.Listen` runs.

**Docs**: `docs/LINKED-SESSIONS-DESIGN.md` decomposition item 5 now
documents what shipped and the explicit CLI-wiring gap.
`docs/REBUILD-SPEC.md` §12 Notes calls out this one exception to "MCP
servers are system-wide."

## What did not ship (explicit non-goal, per the architect's handoff)

Real provider CLI wiring of `StartOptions.MCPServers` into actual launch
args/config — `claudecode`/`kirocli` writing `.mcp.json` or `--mcp-config`
never happens today for *any* capability, declared or synthetic. This task
built the descriptor field and a real, working backend + MCP server, but a
live `claudecode`/`kirocli` agent still can't reach it. Fast-follow, as
scoped.

## Scrutiny points

1. **Bounded-wait tests don't exercise the real digest pipeline.**
   `digestwatch.Watcher` (L2b) isn't wired into the Router's test harness
   (`api_test.go`'s `newTestServer*` helpers never construct one — confirmed
   via grep, zero hits). `TestOrchestrateMessageBoundedWaitReturnsReply` /
   `...TimesOut` (`orchestrate_test.go`) instead append a
   `contracts.LinkMessageInjected` envelope directly via `dataclient.Append`
   in a background goroutine, simulating what a real digest landing would
   produce. This exercises `awaitOrchestrateReply`'s polling logic exactly,
   but never proves the full turn-ended → digest job → inject chain
   round-trips into a `send_message` wait. That chain is L2b's own tested
   surface (`digest_test.go`), not re-tested here — reviewer should confirm
   that's an acceptable line, not a gap.

2. **Reply correlation is best-effort by design.** `send_message`'s
   `wait_ms` polls for *any* new `link.message_injected` event on the
   leader's own session, not one keyed to this specific send. An unrelated
   digest landing during the wait window could be mis-picked-up as "the
   reply." No correlation ID exists to fix this precisely; out of scope per
   the architect's handoff, but worth a second look if this ships to real
   users.

3. **`capability_test.go`'s `TestMain` now builds a real `mads` binary**
   (`madsBinDir`), in addition to the existing `envdumpBin`, so
   `TestSetupOrchestrateWithGrant` could exercise `setupOrchestrate`'s
   hardcoded `"mads internal orchestrate-mcp"` command through a real
   subprocess spawn rather than only testing the gating logic. This adds a
   few hundred ms to that package's test startup; flagged in case there's a
   faster alternative preferred (e.g., making `Config.Command` injectable
   for tests) — I chose not to add that indirection since nothing else in
   `setupOrchestrate` needed it.

## Verification

- `make check` — green (Go lint + test, UI test suite all pass).
- `gofmt -l` — clean on every changed/new file.
- All new tests pass: 8 in `orchestrate_test.go` (router), 6 in
  `orchestratemcp_test.go`, 3 in `orchestrate_mcp_test.go` (cmd/mads), 3 in
  `services/session/internal/runner/orchestrate_test.go` (including one real
  subprocess spawn).
