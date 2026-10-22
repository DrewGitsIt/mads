# providers/claudecode

The reference provider adapter (spec §11): Claude Code driven through its
`stream-json` interface for interactive sessions, `claude -p` for one-shots,
`--resume` for reattach, and the on-disk project transcripts for normalized
history.

## Matrix, row by row

| Behavior | Support | Why |
|---|---|---|
| Interactive | **Native** | `claude -p --input-format stream-json --output-format stream-json` is a real, documented persistent structured session. |
| Interrupt | **Emulated** | Rides a stream-json `control_request` (`subtype: interrupt`) rather than a first-class harness API — the turn ends, the process survives, but it is a protocol convention layered on top of the stream, not a dedicated call. |
| OneShot | **Native** | `claude -p "<prompt>" --output-format json` is the documented non-interactive invocation. |
| Resume | **Native** | `--resume <session-id>` is a real, documented flag on both transports. |
| Transcript | **Native** | Claude Code writes project transcripts to `~/.claude/projects/<project>/<session-id>.jsonl`; this adapter reads them directly. |
| ModelDiscovery | **Native** | `list_models` over stream-json returns the harness's own catalog (`discover.go`): one control request, no turn, no LLM call, ~1s. Aliases (`opus`, `default`) are kept as the selectable id so the picker tracks the latest model without mads re-pinning. `Config.Models` pins the list instead when set; otherwise a failed probe wraps `ErrDiscoveryUnavailable` rather than falling back to a stale list. |
| CostReporting | **Native** | Both `-p --output-format json` and the stream-json `result` line report `total_cost_usd` and a nested `usage` object with token counts. |
| ACP | **Native** | The stream-json structured transport (this package's name for its ACP-equivalent event stream). |
| PTY | **Native** | claude-code's own interactive terminal UI runs directly on a pseudo-terminal. |
| Branching | **Native** | Fork-on-resume only: `StartOptions.Fork` launches `--resume <ref> --fork-session --session-id <new uuid>` on both transports (`fork.go`). The fork gets a new id and its own transcript in the new cwd's project dir (lines' `cwd` rewritten); the original file is never written. The id is pinned with `--session-id` because the terminal transport can't otherwise learn it, and a forked session's `Ref()` stays empty until claude confirms it (the stream `system`/`init` line, or the fork's transcript appearing on the terminal transport) — never the original's id. Forking at an earlier turn (rewind) is not supported: claude has no scriptable flag for it. |
| CwdReroot | **Unsupported** | No flag or API moves a live session to a different working directory. |
| TrustLimited | **Native** | `--permission-mode manual`: claude's own normal mode, in which claude itself decides what is risky enough to ask about (a `Bash` call, a write outside the working directory). On chat the asks reach the user over `--permission-prompt-tool stdio`; on the terminal claude's own UI asks. See "Trust enforcement" below. |
| TrustReadOnly | **Native** | The same mode plus `--disallowedTools Bash,Write,Edit,NotebookEdit`, which removes command execution and every file-mutating tool from the palette (confirmed against `claude --help`: "a comma or space-separated list of tool names to deny"). Whatever narrow set remains still asks the same way. |
| TrustAutoReview | **Native** | `--permission-mode auto`: claude's own auto mode, whose server-side classifier approves routine calls and blocks risky ones; after 3 consecutive (or 20 total) blocks claude stops trusting it and asks — over `--permission-prompt-tool stdio` on chat, claude's own UI on the terminal. Probed against 2.1.285; see "Auto-review: claude's auto mode" below. |
| MCPServers | **Native** | `--mcp-config <file> --strict-mcp-config` threads `StartOptions.MCPServers` into a real launch on both transports — see "MCP servers" below. |
| RefAnnounce | **Emulated** | The structured transport's `system`/`init` line carries `session_id` at connect, before any turn (Native-strength). The terminal transport prints nothing, and a structured session's own announcement can itself be dropped or delayed past its window; either way the Runner falls back to `LocateSession` (`providers/provider.SessionLocator`), a weaker best-effort mechanism — see "Ref announcement, transport by transport" below. Emulated is the honest one-row summary across both. |
| StableKeys | **Native** | Every item's key is an id claude writes the same on stdout and in the JSONL: `<message id>:<block index>` for text and thinking, the `tool_use` id for a call and its result, `user:<line uuid>` for a user message (mads's id, sent as the stdin `uuid` by `SendKeyed`). Settle hints and an incremental reader (`TranscriptSince`) come with it. See "Conversation keys" below. |

