# Handoff: Leader tools: live get_summary via turndigest + get_turns drill-down

**Architect**: Claude Haiku 4.5
**Executor**: Claude Sonnet 5
**Date**: 2026-07-27
**Task**: rework handleOrchestrateSummary to compress live turn-ended envelopes via pkg/turndigest; add get_turns tool for drill-down into cited ranges

## Executor Notes (what actually shipped)

Implemented in `services/router/internal/api/orchestrate.go`, `services/router/internal/api/api.go` (route registration), and `pkg/orchestratemcp/{tools,client,orchestratemcp}.go` (+ tests in both packages).

**Deviations from this handoff's design, and why:**

1. **Summary is no longer a short-circuit when live turns exist.** The handoff's step-by-step said "keep durable summary check and return unchanged" as step 1, then "for live path" as a separate step 2 — read literally, that means a subject with *both* a Summary and turn_ended envelopes would still only ever return the (possibly stale) Summary, never reaching compression. That directly contradicts the Overview's "prepend Session.Summary as prologue" and the acceptance criterion "with the durable summary as prologue when present," and reproduces exactly the staleness bug the task exists to fix. I resolved it as: **turn_ended envelopes present → always compress, with Summary (if any) carried as a `prologue` field alongside the compressed text; no envelopes → exactly today's behavior (Summary short-circuit, else raw tail)**. This keeps `TestOrchestrateSummaryUsesSessionSummary` (no envelopes in that fixture) passing unchanged, and satisfies both AC bullets. **Reviewer: please double check this reading against intent** — it's the one place I overrode the handoff rather than followed it literally.

