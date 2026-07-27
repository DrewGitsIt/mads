# HANDOFF: Expose permission intents and provider enforcement in agent editor and trust pill

Task: tsk_48bc0b95ec91d3fa

## Recovery note

This branch's tip (`de55d46`) was a *second* architect pass that re-designed
this same task from scratch, landing a stale placeholder doc at
`ui/src/library/agents/HANDOFF.md` — apparently unaware that a prior session
had already fully implemented, tested, and verified this exact task. That
prior work only survived on `backup/tsk_48bc0b95ec91d3fa` (commits `878b050`
executor HANDOFF, `bbdb84a` the actual implementation) and was never merged
to this branch.

Per the repo's backup-branch recovery convention, I compared the two: the
backup's implementation was materially more complete than a fresh attempt
would be (it honestly exposes all 4 permission intents in the agent editor —
including `read_only`/`require_approval` as explained, disabled rows — rather
than silently omitting the two intents `AgentPermissions` doesn't yet have
fields for). I cherry-picked `bbdb84a` onto this branch, re-verified it
against current `main` (type check, lint, full `vitest` suite, `make check`),
and am shipping it as this task's result rather than duplicating the work.

## What was done (from bbdb84a, re-verified this session)

1. **Permissions section in AgentEditor** (`ui/src/library/agents/AgentEditor.tsx`):
   `deny_commands` (denied command patterns) and `path_scope` (allowed paths)
   are editable via `ChipListField`, active iff the list is non-empty —
   matching `AgentPermissions.Intents()` (`pkg/contracts/permission.go`)
   exactly, so round-trip semantics hold with no extra "enabled" boolean.
   `read_only`/`require_approval` render as disabled, explained rows (no
   `AgentPermissions` field exists for them yet — they're session-trust-only
   today). A leading note states these are author-side constraints that
   combine with the session's trust choice at runtime. The editor has no
   provider context (`AgentsShelf.tsx` mounts it standalone), so it makes no
   enforcement claim at all — just "enforcement depends on the provider
   chosen when starting a session."
2. **Shared intent logic** (`ui/src/permissions/intents.ts`, new): single
   source for intent labels/descriptions, `agentIntents(perms)` (TS mirror of
   `AgentPermissions.Intents()`), `intentSupport`/`intentEnforcementNote`
   (matrix lookup + honesty copy, absent matrix defaults to `"unsupported"`),
   and `agentIntentNotes(perms, matrix)`. Used by both AgentEditor and
   TrustPill so the "what counts as enforced" logic can't drift between them.
3. **Types** (`ui/src/router/types.ts`): `PermissionIntent`, `IntentMatrix`
   (mirrors `providers/provider/intent.go`), and `intents?: IntentMatrix` on
   `ProviderInfo`. Backend already returns this via `GET /providers`
   (`services/router/internal/api/reads.go`) — verified directly against the
   Go source, no backend change needed.
4. **TrustPill honesty** (`ui/src/features/sessions/launchpad/pills/TrustPill.tsx`):
   new optional `agentPermissions`/`intentMatrix` props. When the selected
   agent declares any intent, the popover grows a "This agent also declares"
   block below the existing trust-level rows, one line per declared intent
   with its honesty note (native → "enforced", emulated → "best-effort... not
   a hard guarantee", unsupported/absent → "not enforced"). Renders nothing
   when the agent declares nothing. Existing trust-level rows/`trustLevels.ts`
   are untouched.
5. **Launchpad wiring** (`ui/src/features/sessions/launchpad/Launchpad.tsx`):
   `selectedAgent` state (mirrors `selectedWorkspace`) captured from
   `AgentPill.onChange`, passing `selectedAgent?.permissions` and the matched
   `ProviderInfo.intents` into `TrustPill`.
6. **Fixtures**: `claude-code` fixture provider gained an `intents` entry;
   `fake` deliberately has none, to keep exercising the "provider doesn't
   implement IntentEnforcer" absent case in dev/tests.
7. **Tests**: `ui/src/permissions/intents.test.ts` (new, unit tests for the
   shared logic), plus new cases in `AgentEditor.test.tsx` (Permissions
   section renders all 4 intents, never claims enforcement; edit + save
   round-trip) and `TrustPill.test.tsx` (agent-declared-intent honesty rows).

## What was NOT done

- **Session header** (`ConversationHeader.tsx`): doesn't display trust or
  permissions at all today post-session-creation, so there was no existing
  enforcement claim to fix there, and adding a new surface is a bigger change
  than the brief's "expose honestly what exists." Scoped to the Launchpad's
  TrustPill, which is where trust + provider + agent overlap and what the
  acceptance criteria ("trust pill flow") names. If enforcement honesty needs
  to be visible *during* a live session, that's a follow-up task.
- **Backend schema extension for agent-level `read_only`/`require_approval`**:
  not attempted — out of scope for a UI task (Go struct + data-service schema
  + migration). The editor is honest about the gap (disabled rows, explained).
- **Settings → Providers panel**: no Intents column added there; out of scope
  per the task brief.

## Where to scrutinize

- `agentIntents()` (`ui/src/permissions/intents.ts`) must stay in lockstep
  with `AgentPermissions.Intents()` (`pkg/contracts/permission.go`) — no
  shared codegen between them, just a comment pointing each way. If the Go
  side changes, this drifts silently until a test catches it.
- The AgentEditor's `permissions` payload is now always sent, even when both
  lists are empty (mirrors how `capabilities` is already always sent).
  `AgentPermissions` fields are all `omitempty` in Go and the existing update
  path already sends `capabilities` the same way, so this should be
  consistent with existing behavior, but worth a second look if the data
  service ever distinguishes "sent but empty" from "field omitted."
- This recovery: the stale `ui/src/library/agents/HANDOFF.md` placeholder
  doc from the second architect pass is still present on this branch and
  should be swept along with this file per the usual pre-merge HANDOFF
  cleanup — it describes a design that was superseded before this commit.
- `make check` (golangci-lint, `go test -race ./...`, `npm run lint`, `npm run
  test`) all pass locally. No Go files were touched.
