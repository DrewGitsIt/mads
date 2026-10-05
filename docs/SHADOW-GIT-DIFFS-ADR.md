# ADR: Shadow git storage for historically-accurate tool diffs

**Status:** Accepted (open questions resolved 2026-09-09, see Resolutions) ·
**Date:** 2026-09-09 · **Driver:** conversation-pane redesign — diff blocks on tool
rows must stay accurate forever, not just while the turn is live.

## Context

The redesigned conversation pane renders an `edit`-kind tool row with a real diff
(old → new). Today that diff is reconstructed in the UI from the tool *input*
(`Edit.old_string/new_string`, `Write.content`), which has three unfixable holes:

1. **`Write` has no old side.** Once the file is overwritten, the pre-image is gone.
   The provider's on-disk transcript can never reproduce it, so transcript refetch —
   our history path — renders a lie (empty old side) for every Write.
2. **Providers can't be trusted to supply diffs.** Empirical survey (2026-09-09):
   claude-code's wire has no diff content at all (input only); Hermes speaks valid
   ACP v1 but emits zero `diff` content blocks (its edits ran through a terminal
   tool, output pre-rendered as markdown text); goose can emit ACP diff blocks but
   only when the model uses its editor tool. Per project policy we write happy-path
   code for ACP conformance plus defensive code for harnesses still catching up —
   diffs must be something mads can *produce itself*.
3. **Shell-driven mutations are invisible.** A `sed -i`/`printf >>` via Bash carries
   no `old_string` anywhere on any wire.

The obvious fix — store `oldText`/`newText` in the data service event log — is the
Cursor model (full per-step snapshots in SQLite), which is notorious for
multi-gigabyte state growth, and size caps would make exactly the large files lie.
Cline's checkpoint system points at the right shape: a **shadow git repository**
outside the user's repo, committed per step, with diffs computed on demand —
content-addressed, deduplicated, lazy.

## Decision

Adopt a per-session shadow git store with **two complementary capture mechanisms**,
chosen to match what each can do without racing the agent:

1. **Per-tool pre-image blobs (hot path, tiny).** When a *mutating* tool_use event
   arrives (`Write`/`Edit`/`MultiEdit`/`NotebookEdit` — the ledger's existing
   `Mutates` signal), the runner reads the single target file and stores it with
   `git hash-object -w` into the shadow object store, recording
   `(tool_use_id → blob hash)` on the persisted event. This is safe on claude-code
   because tool_use is emitted **before execution** — the same pre-execution window
   the file ledger's stat classification already relies on — and a single-file read
   costs about as much as that stat (~1–5 ms). No tree scan, no index, no race with
   the rest of the repo.
2. **Turn-boundary tree commits (quiescent path).** A baseline commit of the session
   root at session start (background, during provisioning), then one commit at each
   `EventTurnEnd` — the same hook `sweepDeletedFiles` already uses, a moment when the
   provider is guaranteed idle. Commits respect the user repo's ignore rules plus a
   mads exclude list (`.git`, build dirs).

The event log stores **only hashes**, never file text. Diffs are computed on demand:

- **Per-tool diff** (`GET /sessions/{id}/tool-diff?tool_use_id=…`): old side = the
  recorded blob; new side = tool input (Edit/Write) or, absent that, the file as of
  the next capture/turn commit. Serves the conversation pane's history cards.
- **Turn diff / working diff**: `git diff baseline..turnN` scoped to a path. The
  Files tab's working-diff "original" becomes the **baseline commit** — strictly
  better than today's `sessionBaseRev` heuristic (parent of earliest recorded
  commit), which fails for sessions that never commit.
- Bash-driven mutations get turn-level diffs only (no per-tool pre-image exists) —
  an honest, documented limitation; the turn commit still catches them.

### Layout & ownership (multi-root)

Sessions legitimately touch multiple git roots — started in the wrong place, or
spanning repos (IaC + service). The shadow store is therefore per **(session, root)**:

```
$XDG_DATA_HOME/mads/shadow/<session_id>/<root_key>/   # one shadow git dir per root
```

Roots are discovered lazily from ledger paths (`git rev-parse --show-toplevel` on
each new path prefix, cached; a non-git directory counts as its own root). The
session's primary cwd root is baselined at provisioning (backgrounded); any other
root is baselined on first touch. Turn-end commits run only in roots dirtied that
turn.

**Non-repo roots get no baseline and no turn commits.** A directory that isn't inside
a git repo still counts as its own root, but only so captured blobs have a shadow dir
to live in. Baselining it would `add -A` an unbounded tree — and a session can be
rooted at `~` or `/` (imported conversations live in any folder, and a file edited
directly in `~` makes `~` the root) — copying the home directory or filesystem into git
objects, bounded only by the 30-minute timeout and the short exclude list. So for a
non-repo root the store records per-capture pre-image blobs and nothing else:

- **Per-tool diff**: unchanged for the old side (the recorded blob). The new side comes
  from the next capture of the same path, else the file on disk (there is no turn
  commit to read it from), so a later out-of-band edit can leak into an earlier tool's
  "after".
- **Working diff**: "original" is the pre-image captured before the session's first
  tool call on that file (`shadow.FirstCaptureOriginal`), empty for a file the tool
  created.
- **Not shown**: any change not made through a captured tool call — shell commands
  (`sed -i`, `mv`, build output), edits by the user — has no pre-image and no tree
  diff, so it doesn't appear in the diff view. This is the same "Bash gets turn-level
  diffs only" limitation, minus the turn-level part. The file ledger still lists those
  paths; only the before-side is unavailable.