2. **Turn numbering uses the envelope's `Seq`**, not a locally-counted ordinal. There's no per-turn counter anywhere in the system (`pushTurnEnded` doesn't stamp one) and the Data Service's HTTP `/events` endpoint doesn't expose `Since`/`Until`/`Limit` query params (only `since`, `session_id`, `types` — see `services/data/internal/api/events.go:parseFilter`), so a "last k, filtered range" query isn't available over the wire. Using the envelope's own `Seq` (global, gapless, monotonic, already returned to the caller) as the turn anchor sidesteps both problems and is exactly what get_turns needs to recover the same range a digest cites — it's already stable and unique, an ordinal recomputed on every query wouldn't be. `get_summary`/`get_turns` both fetch the full since=0 batch for the subject and filter/slice in Go, matching the existing pattern in `orchestrateTranscriptTail` (already loads live/durable messages and slices a tail in Go, not via a paginated query).

3. **Per-turn entries use `turndigest.Parse(delta)`**, not a single blob `Entry{Kind: KindAssistant, Text: delta}` as the handoff's "conservative" fallback suggested. `Parse` is `Render`'s exact inverse (round-trip tested in `pkg/turndigest/turndigest_test.go:TestParseRenderRoundTrip`) and is already what `job/digest.go`'s `GenerateDigest` uses on the same envelope payload for the monitor-push digest. Using it here means get_summary's live path builds Turns from the same normalized shape the rest of the codebase already trusts, not a fresh ad hoc shape.

4. **True digest-of-digests (memoized per-turn digests) is NOT implemented.** `GenerateDigest` (job/digest.go) computes a MonitorPush-audience compressed digest per turn, but only for delivery to live monitor links — it's never persisted anywhere queryable. So there's nothing durable to reuse; get_summary's live path compresses `turndigest.Parse(delta)` of the raw deltas directly (still a single Fast Model call over up to `orchestrateLiveTurnLimit` (20) turns, still budget-bounded — just not reusing an already-compressed per-turn artifact, because none exists). The handoff itself flags this as a "can be memoized in a follow-up" risk, not a blocking AC. Left as a documented gap, not attempted.

5. **Digester is constructed inline** (`turndigest.New(s.jobs.FastModel())`) at each call site rather than added as a `server` struct field / wired in `main.go`. This matches the existing pattern in `services/router/internal/api/agents.go` (`agentgen.New(s.data, s.jobs.FastModel())`), avoids touching `server`'s constructor or `main.go` at all, and is strictly smaller.

**What to scrutinize:**
- Point 1 above — the summary-as-prologue-vs-short-circuit reading.
- `orchestrateLiveTurnLimit = 20` and `orchestrateSummaryBudgetTokens = 1000` are my picks (handoff suggested "500-1000" and didn't pin a turn count); no fixture/requirement pinned a specific number, so these are reasonable defaults, not derived constants.
- get_turns' 403 test and cited-range test are in `services/router/internal/api/orchestrate_test.go` (`TestOrchestrateGetTurnsReturnsCitedRange`, `TestOrchestrateGetTurnsNoOrchestrateLinkForbidden`); the long-transcript budget test is `TestOrchestrateSummaryLongTranscriptHonorsBudget` (100 turn_ended envelopes, asserts output length against a generous multiple of the budget — the `fake` provider's Fast Model backend replies deterministically, so this isn't testing real-model compression quality, just that the endpoint's plumbing enforces the size contract).

All of `go build ./...`, `go vet ./...`, `golangci-lint run ./services/router/internal/api/... ./pkg/orchestratemcp/...`, and the full test suites for `pkg/turndigest`, `pkg/orchestratemcp`, `services/router/internal/api`, `services/router/internal/job`, and `services/session/internal/runner` pass.

---

## Original architect handoff (below, for reference)

## Overview

The leader's `get_summary` tool today returns either a durable `Session.Summary` (when a task lands) or a raw live transcript tail (stale-or-raw for live subjects). Rework the live path to:
1. Read the subject's last k `session.turn_ended` envelopes (per-turn deltas persisted by `Runner.pushTurnEnded`)
2. Compress via `pkg/turndigest` with `audience=LeaderCatchup`
3. Prepend durable `Session.Summary` as prologue when present
4. Fallback to raw tail only when no envelopes exist

Add a `get_turns(subject, from_turn, to_turn)` tool to recover the normalized (not model-compressed) deltas for a range cited by a digest.

## Acceptance Criteria

- `get_summary` on a live subject returns compressed recent-turn prose citing a turn range, with durable summary as prologue when present
- Output size honors the budget on a long-transcript fixture
- `get_turns` returns the cited range and is 403 without an orchestrate link
- No change for subjects with no turn_ended envelopes (raw-tail fallback preserved)

## Design Decisions

### 1. Data Flow: turn_ended Envelopes → Digest

**Files to read**:
- `services/router/internal/api/orchestrate.go` — `handleOrchestrateSummary` (line ~95) and `orchestrateTranscriptTail` (line ~135)
- `services/session/internal/runner/runner.go` — `pushTurnEnded` (line ~645) to understand envelope shape
- `pkg/turndigest/turndigest.go` — `Digest(ctx context.Context, req Request)` API

**Current behavior** (lines 95–120):
1. Finds orchestrate link (gated by `findOrchestrateLink`)
2. If durable `Session.Summary` exists, returns `{"source": "summary", "text": subject.Summary}`
3. Otherwise calls `orchestrateTranscriptTail` → fetches live transcript (child first, durable fallback) → takes tail last 20 messages → joins as plain text

**New behavior**:
1. Keep durable summary check and return unchanged (lines 116–118)
2. For live path (new):
   a. Query subject's `session.turn_ended` envelopes via `s.data.Query(ctx, Filter{SessionID: subjectID, Types: []string{"session.turn_ended"}, Limit: k})`
   b. If envelopes exist, extract and decompress deltas → convert to `turndigest.Turn` + `turndigest.Entry` structs → call `s.digester.Digest(ctx, turndigest.Request{Turns: turns, Audience: turndigest.LeaderCatchup, BudgetTokens: budget, Subject: subjectID})`
   c. If no envelopes OR digest fails (best-effort), fallback to existing raw tail
   d. Return `{"source": "compressed_recent", "text": result.Text, "from_turn": result.FromTurn, "to_turn": result.ToTurn}` or fallback object

### 2. Digester Instance

The `handleOrchestrateSummary` function receives a digester at server startup. Check how existing code instantiates services/runner with `pkg/turndigest.New(fastmodel.Runner)`. The router's main.go or a handler setup function should initialize `s.digester` or pass it as a dependency.

**Search path**: 
- `services/router/cmd/router/main.go` — see how digester is initialized for monitors/jobs
- `services/router/internal/api/server.go` — struct field + constructor

### 3. Turn_Ended Envelope Shape

`pushTurnEnded` (runner.go:645) appends:
```go
env := contracts.Envelope{
    Type:      contracts.TurnEnded,
    SessionID: r.sessionID,
    Payload:   mustJSON(map[string]string{"delta": delta}),
}
```

Each envelope's `Payload` is JSON: `{"delta": "<rendered-plaintext>"}`. The `delta` is the output of `r.digest.renderAndReset()` — already a plaintext rendering.

**Question**: is delta a plaintext plain rendering, or the normalized structured output? Check `services/session/internal/runner/digest.go` for `renderAndReset`. This determines whether you re-normalize or use deltas directly.

### 4. Turndigest.Entry Construction

The `turndigest.Turn` and `turndigest.Entry` types are designed for structured data (kind + text, or tool calls reduced to name+path). But the envelope's delta is plain rendered text from a monitor digest.

**Strategy**: For now, treat the entire delta as a single `Entry{Kind: KindAssistant, Text: delta}` per turn. This is conservative: it doesn't re-normalize or lose fidelity. If per-turn digests for monitors use the same Entry shape, this becomes a digest-of-digests (compress monitors' already-compressed deltas). Memoization can come later.

