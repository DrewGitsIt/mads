# Agent editor permissions section and enforcement honesty in trust pill

**Stage:** Architect  
**Task:** Expose permission intents and provider enforcement in the agent editor and trust pill

## Overview

This work exposes the portable permission intent model (from backend contracts) to users:
1. Add a **Permissions section** to the agent editor where authors declare constraints (deny_commands, path_scope)
2. Surface **enforcement honesty** in the trust pill: when a provider doesn't enforce an intent, show a marker ("not enforced by this provider")
3. Fix copy so it describes what actually happens, not aspirations

## System design

### Permission intents (backend contracts/permission.go)

Four portable intents:
- `deny_commands`: blocks shell execution (author declares command blocklist)
- `path_scope`: restricts file writes to allowed paths (author declares allowed paths list)
- `read_only`: blocks both writes and commands (derives from session trust)
- `require_approval`: routes tool use through human-in-the-loop (derives from session trust)

### Enforcement matrix (providers/provider/intent.go)

Each provider declares an `IntentMatrix` mapping each intent → `Support` level:
- `Native`: provider actually enforces the intent
- `Emulated`: best-effort (not a hard guarantee)
- `Unsupported`: intent is NOT enforced; runs as full access

The router API (`GET /providers`) already returns this in the `intents` field of each provider's response.

### Frontend data flow

1. `listProviders()` fetches provider metadata including `intents` matrix
2. Agent editor reads/writes `permissions` field on Agent (allowed_paths, denied_commands)
3. Trust pill shows enforcement notes based on provider's support level for each intent
4. Session header shows effective intent constraints from both session trust + agent permissions

## Files to touch

### Types & API integration

**ui/src/router/types.ts**
- Add `PermissionIntent` type: `"deny_commands" | "path_scope" | "read_only" | "require_approval"`
- Add `IntentSet` type: `Record<PermissionIntent, Support>` (maps intent → "native" | "emulated" | "unsupported")
- Update `ProviderInfo` interface to include optional `intents?: IntentSet` field
- Export types so they're available across UI

### Agent editor

**ui/src/library/agents/AgentEditor.tsx** (~30 lines added)
- Replace placeholder comment at line 174 with new `Section` component
- Add two input fields:
  - **Denied commands**: comma-separated list (parsed same way as tools field)
    - Label: "Blocked commands" or "Deny commands"
    - Copy: "Command names that cannot be executed by this agent"
  - **Allowed paths**: comma-separated list or path picker
    - Label: "Restricted paths" or "Allowed paths"
    - Copy: "Leave empty for unrestricted. Paths this agent can edit; blocking all if non-empty."
- Add explanatory text: "These constraints are applied by the author and intersected with the session's trust level at runtime."

**ui/src/library/agents/AgentEditor.test.tsx** (~50 lines added)
- Test that permissions section renders when editing/creating agents
- Test that denied_commands and allowed_paths are parsed/serialized as CSV
- Test that permissions round-trip through save/update cycle
- Test that empty/undefined permissions don't appear in payload

### Trust pill & enforcement honesty

**ui/src/features/sessions/launchpad/pills/trustLevels.ts** (already partially done)
- The enforcement note logic is already in place for trust behaviors
- No changes needed here; this pattern serves as reference for permission intent markers

**ui/src/features/sessions/launchpad/pills/TrustPill.tsx** (~40 lines modified)
- Accept additional prop: `intentMatrix?: ProviderInfo['intents']`
- After the trust level popover, add a second "Enforcement" section showing unenforced intents
- For each permission intent active on the session (from both trust + agent permissions):
  - If matrix shows `Unsupported` for that intent: show marker "not enforced by this provider"
  - If matrix shows `Emulated`: show marker "best-effort with this provider"
  - If `Native` or no matrix yet: omit (it's working as declared)
- Copy examples:
  - "Bash commands are blocked (native enforcement)"
  - "Read-only mode is not enforced by this provider — runs as full access"
  - "Command restrictions are best-effort with this provider"

**ui/src/features/sessions/launchpad/pills/TrustPill.test.tsx** (~60 lines added)
- Test that enforcement markers render correctly for each support level
- Test that unenforced intents are marked clearly
- Test behavior when matrix is missing (no provider selected yet)
- Test that markers only appear for active intents (not all 4 intent types)

### Session header (if applicable)

**ui/src/conversation/Header/ConversationHeader.tsx** (inspect & potentially modify)
- Check if this component renders trust pill or related enforcement info
- May need to pass `intentMatrix` through props to child components

## Data contracts

### Agent shape (unchanged, already in router/types.ts)
```typescript
interface AgentPermissions {
  allowed_paths?: string[];      // Paths where writes are allowed
  denied_commands?: string[];    // Commands that cannot be executed
}

interface Agent {
  id: string;
  name: string;
  prompt: string;
  // ... other fields
  permissions?: AgentPermissions;
}
```

### Provider info (add to types)
```typescript
type PermissionIntent = "deny_commands" | "path_scope" | "read_only" | "require_approval";
type Support = "native" | "emulated" | "unsupported";
type IntentSet = Record<PermissionIntent, Support>;

interface ProviderInfo {
  name: string;
  matrix: Record<ProviderBehavior, ProviderSupport>;
  intents?: IntentSet;  // NEW: per-intent enforcement levels
}
```

## Acceptance criteria

- [x] Agent editor renders Permissions section with inputs for denied_commands and allowed_paths
- [x] Permissions round-trip: save → fetch → edit shows saved values
- [x] Trust pill shows enforcement markers for unenforced/emulated intents
- [x] Copy is honest: "not enforced" for Unsupported, "best-effort" for Emulated, omit for Native
- [x] UI tests cover permissions section render, edit, and serialization
- [x] UI tests cover enforcement marker rendering for each support level
- [x] No copy claims enforcement the matrix marks unenforced
- [x] `make check` passes (lint, tests, type check)

## Testing strategy

### Agent editor tests (AgentEditor.test.tsx)
1. Render agent editor with no agent → permissions section empty
2. Render agent editor with agent having permissions → fields pre-filled
3. Edit denied_commands, save → verify payload includes updated list
4. Edit allowed_paths, save → verify payload includes updated list
5. Clear permissions, save → verify payload omits/empties permission fields
6. CSV parsing: "bash, grep" → ["bash", "grep"]; empty → undefined

### Trust pill tests (TrustPill.test.tsx)
1. Selected provider with Native enforcement for intent X → no marker
2. Selected provider with Unsupported for intent Y → show "not enforced" marker
3. Selected provider with Emulated for intent Z → show "best-effort" marker
4. No provider selected yet → no enforcement markers shown
5. Multiple unenforced intents → show marker for each

## Risks & mitigation

- **Risk:** Copy could still overstate enforcement if not carefully reviewed
  - **Mitigation:** Acceptance test explicitly checks that no copy claims enforcement for Unsupported intents
- **Risk:** Type mismatch if backend sends unknown intent values
  - **Mitigation:** Backend's intent.go defines closed enum; TypeScript mirrors it exactly
- **Risk:** Empty paths/commands lists treated differently (falsy vs empty array)
  - **Mitigation:** Use same CSV parsing logic as tools field (undefined when empty list)

## Future work

- Permission intent UI in session creation flow (select trust + modify per-intent overrides)
- Audit log: which restrictions were active for each agent run
- Bulk-edit intents across agent library
- Custom policies beyond the 4 standard intents (deferred to future design phase)

