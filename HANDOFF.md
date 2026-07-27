# HANDOFF: pkg/turndigest — shared n-turn compression for agent readers

Design stage output. Executor implements; no code has been written yet.

## Task

One package owning "n turns of agent activity in, minimum tokens for a reading
LLM out," consolidating today's sibling digest paths. Refactor
`job.GenerateDigest` to route through it, behavior-preserving. Acceptance:

1. `GenerateDigest` routes through turndigest with **unchanged observable
   behavior** (existing tests in `services/router/internal/job/digest_test.go`
   and the digest subtest of `unconfigured_test.go` pass **unmodified**).
2. An under-budget delta returns `Source=verbatim` with **zero** backend calls
   (counting fake).
3. Normalize collapses repeated tool calls and caps entry length (golden
   tests, via `pkg/golden`).
4. The package imports at most contracts/dataclient/fastmodel — no router or
   session service internals. (This design needs only
   `providers/fastmodel` + stdlib.)

## Key constraints discovered (read before coding)

- **The delta crosses the wire as a rendered string, not entries.** The
  session child renders `digestEntry`s to `"user: …\nassistant: …\ntool: N P\n"`
  (`services/session/internal/runner/digest.go:57` `renderAndReset`), appends
  it as `session.turn_ended` payload (`runner.go:642` `pushTurnEnded`), and
  `digestwatch/watcher.go:91` hands that string to
  `job.GenerateDigest(sessionID, delta)`. So turndigest must export a `Parse`
  for its own canonical rendering. Do **not** change the wire payload in this
  task (running children would emit the old shape — payload skew).
- **Existing tests pin exact behavior for tiny deltas.**
  `TestGenerateDigestDeliversToPerTurnReadersOnly` sends
  `"user: hi\nassistant: did stuff\n"` and asserts exactly **1** backend call
  and delivered text == the model's reply. So GenerateDigest must NOT get a
  verbatim short-circuit. This is why `BudgetTokens <= 0` must mean "always
  compress" (nothing fits a zero budget) — GenerateDigest passes 0 and
  behavior is preserved with no special-casing.
- **Unconfigured backend ⇒ zero deliveries, no job.failed**
  (`unconfigured_test.go` digest subtest). With budget 0, the skippable-error
  degrade path ("truncated verbatim") truncates to zero tokens ⇒ empty text ⇒
  GenerateDigest skips the push. Falls out of the same rule; don't add a
  separate IsSkippable check in job code.
- `fastmodel.Runner.DoText` is the text path; `fastmodel.IsSkippable` covers
  `ErrBackendUnconfigured` + `ErrUnusableOutput`
  (`providers/fastmodel/fastmodel.go:44`). Non-skippable errors (transport,
  rate limit) must still propagate out of GenerateDigest as
  `fmt.Errorf("digest: compress: %w", err)` — today's job-failure semantics.

## Files

### New: `pkg/turndigest/` (package turndigest)

Suggested layout: `turndigest.go` (types + Digest), `normalize.go`,
`render.go` (Render/Parse), `prompt.go`, tests + `testdata/`. Match the
repo's doc-comment density; package doc should name the consolidation intent
and docs/LINKED-SESSIONS-DESIGN.md decision 4.

```go
// Entry is the shared shape promoted from the session child's digestEntry
// (services/session/internal/runner/digest.go) / synth's deltaEntry: text
// verbatim, tool uses reduced to {name, path} — raw tool I/O never leaves
// the session process.
type Entry struct {
    Kind string // KindUser | KindAssistant | KindTool
    Text string
    Tool string
    Path string
}

const (
    KindUser      = "user"
    KindAssistant = "assistant"
    KindTool      = "tool"
)

// Turn is one turn's entries plus the caller's anchor number (0 = unknown).
type Turn struct {
    Number  int
    Entries []Entry
}

type Audience string

const (
    MonitorPush   Audience = "monitor_push"   // per-turn push to a read-link monitor
    LeaderCatchup Audience = "leader_catchup" // reserved: get_summary's transcript tail
    RunDigest     Audience = "run_digest"     // reserved: pipeline-run digests
)

type Request struct {
    Turns        []Turn
    Audience     Audience
    BudgetTokens int    // reader's verbatim tolerance AND truncation cap; <=0 = always compress, degraded fallback is empty
    Subject      string // optional prompt context ("session build-fix", "task tsk_x"); empty = omitted
}

type Source string

const (
    SourceVerbatim   Source = "verbatim"
    SourceCompressed Source = "compressed"
)

type Result struct {
    Text     string
    Source   Source
    FromTurn int // Turns[0].Number
    ToTurn   int // Turns[len-1].Number
}

type Digester struct{ fm *fastmodel.Runner }

func New(fm *fastmodel.Runner) *Digester

func (d *Digester) Digest(ctx context.Context, req Request) (Result, error)

// Render / Parse are the canonical line format ("user: …", "assistant: …",
// "tool: Name Path"), moved from runner's renderAndReset / synth's
// renderDelta. Parse is Render's inverse: lines starting with a known
// "<kind>: " prefix open an entry; any other line is a continuation of the
// previous entry's Text (joined with "\n"). Lossy only when entry text
// itself contains a line starting with a prefix — acceptable, best-effort.
func Render(entries []Entry) string
func Parse(delta string) []Entry
```