### 5. get_turns Tool: New Endpoint

Add a new tool to `pkg/orchestratemcp`:

**Tool definition** (in `tools.go` `toolDefs`):
```go
{
    "name": "get_turns",
    "description": "Get the raw turn deltas for a cited range; used to drill down into a digest",
    "inputSchema": {
        "type": "object",
        "properties": {
            "subject_session_id": {"type": "string"},
            "from_turn": {"type": "integer"},
            "to_turn": {"type": "integer"},
        },
        "required": ["subject_session_id", "from_turn", "to_turn"],
        "additionalProperties": false,
    },
}
```

**Client method** (in `client.go`):
```go
func (c *client) getTurns(ctx context.Context, subjectID string, fromTurn, toTurn int) (int, []byte, error) {
    path := "/sessions/" + url.PathEscape(c.sessionID) + "/orchestrate/turns?for=" + url.QueryEscape(subjectID) + "&from_turn=" + strconv.Itoa(fromTurn) + "&to_turn=" + strconv.Itoa(toTurn)
    return c.do(ctx, http.MethodGet, path, nil)
}
```

**MCP handler** (in `orchestratemcp.go` `handleToolCall`):
```go
case "get_turns":
    var args struct {
        SubjectSessionID string `json:"subject_session_id"`
        FromTurn int `json:"from_turn"`
        ToTurn int `json:"to_turn"`
    }
    if unmarshalErr := json.Unmarshal(params.Arguments, &args); unmarshalErr != nil {
        return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid arguments: " + unmarshalErr.Error()}}
    }
    status, body, err = s.client.getTurns(ctx, args.SubjectSessionID, args.FromTurn, args.ToTurn)
```

**Router endpoint** (in `orchestrate.go`):
```go
func (s *server) handleOrchestrateGetTurns(w http.ResponseWriter, r *http.Request) {
    leaderID := r.PathValue("id")
    subjectID := r.URL.Query().Get("for")
    fromTurn := parseIntQuery(r, "from_turn", 0) // helper: r.URL.Query().Get("from_turn"), strconv.Atoi
    toTurn := parseIntQuery(r, "to_turn", 0)
    
    // Same orchestrate link check as get_summary
    link, err := s.findOrchestrateLink(r.Context(), leaderID, subjectID)
    if err != nil {
        writeError(w, err)
        return
    }
    if link == nil {
        writeJSON(w, http.StatusForbidden, map[string]string{"error": "no orchestrate link to that subject"})
        return
    }
    
    // Query envelopes in range [fromTurn, toTurn]
    // Normalize (decompress delta, assemble Entry structs) but do NOT compress
    // Return as JSON array of entries
    result := s.getOrchestrateEnvelopesInRange(r.Context(), subjectID, fromTurn, toTurn)
    writeJSON(w, http.StatusOK, result)
}
```

## Implementation Path

### Phase 1: Modify handleOrchestrateSummary (live path)

1. **Extract turn_ended querying into a helper**:
   - `func (s *server) getOrchestrateEnvelopesInRange(ctx, sessionID string, fromTurn, toTurn int) []turndigest.Entry`
   - Calls `s.data.Query` with `Filter{SessionID: sessionID, Types: []string{"session.turn_ended"}}`
   - Decodes each envelope's delta payload
   - Assembles Turn+Entry structs
   - Returns raw Entries (NOT compressed)