## Ref announcement, transport by transport

`BehaviorRefAnnounce` lets the runner persist a session's provider-native
ref the moment it's known, instead of only at turn end — the defense
against a crash losing an unresumable session. Two independent mechanisms
feed it:

**Wire announcement (primary, `providers/provider.RefAnnouncer`):**
`startStream()`'s `system`/`init` line already carries `session_id` before
any user turn is sent (confirmed against the scripted stand-in's
`fcStream`, which emits it immediately on connect, mirroring real
claude-code 2.1.207 behavior). `handleLine` fires `streamSession.announced`
the first time `session_id` is seen, which for a fresh session is on that
very first line. The terminal transport has no equivalent — `ptySession`
does not implement `RefAnnouncer` at all.

**Content-correlation backstop (`providers/provider.SessionLocator`,
`locate.go`):** the Runner calls `LocateSession(ctx, cwd, spawnedAfter,
firstMessage)` with the exact first message it sent whenever the wire
announcement never arrives — always for a PTY session, and also for a
structured session whose announcement is dropped or delayed past its
window. `LocateSession` retries two rungs on a short interval until `ctx`
is done:

1. **Rung 1 — `locateFromHistory`:** scan claude-code's own append-only
   prompt index, `Config.HistoryFile` (default `~/.claude/history.jsonl`),
   for the one entry whose `project` matches `cwd`, whose `timestamp` is no
   earlier than `spawnedAfter` (minus a small clock-skew margin), and whose
   `display` corresponds to `firstMessage` (exact, or a prefix match in
   either direction to tolerate claude's pasted-content placeholder
   mangling). A history scan is far cheaper than walking every transcript
   file, so it runs first whenever the index has a usable entry. Its result
   is never trusted on its own — `verifyRef` confirms it names exactly one
   real transcript under `TranscriptDir` with a matching `cwd` before
   `LocateSession` returns it.
2. **Rung 2 — `discoverRefByContent`:** the fallback when the index has no
   usable entry (or is unavailable). `candidateTranscripts` filters every
   transcript under `TranscriptDir` to those modified after `spawnedAfter`
   (same clock-skew margin) whose stamped `cwd` matches; `discoverRefByContent`
   then narrows those survivors to the one whose first user-role message is
   exactly `firstMessage`.

Both rungs decline (rather than guess) on zero or multiple matches — a
wrong ref would silently resume someone else's conversation, which is worse
than no ref. If neither rung ever resolves before the caller's `ctx` is
done, the ref is simply never discovered — logged, never surfaced to the
user as an error, and the session works exactly as it did before this
mechanism existed (resume-less until reattached with an explicit ref).

## Trust enforcement, transport by transport

Both `startStream()` (structured transport) and `startPTY()` (terminal
transport) build the trust flags with the shared `trustArgs()` helper. The
mode and the removed tools are identical on both; the only difference is
who answers claude's asks — the user through mads on chat
(`--permission-prompt-tool stdio`, see below), a human at claude's own
terminal UI on PTY.

The principle: **mads asks the user only when the harness itself would
ask.** mads never invents an ask-first gate of its own; "limited" means
"claude's normal judgment about what is risky", answered by the user.

| Trust | Flag(s) | Effect |
|---|---|---|
| `TrustLimited` | `--permission-mode manual` (+ `--permission-prompt-tool stdio` on chat) | claude's normal mode: it asks before anything it deems risky — a `Bash` call, a write outside the working directory — and runs the rest. Reads inside the working directory never ask. |
| `TrustAutoReview` | `--permission-mode auto` (+ `--permission-prompt-tool stdio` on chat) | claude's auto mode: a classifier decides each call that would otherwise need approval. Routine calls run; a risky one is denied outright with the classifier's reason, not asked about. A run of blocks escalates to an ask — the same card limited shows. |
| `TrustReadOnly` | `--permission-mode manual --disallowedTools Bash,Write,Edit,NotebookEdit` (+ `--permission-prompt-tool stdio` on chat) | Command execution and every file-mutating tool are absent from the palette — a hard guarantee, not a prompt. Whatever remains asks the same way limited does. |
| `TrustFull` (and the zero value) | `--permission-mode bypassPermissions` | claude never asks at all, so no prompt channel is wired. |