`Digest` pipeline (all steps deterministic except the one model call):

1. Anchors: `FromTurn = Turns[0].Number`, `ToTurn = last.Number` (0,0 when no
   turns). No entries at all ⇒ `Result{Text: "", Source: SourceVerbatim}`,
   nil error, no model call.
2. **Normalize** each turn's entries (pure function, unexported):
   - merge runs of adjacent `KindAssistant` entries into one, Text joined
     with `"\n"` (same bytes as today's rendering, minus repeated prefixes);
   - collapse runs of adjacent `KindTool` entries with identical
     `(Tool, Path)` into one rendered with a count suffix:
     `"tool: Read foo.go ×6"` (count 1 renders with no suffix). Use an
     internal render-item struct with a count field — do NOT add Count to the
     shared Entry;
   - cap every entry's Text at `maxEntryRunes` (const, suggest 400 runes),
     rune-safe, `"…"` appended when cut.
3. **Render** the normalized turns. When `len(Turns) > 1`, precede each
   turn's block with a `"— turn N —"` header line (omit header when Number
   is 0); when `len(Turns) == 1`, no header — the n=1 rendering must look
   like today's prompt body.
4. **Budget check**: `estimateTokens(s) = len(s)/4` (bytes; crude is fine —
   it's a threshold, document it). If `estimate <= req.BudgetTokens` ⇒
   `Result{Text: rendering, Source: SourceVerbatim}`, **zero backend calls**.
   `BudgetTokens <= 0` never fits ⇒ always proceeds to compress.
5. **One model call**: `d.fm.DoText(ctx, fastmodel.Job{System: prompt, Prompt: rendering})`.
   Prompt = the audience's system prompt; when `Subject != ""` append a
   sentence naming it; when `BudgetTokens > 0` append a prompt-side length
   target ("Reply in at most ~N tokens."). **With MonitorPush, budget 0, no
   subject, the system prompt must be byte-identical to today's
   `digestSystemPrompt`** (moved from `services/router/internal/job/digest.go:19`):

   > You compress one turn of an AI coding agent's activity into a short
   > digest for someone monitoring the session. Plain prose, a few sentences
   > at most: what was asked, what the agent did.

   LeaderCatchup / RunDigest prompts: same register, written fresh
   (catchup: "n turns of a session you delegate to, for a leader agent
   catching up"; run: model on `run_digest.go:17`'s runDigestSystemPrompt).
   Unknown Audience values fall back to the MonitorPush prompt — never error.
6. **Error handling**: `fastmodel.IsSkippable(err)` ⇒ degrade, nil error:
   `Result{Text: truncate(rendering, req.BudgetTokens), Source: SourceVerbatim}`
   where `truncate` cuts rune-safe at ~`BudgetTokens*4` bytes with `"…"`, and
   budget <= 0 yields `""`. Any other error returns `(Result{}, err)` — the
   caller decides (job path fails the job, same as today).
7. **Success**: `Source: SourceCompressed`. Backstop: if `BudgetTokens > 0`
   and the reply estimates over ~2× budget, hard-truncate it (same truncate
   helper). Budget 0 ⇒ reply passes through untouched (today's behavior).

### Modified: `services/router/internal/job/digest.go`

- Delete `digestSystemPrompt` (it moves to turndigest verbatim).
- Replace the `deps.FastModel.DoText(...)` block (lines 67–75) with:

```go
res, err := turndigest.New(deps.FastModel).Digest(ctx, turndigest.Request{
    Turns:    []turndigest.Turn{{Entries: turndigest.Parse(delta)}},
    Audience: turndigest.MonitorPush,
    // BudgetTokens deliberately 0: monitors always get a compressed digest,
    // and a skippable backend degrades to empty ⇒ no push (both today's
    // behavior).
})
if err != nil {
    return fmt.Errorf("digest: compress: %w", err)
}
if res.Text == "" {
    return nil // skippable backend (or empty delta): skip, not a failure
}
text := res.Text
```

- Keep the ordering: the Digest call stays **after** target filtering /
  `len(targets) == 0` early return (the manual-only test asserts zero backend
  calls).
- Drop the now-unused `providers/fastmodel` import from this file.
- Do not touch `title.go`, `run_digest.go`, `summary.go`, or `synthesis.go`.

### Modified: `services/session/internal/runner/digest.go`

Promote the shape: replace `digestEntry` with `turndigest.Entry`
(`buf []turndigest.Entry`, Kind constants), and `renderAndReset`'s inline
switch with `turndigest.Render(d.buf)`. Render is the same code moved, so
output is byte-identical — the wire format does not change. Keep the
accumulator, mutex, and reset semantics exactly as they are; keep the
doc comments' pointers but note the shape now lives in `pkg/turndigest`.

### Untouched (verify no other references break)

`services/session/internal/synth/transcript.go` keeps its own
`deltaEntry`/`renderDelta` for now (identical logic; switching it to
`turndigest.Render` is an optional follow-up, not required — skip unless
trivial). `digestwatch/watcher.go` unchanged.

## Tests

New, in `pkg/turndigest` (external test package, mirror
`job/digest_test.go`'s `countingBackend` — a `fastmodel.Backend` counting
`Complete` calls; build the runner with `fastmodel.NewWithBackend`):

1. **Verbatim short-circuit**: few small entries, `BudgetTokens: 4096` ⇒
   `Source == SourceVerbatim`, backend count 0, Text == normalized rendering.
   (Acceptance criterion 2.)
2. **Golden normalize** (`pkg/golden`, `testdata/`): input with (a) 6
   consecutive `tool: Read foo.go` entries, (b) interleaved non-identical
   tools that must NOT collapse, (c) adjacent assistant fragments, (d) one
   over-long entry ⇒ golden-assert `Result.Text` of the verbatim path shows
   `Read foo.go ×6`, merged assistant block, `"…"`-capped entry. A
   multi-turn golden covering `— turn N —` headers. (Criterion 3.)
3. **Compression path**: over-budget input, backend replies `"short"` ⇒
   `Source == SourceCompressed`, count 1; capture the system prompt and
   assert it contains the length target when budget > 0, and that with
   `MonitorPush`/budget 0 it equals the legacy prompt string exactly.
4. **Skippable degrade**: backend whose Complete returns
   `fastmodel.ErrBackendUnconfigured` — budget > 0 ⇒ nil error, verbatim
   truncated text; budget 0 ⇒ nil error, `Text == ""`. Non-skippable error ⇒
   error propagates.
5. **Anchors**: multi-turn request ⇒ `FromTurn`/`ToTurn` from first/last
   `Turn.Number`.
6. **Parse/Render round-trip**: `Parse(Render(entries)) == entries` for
   single-line texts; multiline text folds back via continuation lines.

Existing suites that must pass **unmodified** (criterion 1):
`go test ./services/router/internal/job/ ./services/session/... ./pkg/turndigest/`.
Full `go build ./... && go vet ./...` before handing to review.

## Risks / traps

- The whole behavior-preservation story hangs on budget-0 semantics
  (always-compress, empty degraded fallback). If you find yourself adding an
  `IsSkippable` check or a "minimum budget" default inside GenerateDigest or
  Digest, you've drifted — re-read the constraints section.
- Normalize must not alter the n=1 prompt shape beyond the specified
  collapses: no headers, no extra blank lines, trailing `"\n"` preserved to
  match `Render`'s current output.
- `Parse` is best-effort on adversarial text (an entry line starting with
  `"assistant: "` inside user text splits). Fine for digests; document it,
  don't fix it.
- Import discipline: turndigest may import only stdlib +
  `providers/fastmodel` (contracts/dataclient allowed by the criteria but
  unneeded). No `services/...` imports — reviewers will check.

## Out of scope, noted for later

- `MaxTokens` knob on `fastmodel.Job` (explicitly deferred by the task).
- `get_summary`'s transcript-tail fallback
  (`services/router/internal/api/orchestrate.go` handleOrchestrateSummary,
  `orchestrateTranscriptTailLen`) switching to `LeaderCatchup` — the audience
  constant and Turn anchors exist for it.
- `GenerateRunDigest` switching to the `RunDigest` audience (its prompt gets
  task/status context via `Subject`).
- Wire upgrade: `session.turn_ended` carrying structured `entries` JSON
  instead of a rendered string (kills `Parse`); needs a compat window for
  already-running session children.
- `synth/transcript.go` reusing `turndigest.Render`.
