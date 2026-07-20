# Trust "Auto" must actually mean auto (EXECUTOR HANDOFF)

Task: tsk_51907d604b7638b3.

## What was done

**Root-cause fix — TrustFull now passes a real permission-bypass flag:**
- `providers/claudecode/claudecode.go`: renamed `disallowedToolsArgs` →
  `trustArgs`; its `default:` case (TrustFull and the zero value) now
  returns `["--permission-mode", "bypassPermissions"]` instead of `nil`.
  TrustLimited/TrustReadOnly's `--disallowedTools` mapping is byte-for-byte
  unchanged.
- `providers/claudecode/session.go` and `pty.go`: both call sites updated to
  the renamed function — the fix reaches both the structured (chat/ACP) and
  terminal (PTY) transports, per the brief's explicit "BOTH transports"
  requirement.
- Deliberately used `--permission-mode bypassPermissions`, not
  `--dangerously-skip-permissions` — the latter stays exclusively the
  pipeline engine's own flag (`cmd/mads/run.go`'s `-claude-args` default,
  unchanged, verified by grep). Reasoning documented in the README.

**Defense in depth — a blocked tool call is never a silent no-op:**
- `providers/provider/event.go`: added `EventToolResult` (`"tool_result"`)
  and an `IsError` field on `Event`.
- `providers/claudecode/wire.go`: `wireLine.events()` previously dropped
  every `"user"`-type wire line entirely (fell into `default: return nil`).
  Added a `case "user":` that mirrors `transcript.go`'s existing
  `normalizeTranscript` handling — each `tool_result` block becomes an
  `EventToolResult` with `Tool.ID`, `Text`, and `IsError`. This is real wire
  behavior, not a guess: verified against the actual captured fixture at
  `providers/claudecode/testdata/transcript_denial.jsonl` (a real session
  with a denied `Read` permission, `is_error: true`).
- No backend SSE/broadcast changes needed — `services/session`'s event
  plumbing is already generic over `EventKind` (verified by reading
  `hub.go`/`runner.go`/`api.go`).
- Frontend: `ui/src/router/types.ts` gained the `tool_result` `StreamEvent`
  variant. `ConversationTab.tsx` listens for it; on `is_error: true` it sets
  `TurnState`'s waiting phase to carry a `blocked` message. `ChatRenderer`/
  `WorkingIndicator` render that as a system-toned `Chip tone="bad"` banner
  ("blocked awaiting permission — switch to terminal or raise trust")
  instead of the pulsing-dots spinner, so a stalled/denied tool call is
  always visible in chat mode, never a silent stall.

**Tests:**
- `fakeclaude_test.go`: new `--permission-mode` arg parsing and a
  `permission-echo` dialect (mirrors the existing `mcp-echo` pattern) on
  both the stream and terminal fake-CLI paths.
- `claudecode_test.go`: `TestTrustFullBypassesPermissionsStream` and
  `...PTY` prove the exact flag reaches the launched subprocess on both
  transports for `TrustFull` and the zero value (provider-args-level
  integration tests, per acceptance criteria). `TestTrustDisallowsTools`
  (Limited/ReadOnly) is untouched and still passes.
  `TestWireEventsSurfacesToolResultDenial` is the live-wire fixture test —
  reuses the same real `transcript_denial.jsonl` fixture
  `TestTranscriptNormalizationDenialGolden` already pins for the on-disk
  path, parsed instead through `wireLine.events()`, proving the permission
  denial surfaces as `EventToolResult{IsError:true}` on the live stream.
- `ConversationTab.test.tsx`: new test emits a `tool_result` with
  `is_error: true` and asserts the blocked banner renders and the plain
  "Using Bash…" spinner is gone.
- `providers/claudecode/README.md`: documented the trust→flag matrix
  (including the `bypassPermissions` vs. `dangerously-skip-permissions`
  reasoning) and the new live `EventToolResult` behavior under a new
  "Defense in depth" subsection.

**Verification run:**
- `go build ./...`, `go vet ./...`, `go test ./providers/... ./services/...`
  — all pass.
- `ui`: `tsc --noEmit`, `eslint .`, `vitest run` (202 files / 1097 tests) —
  all pass.

## What was NOT done

- No `MADS_CLAUDE_SMOKE=1` run against a real `claude` binary — the flag
  choice (`--permission-mode bypassPermissions`) and the wire shape for
  `case "user":` are both taken from `claude --help` and a real captured
  fixture respectively, per the README's existing caveat convention, but
  neither is confirmed against a live divergence test in this session. The
  README's existing caveat paragraph already covers this and now also
  covers the new claim.
- The blocked banner's copy is the literal string suggested in the task
  brief ("blocked awaiting permission — switch to terminal or raise
  trust"), not the specific `tool_result` text claude reports (e.g.
  "permission denied: image reads require approval") — kept simple/generic
  since the brief quoted this exact wording; the underlying `Text` is still
  carried on the event if a future change wants to surface it.
- No change to `TestTrustDisallowsTools`, the Matrix() Native/Emulated
  classification for Limited/ReadOnly, or anything on the one-shot/pipeline
  code path — none of those needed to change for this fix.

## What the reviewer should scrutinize

- The choice of `--permission-mode bypassPermissions` over
  `--dangerously-skip-permissions` for TrustFull is a judgment call (both
  flags skip the same interactive prompt per `claude --help` in this
  environment) — confirm this is still claude 2.1.x's supported spelling
  before this ships, ideally via `MADS_CLAUDE_SMOKE=1`.
  `--dangerously-skip-permissions` was deliberately left untouched on the
  pipeline engine path per the brief.
- `onToolResult` in `ConversationTab.tsx` only reacts when
  `is_error === true`; a successful `tool_result` is intentionally a no-op
  there since `onTool`/`onText`/`onTurnEnd` already drive the rest of the
  turn-state machine correctly for the happy path — worth double-checking
  there's no case where a *successful* result needs its own live signal.
  None was identified.
- `wireLine.events()`'s new `case "user":` returns `nil` (via an empty
  `evs` slice) for a `"user"`-role line with no `tool_result` blocks (i.e.
  the human's own outbound message never reaches this parser over the wire
  — it only ever travels over stdin) — this matches the existing comment
  on `wireBlock.ToolUseID` but is worth a second look if claude-code's wire
  format ever starts echoing the human's own turn back as a `"user"` line.
