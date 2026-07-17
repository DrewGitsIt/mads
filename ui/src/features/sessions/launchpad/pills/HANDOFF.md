# Trust pill: honest options with explanations

## What was done

1. **Placeholder fix.** `TrustPill.tsx` no longer shows `"Prompt each time"`
   (not a `contracts.SessionTrust` value — `full | limited | read_only`,
   `pkg/contracts/session.go:24-28`). It now fetches `router.getConfig()` in
   a `useEffect` (mirroring `WorkspacePill`'s self-fetch pattern) and shows
   `${defaultLabel} (default)` — e.g. `"Limited (default)"` — falling back to
   `"limited"` on fetch failure, matching `resolveSessionDefaults`'s own
   hardcoded fallback (`services/router/internal/api/sessiondefaults.go:54-59`).
2. **Per-option descriptions.** Moved `TRUST_LEVELS` into its own module,
   `trustLevels.ts`, adding a `description` field per value. Each popover
   option now renders a two-line block (label + description) via new
   `.optionLabel` / `.optionDescription` CSS classes in `pills.module.css`,
   not a tooltip.
3. **Reuse.** `trustLevels.ts` is the single source for this copy so it's
   ready to reuse the moment a Settings trust control or a header trust chip
   exist. Neither exists today (see "Confirmed, not touched" below) so there
   was nothing else to update.
4. Rewrote `TrustPill.test.tsx`: default-display test (asserts
   `"Limited (default)"` renders after the config fetch resolves, via
   `vi.stubGlobal("fetch", ...)` + `fixtureConfig`), the existing
   selection/`onChange` test updated for the new trigger text, and a new
   test asserting all three `trustLevels.ts` descriptions render in the open
   popover.

## Why the copy reads the way it does — enforcement gap, stated plainly

`contracts.Session.Trust` is set once by `resolveSessionDefaults`
(session → `cfg.DefaultTrust` → hardcoded `TrustLimited`) and then **used
nowhere else in the Go codebase** except the enum declaration and
`ValidateConfig`'s allowed-values check (`pkg/contracts/config.go:182-183`).
Confirmed by grep, not assumption:

- `provider.StartOptions` / `provider.OneShotRequest`
  (`providers/provider/provider.go:46-66`) — the structs every adapter
  receives to launch a session — carry no trust/permission field.
- `providers/kirocli/kirocli.go:125` hardcodes `--trust-all-tools`
  unconditionally on every call, regardless of session trust.
- `providers/claudecode` passes no permission-mode flag conditioned on trust.

**Net effect: selecting Full / Limited / Read-only has zero runtime effect
today** — every session runs identically regardless of trust level. Wiring
that up is a `providers/*` + `StartOptions` change, cross-cutting and out of
scope here.

This is why the option copy uses "may" framing (`"Agent may edit files and
run any command without asking"`) rather than "will/won't" framing — it
states each level's **declared meaning** per `contracts.SessionTrust`'s doc
comment and `DefaultConfig`'s rationale (`pkg/contracts/config.go:146-148`:
default is `TrustLimited`, not `TrustFull`, so sessions don't default to
unattended full-trust execution). Asserting active gating ("won't run
commands") would be false today and would just relocate the "opaque/dishonest
UI" complaint from the placeholder into the options themselves.

| value | label | description |
|---|---|---|
| `full` | Full access | "Agent may edit files and run any command without asking." |
| `limited` | Limited | "The safer default — agent may edit files and run everyday commands, avoiding destructive ones." |
| `read_only` | Read-only | "Agent may look around and suggest changes, but may not edit files or run commands." |

**Reviewer: this enforcement gap is a real product issue, not something this
task fixed or should have fixed.** Flagging it as a follow-up work item
(providers need to consume `Session.Trust` and gate accordingly) rather than
building it here.

## Confirmed, not touched (findings only)

- **"Prompt each time" is not a trust mode anywhere.** Checked spec §13 and
  MADS-UX §3.2/§3.3 — neither proposes a 4th "ask" trust mode.
  REBUILD-SPEC line 1064 names a different, stale `manual/accept-edits/auto`
  scheme for Settings' "Default trust level" field that doesn't match
  `contracts.SessionTrust` either — that's pre-existing MADS-UX/REBUILD-SPEC
  drift (same category already logged in memory as `mads-ux-spec-gaps`), not
  reconciled here. Per the task brief, not inventing a 4th value.
- **Settings has no trust control to reuse copy into.** `Config.default_trust`
  / `ProfileSettings.default_trust` exist as contract fields
  (`ui/src/router/types.ts:504,530`) but no panel under
  `ui/src/features/settings/` renders a trust control at all.
- **No trust pill/chip in the session header.** `ConversationHeader.tsx`'s
  own comment documents this as a deliberate, already-decided scope
  exclusion from a separate work item (frontend `Session` type has `trust?`
  but the header doesn't surface it).
- No change to `contracts.SessionTrust`'s three values, no
  `providers/*`/`StartOptions` enforcement wiring.

## Verification

`make check` green: Go tests (all packages `ok`), `golangci-lint`, UI
`eslint .` (clean), and the full `vitest` suite (177 files / 732 tests
passed, including the 3 rewritten `TrustPill.test.tsx` cases). Also ran
`tsc --noEmit` standalone — no type errors.

## Reviewer should scrutinize

- The exact option copy wording (table above) — I kept the architect's
  proposed strings; if terser/clearer phrasing is preferred, `trustLevels.ts`
  is the single place to change it.
- Whether "may" framing reads as honest enough given the enforcement gap, or
  whether the pill should additionally surface the gap in-product (e.g. a
  banner) rather than only in this handoff / commit message — I judged that
  out of scope since the task only asked for accurate option copy, not new
  UI surfacing the enforcement gap itself.