`manual` is the CLI's canonical name for its normal mode in claude-code
2.1.285 (`claude --help` lists `acceptEdits, auto, bypassPermissions,
manual, dontAsk, plan`; the `system`/`init` line still reports it as
`"permissionMode":"default"`). It is passed explicitly so a user's own
`settings.json` `defaultMode` can't quietly loosen a limited session.

`--disallowedTools`'s effect is confirmed against `claude --help` in this
environment: `--disallowedTools, --disallowed-tools <tools...>` takes "a
comma or space-separated list of tool names to deny".

**Why `TrustFull` needed a flag at all:** claude's *default* permission mode
asks before risky tool calls, on both transports. That's fine when a human is
sitting at the terminal to answer, but chat mode (the structured transport)
has no surface to answer a permission prompt at all — the request just sits
there. Before this fix, `TrustFull` (meant to be "no restrictions, no
prompts") passed no permission-related flag, so a full-trust chat session
silently stalled on the CLI's own interactive prompt instead of running the
tool. `--permission-mode bypassPermissions` makes claude skip that flow
entirely, which is what "Auto" trust is supposed to mean on both transports.

**Why `--permission-mode bypassPermissions` and not
`--dangerously-skip-permissions`:** both flags skip the same prompt, but
`--dangerously-skip-permissions` is reserved for the separate pipeline
engine's own autonomous-agent invocation (`cmd/mads/run.go`'s
`-claude-args`, see "Pipeline vs. interactive registries" below) and is left
untouched by this change. `--permission-mode bypassPermissions` is the
CLI's documented interactive-session equivalent (claude 2.1.x); using a
distinct flag for the interactive path keeps the two invocation sites
independently greppable and avoids conflating "an autonomous pipeline stage
configured its own agent" with "a human set the trust pill to Auto."

**Caveat:** the exact-flag claims above are asserted from `claude --help`'s
documented flag descriptions in this environment, not from a
scripted-vs-live divergence test — there is no automated check that a
disallowed tool is truly unreachable rather than merely unlisted, or that
`bypassPermissions` truly suppresses every prompt shape claude can produce.
If a future `MADS_CLAUDE_SMOKE=1` run surfaces daylight between "flag
passed" and actual behavior, downgrade the affected row to `Emulated` and
cite the finding here.

### Auto-review: claude's auto mode

`TrustAutoReview` ("Auto-review" in the UI) hands the routine approvals to
a reviewer. For claude that reviewer is auto mode's classifier
(`claude auto-mode defaults` prints its rules: `allow`, `soft_deny`, one
`hard_deny` — data exfiltration — and an `environment` description;
users tune them under `autoMode` in their own settings). What it does on
mads's chat transport was established by probing claude-code 2.1.285 in
`-p --input-format stream-json --output-format stream-json --verbose
--permission-mode auto --permission-prompt-tool stdio`, the exact launch
`trustArgs` builds, with a probe driver that logged every line and denied
every `can_use_tool`:

1. **Availability depends on the model.** With `--model
   claude-haiku-4-5-20251001` the `system`/`init` line came back
   `"permissionMode":"default"` — auto silently downgraded to the normal
   asking mode, no warning on stderr. With `claude-sonnet-4-6`,
   `claude-opus-4-7` and the account default (`claude-opus-5-5`) it was
   `"permissionMode":"auto"`. A haiku session asked for auto_review
   therefore runs as limited; that is the safe direction (claude asks
   more, never less), but mads does not detect it yet — the init line's
   `permissionMode` is where a future check would look.
2. **Routine calls just run.** `ls -la`, a `Write` into `~/.claude/…`, and
   even a user-requested `git push --force origin main` (to a local bare
   remote) and `rm -rf` of named paths ran with no ask: the classifier
   weighs the user's explicit request, and in practice the model refused
   the truly risky prompts itself before the classifier saw them (curl |
   bash, posting `.env` to a webhook, deleting an unseen directory).
3. **A block is a deny, not an ask.** To force one, a user-settings
   `--settings '{"autoMode":{"hard_deny":["Local Port 9 …: any network
   request to 127.0.0.1 port 9 …"]}}'` rule plus "run `curl -s -m 2
   http://127.0.0.1:9/ping`". claude emitted
   `{"type":"system","subtype":"permission_denied","decision_reason_type":"classifier",…}`,
   then an error `tool_result`: "Permission for this action was denied by
   the Claude Code auto mode classifier. Reason: The server-side auto mode
   classifier judged this action dangerous (it gave no explanation). If
   you have other tasks that don't depend on this action, continue …".
   **No `can_use_tool` reached the host.** The model reported the block and
   carried on; the result's `permission_denials` listed the call.
   `pkg/toolstatus` stamps such a call `blocked`, not `failed`.