2. **Add digester instance to server**:
   - Search `services/router/cmd/router/main.go` to see how digester is initialized
   - Add field `digester *turndigest.Digester` to `server` struct
   - Initialize in constructor

3. **Update handleOrchestrateSummary**:
   - Keep durable summary return (lines 116–118)
   - New live path:
     a. Call helper to get Entries
     b. If no entries, use existing raw tail (no change)
     c. If entries exist, build `turndigest.Request{Turns: [], Audience: LeaderCatchup, BudgetTokens: budget}`
     d. Call `s.digester.Digest(ctx, req)`
     e. Return new response format with turn range
   - Budget token: pick a reasonable default (e.g., 500–1000 tokens for leader catchup)

4. **Update response format**:
   - Existing: `{"source": "summary", "text": "..."}` or `{"source": "transcript_tail", "text": "..."}`
   - New: `{"source": "compressed_recent", "text": "...", "from_turn": N, "to_turn": M}`
   - Fallback: preserve old format when no envelopes exist

### Phase 2: Add get_turns tool

1. **Add tool definition** in `pkg/orchestratemcp/tools.go`
2. **Add client method** in `pkg/orchestratemcp/client.go`
3. **Add MCP handler** in `pkg/orchestratemcp/orchestratemcp.go`
4. **Add router endpoint** in `services/router/internal/api/orchestrate.go`
   - Same link check, same Query pattern as Phase 1 helper
   - Returns normalized Entries (NOT compressed)

### Phase 3: Testing

- Existing: `handleOrchestrateSummary` test at `services/router/internal/api/orchestrate_test.go`
- Add fixture with mock turn_ended envelopes
- Verify digest-of-digests path: MonitorPush deltas should compress well as LeaderCatchup
- Verify get_turns returns matching range
- Verify no link → 403

## Key Files

| File | Changes | Lines | Purpose |
|------|---------|-------|---------|
| `services/router/internal/api/orchestrate.go` | Add digester field, modify handleOrchestrateSummary, add getOrchestrateEnvelopesInRange, add handleOrchestrateGetTurns | ~100–150 new | Main implementation |
| `pkg/orchestratemcp/tools.go` | Add get_turns tool def | ~20 | Tool registration |
| `pkg/orchestratemcp/client.go` | Add getTurns method | ~10 | MCP→Router bridge |
| `pkg/orchestratemcp/orchestratemcp.go` | Add "get_turns" case in handleToolCall | ~15 | Dispatcher |
| `services/router/cmd/router/main.go` | Initialize digester, wire to server | ~5–10 | Startup |
| `services/router/internal/api/orchestrate_test.go` | Add test fixtures | ~50–100 | Verify behavior |

## Testing Strategy

1. **Unit**: 
   - `handleOrchestrateSummary` with mock turn_ended envelopes
   - Verify digest source + turn range
   - Verify fallback (no envelopes)

2. **Integration**:
   - Launch a live session, run a turn, confirm turn_ended envelope is persisted
   - Leader calls get_summary → compressed output with turn range
   - Leader calls get_turns with that range → recovers raw deltas
   - Verify 403 without orchestrate link

3. **Budget compliance**:
   - Use a long-transcript fixture (100+ turns)
   - Verify output fits LeaderCatchup budget (likely 1000–2000 tokens)

## Risk & Mitigations

| Risk | Mitigation |
|------|-----------|
| Digester instance missing at startup | Search main.go for existing digester usage; reuse that pattern |
| turn_ended envelope not persisted yet | Fallback to existing raw tail; no change in behavior |
| Budget too tight, digest always truncates | Adjust BudgetTokens or re-benchmark; start with 1000 |
| Digest model call fails | Best-effort: fallback to raw tail (same as no-envelopes case) |
| get_turns range misses turns | Query with `Since` + `Until` filters on turn number (convert to seq if needed) |

## Notes

- Raw-tool-I/O-stays-in-process unchanged: deltas already carry only tool name + path, no secrets
- No change for subjects with no turn_ended envelopes (fallback preserved)
- Prefer pre-generated per-turn digests (for monitors) over re-summarizing — this is a digest-of-digests pattern, but can be memoized in a follow-up
- The two sources (`Session.Summary` as prologue + compressed recent turns) are both inputs to the leader's context window — keep them separate in the response so the leader can combine them meaningfully