`root_key` is a hash of the root's absolute path, with the path recorded in
the shadow dir's config for reverse lookup.

The **runner** (session service) writes captures and commits — it owns the event
stream and the cwd. The **router** reads for rendering; it already shells out to git
for `working_diff.go`. The shadow base path is recorded on the session in the data
service (`shadow_dir`), so neither side guesses. Tree operations run as
`git --git-dir=<shadow> --work-tree=<root>`; blob captures (`hash-object -w`) are
path-agnostic, so a file outside every known root still gets per-tool pre-images.

### Live vs. history rendering

Nothing changes for the live turn: cards keep rendering input-derived diffs
instantly (and prefer wire-supplied ACP diff blocks when a provider sends them —
happy path). The shadow store is what makes the *same card look the same* after
turn end, refetch, or a year later.

## Performance budget

| Operation | When | Cost |
|---|---|---|
| `hash-object -w` one file | per mutating tool_use (hot path) | ~1–5 ms |
| Tree commit, warm index | per turn end (quiescent) | measured ~10 ms; no-op turns ~0 ms |
| Baseline commit | once per root, backgrounded | measured 6.3 s on goose (below) |
| Diff render | on demand, cached by (tool_use_id) | one `git diff`/`git show` |

**Measured 2026-09-09** against the goose checkout (`~/repo/goose`: 39 GB on disk,
34 GB of it gitignored `target/` — correctly skipped; 2,478 tracked files, ~300 MB
content): full baseline `add -A` **6.27 s** + commit 0.03 s, ~24 MB peak RSS;
no-op turn-end (`add -A` + `status --porcelain`, warm index) **~0 ms**; single-file
touch + commit **~10 ms**; shadow size after `gc` **300 MB** (≈ the tracked content,
one-time; growth after baseline is churn-only). Extrapolation ≈ **20 s per GB of
tracked content** for the baseline — acceptable backgrounded even at monorepo
scale. Verdict: full baseline adopted. If per-repo baseline duplication across many
concurrent sessions ever matters, a `objects/info/alternates` pointer at the user
repo's own object store would dedup clean-file blobs to near zero — noted as a
future optimization, not v1 (it couples shadow integrity to the user repo's gc).

Failure anywhere degrades to today's behavior (input-derived diffs, HEAD-based
working diff) and must never block or delay the agent's turn: captures are
best-effort, commits run async after turn_end.

## Storage & GC

Content-addressed dedup means growth ∝ actual churn, not repo size × steps: an
unchanged file across 50 turns is stored once. Estimate for a typical mads session
(dozens of file edits): single-digit MB. Controls:

- `git gc --auto` after turn commits (packs loose blobs).
- Shadow dirs deleted when their session is deleted; archived sessions keep their
  shadow for **30 days** (decided; config-overridable).
- **No global size cap** (decided). The catastrophic-growth vectors are bounded
  without one: gitignore rules keep build artifacts out (validated — 34 GB of
  `target/` skipped in the benchmark), and a **per-file capture guard** (skip blobs
  over ~10 MB, recording a "too large to snapshot" marker the UI can render
  honestly) closes the giant-generated-file case. Revisit only if real usage shows
  growth the guard misses.

Secrets note: tree commits respect `.gitignore`, which conventionally covers `.env`
etc., and blobs are only captured for files the agent itself is mutating — the
shadow store never widens what's already on the user's disk, and lives under the
same user account. No network exposure; router serves diffs through the existing
session-scoped, ledger-gated endpoints.

## Provider matrix

| Provider | Pre-image capture | Notes |
|---|---|---|
| claude-code | ✓ solid | tool_use emitted pre-execution (verified; same basis as ledger stat) |
| fake (tests) | under-claims | emits post-execution — capture reads the post-image; documented, matches the ledger's existing caveat |
| goose (ACP) | ✓ + wire diffs | prefer its ACP `diff` blocks when present; shadow covers terminal-driven edits |
| hermes (ACP) | shadow-only | emits no diff blocks at all (captured 2026-09-09); shadow is the only source |

## Alternatives rejected

- **Inline old/new text in the event log** — simplest, but Cursor-style bloat, and
  caps make big-file diffs dishonest. Rejected on the storage concern directly.
- **Full per-step tree commits** (pure Cline model) — per-tool tree commits on the
  hot path race tool execution on large repos; our hybrid keeps per-tool granularity
  at single-file cost.
- **Provider diff blocks only** — Hermes proves fidelity can't be assumed; claude-code
  offers none.
- **Refs/stashes inside the user's own repo** — pollutes reflogs and `gc` behavior in
  a repo we don't own; sessions also run in non-git directories.

## Resolutions (reviewed with Drew, 2026-09-09)

1. **Retention: 30 days** post-archive. **No global size cap** — the eviction
   complexity isn't justified because the guard rails above bound catastrophic
   growth; revisit on evidence.
2. **Multi-root: yes.** Full tree tracking per (session, root) with lazy root
   discovery and first-touch baselines — see Layout. Blob-only coverage remains the
   fallback solely for files outside every discoverable root.
3. **Full baseline adopted**, validated empirically against goose before committing
   (6.3 s / 300 MB on a 39 GB checkout; ~20 s per GB of tracked content
   extrapolated) — see Performance.
4. **Checkpoint-restore: pinned as a fast-follow** after the conversation-pane UX
   redesign ships. It deserves its own mockups and design pass; this ADR only
   guarantees the storage substrate makes it cheap.