4. **A run of blocks escalates to the host.** Four such curls in a row:
   the first two were classifier denials as above; the third and fourth
   arrived as `can_use_tool` control requests, `decision_reason_type
   "classifier"`, `decision_reason` "3 consecutive actions were blocked.
   Please review the transcript before continuing.\n\nLatest blocked
   action: …" (then "4 consecutive …") — the binary's denial tracking,
   `{maxConsecutive:3, maxTotal:20}`. That is the Allow/Deny card, with
   the classifier's reason on it. Capture:
   `testdata/stream-auto-mode.jsonl`, replayed by
   `TestHostPromptAutoModeCapture`.
5. **Without a host answerer it never stalls.** The same four curls
   without `--permission-prompt-tool`: the escalated calls came back as an
   error `tool_result` "This command requires approval" (also
   `toolstatus` `blocked`). The binary has a harder headless path —
   "Agent aborted: too many classifier denials in headless mode" — for
   sessions that may not prompt at all; mads's chat sessions always wire
   an asker, so they take the ask path instead.

So mads's Auto-review on claude means: the classifier's routine allows run
silently, its blocks are reported in the conversation as blocked calls
(the model is told why and usually works around or explains), and only its
escalations — a streak of blocks, or a safety check the classifier doesn't
own — become an Allow/Deny card. On the terminal transport claude's own UI
shows the same escalations. The classifier is server-side and spends no
session tokens of its own that mads sees.

### Defense in depth: a blocked tool call must never be silent

Even with the flag above, a permission-shaped stall is still possible (a
custom permission hook, an unrecognized prompt shape, a future CLI change)
— so the live event stream never silently drops a tool's outcome.
`wireLine.events()` (`wire.go`) now handles the `"user"`-type wire line the
same way `normalizeTranscript` (`transcript.go`) already handled it for the
on-disk/polled path: each `tool_result` block becomes a
`provider.EventToolResult`, carrying the correlating `Tool.ID`, the result
text, and `IsError`. Previously this line type fell into `events()`'s
`default:` case and was dropped entirely on the live stream — a permission
denial (or any other tool failure) was invisible until the next transcript
poll caught up, or never, if the process was genuinely hung awaiting an
unanswerable prompt. The fixture at `testdata/transcript_denial.jsonl` is a
real captured session with exactly this shape (a denied `Read`, `is_error:
true`, followed by a successful `Bash` call); `TestWireEventsSurfacesToolResultDenial`
pins `events()`'s live parsing of it, alongside
`TestTranscriptNormalizationDenialGolden`'s existing coverage of the
on-disk path.

The frontend renders an `EventToolResult` with `is_error: true` as the
error state on its own tool card (claude reports *every* ordinary tool
failure this way — a non-zero exit, a missing file — so `is_error` alone
must not be treated as a permission signal). A call that never ran for want
of permission is told apart by the Runner's status synthesis
(`pkg/toolstatus`): it is stamped `blocked`, not `failed`, when mads refused
its ask, or when its result text is claude's own refusal ("Claude requested
permissions to … but you haven't granted it yet." — `toolstatus.PermissionRefusal`,
which also covers reloaded transcripts). The "awaiting permission" card is
driven by `EventPermissionRequest` (below), which names the asking call by
`tool_use_id` and clears itself when the request resolves.

### Answering the prompt itself: the host permission channel (chat transport)

On the terminal transport (`startPTY`) claude's prompts are its own native
TUI — a human is sitting there, so nothing further is needed. On the
chat/structured transport (`startStream`) there is no such surface, so when
the Runner supplies a `StartOptions.Permissions` asker (it always does) and
trust can ask at all (auto-review/limited/read-only), `trustArgs` adds
`--permission-prompt-tool stdio`: claude's SDK host channel. Every prompt
claude would have shown then arrives on stdout as an inbound control
request, and claude blocks that one tool call until mads answers on stdin
(`hostprompt.go`). Wire shapes, verified against claude-code 2.1.285 (real
capture: `testdata/stream-can-use-tool.jsonl` — a main-agent `Write` that was
allowed and a foreground subagent's `Write` that was denied, both outside
cwd; `stream_event` lines dropped):

```text
← {"type":"control_request","request_id":"<uuid>","request":{"subtype":"can_use_tool",
     "tool_name":"Write","input":{...},"tool_use_id":"toolu_…","agent_id":"a…",
     "decision_reason":"Path is outside allowed working directories", ...}}
→ {"type":"control_response","response":{"subtype":"success","request_id":"<uuid>",
     "response":{"behavior":"allow","updatedInput":{...the input, unchanged...}}}}
→ ... "response":{"behavior":"deny","message":"<relayed to the model verbatim>"}
← {"type":"control_cancel_request","request_id":"<uuid>"}
```

- `agent_id` is present only for a subagent's calls; it equals that
  subagent's `task_id`, so the adapter maps it to the parent Agent call via
  the `task_started` line (`PermissionAsk.ParentToolUseID`).
- A deny's `message` becomes the refused call's error `tool_result` text
  verbatim. The Runner's messages all start with
  `toolstatus.DenialPrefix`, so a reloaded transcript still shows the call
  `blocked`.
- An interrupt while a prompt is open makes claude send
  `control_cancel_request` for it and refuse the call itself ("The user
  doesn't want to proceed with this tool use. …"); the adapter cancels the
  asker's ctx and writes nothing back. The process exiting or `Close`
  cancels every open ask the same way.
- Any other inbound `control_request` subtype gets
  `{"subtype":"error","error":"…"}` — claude blocks on every inbound
  request, and an error answer makes it refuse ("Tool permission request
  failed: …") instead of hanging.
- Without `--permission-prompt-tool stdio` (no asker), claude in manual
  mode doesn't stall: it refuses the call at once with "Claude requested
  permissions to …, but you haven't granted it yet."

The asker (`provider.PermissionAsker`) is harness-agnostic: the Runner's
`RequestPermission` (`services/session/internal/runner/permissions.go`)
broadcasts the `EventPermissionRequest` card, waits for the user's
Allow/Deny (`POST /permission-requests/{id}/resolve`), and denies on its own
after ~10 minutes ("expired") or when ctx is cancelled ("cancelled").

### The persona path-scope hook

A persona's `AllowedPaths` (`StartOptions.PersonaAllowedPaths`, any trust)
is the one thing still enforced by a hook: `permission.go` writes a
`--settings` file with a `PreToolUse` command hook on
`Write|Edit|MultiEdit|NotebookEdit` that denies a target outside the
allowlist. Hook events must nest under a top-level `"hooks"` key, exactly
as in `~/.claude/settings.json`:

```json
{"hooks": {"PreToolUse": [{"matcher": "Write|Edit|MultiEdit|NotebookEdit", "hooks": [{"type": "command", "command": "sh -c '...'", "timeout": 5}]}]}}
```

The flat shape (`{"PreToolUse": [...]}` at the top level) loads without
complaint and is silently ignored — this package wrote it until 2026-10,
so neither the old ask-first hook nor the path-scope hook ever ran.
`TestSettingsShapeRealClaude` pins the nested shape against the real binary
at zero token cost: a `SessionStart` hook added through the same marshaling
code must fire when claude runs against a dead API endpoint with a bogus
key (skipped when `claude` isn't on PATH).

## MCP servers

`StartOptions.MCPServers` (the L2c leader capability's orchestrate server,
`docs/LINKED-SESSIONS-DESIGN.md` item 5) threads into claude's own
documented `--mcp-config <file>` flag — a real temp JSON file
(`mcp.go`'s `writeMCPConfig`), not an inline string, and not a `.mcp.json`
dropped into the session's cwd. `claude mcp list`/`get` show project-scoped
`.mcp.json` servers sitting "⏸ Pending approval" until a human confirms
them interactively; a headless `stream-json` session has nobody to do
that, so a `.mcp.json` server would hang forever. `--mcp-config`-supplied
servers are the CLI's own mechanism for exactly this headless case and
skip that gate. `--strict-mcp-config` is passed alongside it whenever
`opts.MCPServers` is non-empty, so an ambient project `.mcp.json` can never
silently attach and hang the session on approval.

`claude` spawns the MCP server subprocess itself, from the session's own
cwd (a worktree) — not this adapter process's cwd. `writeMCPConfig`
resolves each server's command to an absolute path via `exec.LookPath`
before writing the config, so it survives being spawned from a different
directory; a `LookPath` failure falls back to the raw token rather than
erroring, since one capability's misconfiguration must never block the
whole session launch. Both transports (`startStream`/`startPTY`) build the
same args before `p.cfg.ExtraArgs` and remove the temp config file on
`Close()` and on process-start failure.

`fakeclaude_test.go`'s `"mcp-echo"` dialect reads back the `--mcp-config`
file's exact contents and the `--strict-mcp-config` flag, proving the
adapter's launch args/config carry a server descriptor unmodified
(`TestStartStreamMCPServersMatchesOrchestrateShape`,
`TestStartPTYMCPServersMatchesOrchestrateShape`). `TestRealClaudeMCPSmoke`
is the opt-in real-binary counterpart:

```sh
MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run MCPSmoke
```

A live stream session can also change its servers without a restart:
`streamSession.SetMCPServers` (`provider.MCPHotSetter`) sends the
`mcp_set_servers` control request, which replaces claude's *dynamic*
server set. Tools reach the model from the next turn on the same
conversation. It can add servers but not remove ones passed at launch;
that case returns `provider.ErrMCPHotSetUnsupported`, and the Runner
restarts the process instead. The PTY transport doesn't implement it.
Findings and probes are in `docs/research/claude-mcp-hot-mount.md`, and
the real-binary check is `TestRealClaudeMCPHotSetSmoke` (same
`MADS_CLAUDE_SMOKE=1` gate, `-run MCPHotSetSmoke`).

Note: declaring an MCP server in an agent's own capabilities
(`agent.Capabilities.MCPServers` / `setupMCP` in `capability.go`) is a
separate, still-unwired path — those servers run as health-checked
subprocesses only, never connected to a provider's own launch by any
existing mechanism. This section covers `StartOptions.MCPServers` only.

## Pipeline vs. interactive registries

`cmd/mads/run.go` (pipeline one-shots) and `services/session/main.go`
(interactive sessions) construct two separate `claudecode.Provider`
instances with independent `Config.ExtraArgs`. The pipeline's
`-claude-args` flag supplies `--dangerously-skip-permissions` for autonomous
agents and is untouched by trust enforcement — `OneShot` has no `Trust`
field and pipeline one-shots never call `Start`. Trust only ever reaches
`startStream`/`startPTY`, which only the interactive registry drives.

## Complete: hermetic one-shot completion (Fast Model)

`Complete` (`complete.go`) implements `provider.Completer`, the optional
capability backing Fast Model's zero-config "use my provider" resolution
(`providers/fastmodel`) — stateless, no session left behind, no tools, no
conversation. Unlike `OneShot`, which is session/tool-shaped and runs in
`req.Cwd`, `Complete` runs in a fresh scratch directory with every flag
below verified against the installed `claude` CLI first (see
`testdata/complete_flags.md` — two prior tasks in this repo died on
fabricated flags):

- a throwaway `os.MkdirTemp` cwd, never the workspace — kills CLAUDE.md/
  AGENTS.md discovery and project settings, and any transcript this leaves
  behind lands under a throwaway project slug;
- `--safe-mode`, disabling CLAUDE.md, hooks, MCP, skills, plugins, and
  custom agents while leaving auth, model selection, built-in tools, and
  permissions working normally (`--bare` was rejected — it forces
  `ANTHROPIC_API_KEY`-only auth, silently breaking OAuth/keychain users);
- `--setting-sources ""` plus a per-call `--settings` file with no hooks/
  preferences, so `~/.claude/settings.json` never loads;
- `--strict-mcp-config --mcp-config '{"mcpServers":{}}'`, so no MCP server
  (including an ambient project `.mcp.json`) attaches;
- `--tools ""` (every tool denied);
- an explicit `--model`, defaulting to `DefaultCompleteModel`
  (`claude-haiku-4-5-20251001`) but overridable per call.

JSON validation and the single bounded repair-retry are deliberately absent
here — they live in the `fastmodel` layer only, never per-provider, so
every backend (this one, the OpenAI-compatible endpoint backend) is held to
the same retry contract.

Hermeticity is asserted, not assumed: `TestRealClaudeCompleteHermeticity`
(`complete_test.go`, opt-in, `MADS_CLAUDE_SMOKE=1`) plants a `CLAUDE.md`, a
`SessionStart` hook, and a decoy `.mcp.json` in the surrounding directory,
then asserts none of it is observable in the reply and that
`usage.input_tokens` falls within the documented overhead budget
(`testdata/complete_flags.md`: ~154-165 tokens above the constructed
prompt+system length) — if a future CLI update starts injecting ambient
context, this test fails loudly rather than silently degrading.

**Latency:** a `Complete` call pays claude-code's own process-spawn cost
(roughly 1-2s), on top of whatever the model itself takes. That's
acceptable because every Fast Model job (`providers/fastmodel`) is
debounced or one-off background work — task synthesis, summaries, retro
steps, NL agent generation — never a request blocking on a human's typed
input. Interactive consumers that need lower latency should configure the
OpenAI-compatible endpoint backend (`fastmodel.DynamicConfig.BaseURL`)
instead of relying on the zero-config provider default.

## Conversation keys

`docs/CHAT-STREAM-DESIGN.md` §1: a key identifies one item of the
conversation, the same on the live event (`Event.Key`) and the stored row
(`Message.Key`), with the same text. `keys.go` derives them; all from
claude's own ids, verified against 2.1.285 (`testdata/keys-*`, real
captures; `TestCapturedKeysLiveEqualStored`):

| Item | Live | Stored |
|---|---|---|
| Assistant text / thinking | `message_start.message.id` + the block's `content_block_start` index | the line's `message.id` + its block's position among that id's lines, counted after the uuid de-dupe and over every block type (signature-only thinking included; claude writes one block per line) |
| Tool call / result | `tool_use` id | `tool_use` id (on both rows) |
| User message | — (the runner owns it) | `user:` + the line uuid: mads's id when sent with `SendKeyed` (stdin `"uuid"`); a mid-turn send is a `queued_command` attachment, keyed by its `source_uuid` |
| Interrupt marker, "No response requested.", API-error line, task notification | — | the line's own uuid, `Notice` set (a task notification's line is a user line when idle, a `task-notification` `queued_command` attachment mid-turn; one notice written both ways renders once) |
| Peer message (`inbound`) | unkeyed | unkeyed: no capture shows the replayed line's uuid equal to the stored one |

**Stored thinking** (`stored_thinking: native`): with
`--settings '{"showThinkingSummaries":true}'` claude streams thinking text
and writes the same text to the JSONL (the 2.1.285 haiku `testdata/keys-think`
capture: two of two blocks equal). `KeyedTranscript` and `TranscriptSince`
return each as a Role `"thinking"` row under the block's key; plain
`Transcript` never does, so its consumers are unchanged. By default the
thinking is signature-only on both sides: no live item, no row. An
interrupted message has no `message_stop`, and its partial text is
stored as streamed, cut wherever the abort landed.

**Settle hints** (`StartOptions.Keyed` only): an `EventSettle` after
each main-agent `message_stop` (a message's JSONL lines are written
together ~30 ms after it), after each tool result (~100 ms), and just
before each turn end.

**`TranscriptSince`** keeps the file's identity, the byte offset past the
last whole line, and the reader's state (seen uuids, per-message block
counts, peer de-dupe) in its cursor, so chained reads key every row as a
full `KeyedTranscript` does. A half-written last line waits for the next read;
a compaction boundary waits for its summary line. A file that shrank or
was replaced (different inode) is `ErrCursorStale`.

**Interrupts**: claude closes an interrupted turn with an `is_error`
`error_during_execution` result that has no text. The adapter still emits
the empty `EventError` it always has before the `Interrupted` turn end, so
consumers are unchanged; the stable_keys probe tolerates an error after the
interrupt it asked for.

## Testing without claude installed

`fakeclaude_test.go` re-execs the test binary as a scripted stream-json
stand-in (`MADS_FAKE_CLAUDE=1`) — real subprocess spawn, real stream-json
parsing. `providers/conformance`'s full suite
(`TestConformanceAgainstScriptedBinary`) runs against that stand-in in CI —
no claude install required, and no live LLM is ever called.

`TestRealClaudeSmoke` is opt-in and exercises the actual binary:

```sh
MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run Smoke
```

It skips cleanly if `claude` isn't on `PATH`.

`TestRealClaudeStableKeysSmoke` (same switch, `-run StableKeysSmoke`) runs
the conformance stable_keys probe on haiku: a plain reply, text / Bash `ls`
/ text, and a count to 300 interrupted mid-text, comparing every item's
key and text live against the session's JSONL. Its scratch project dir is
removed afterwards.
