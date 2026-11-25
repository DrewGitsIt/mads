# Chat stream: per-batch cost grows with conversation length

Status: found 2026-11-24 by the mads-mobile spike; causes 1–5 confirmed against the code the
same day. **Causes 1–5 fixed on `main` (fix A + fix B, see "What was done"); direction 5,
rendering a window of turns, is not done.** The measurements below are the spike's, taken
before the fix. Visual summary: [chat-stream-overview.html](chat-stream-overview.html).

## Summary

Every streamed batch makes the conversation view redo work for **every message in the
conversation**, not just the reply that's growing. The cost per batch is roughly
proportional to the number of loaded rows.

- **On a phone** (OnePlus 9, Android 14 WebView), a long conversation streams at about
  7 updates a second: frames take 132 ms instead of 8 ms.
- **On the Mac** it's hidden by CPU speed: each batch costs about 29 ms and the main
  thread is 58% busy while streaming.
- **Idle is fine.** Between turns, frames are back to 8.3 ms at any size, and native
  scrolling stays smooth throughout.

## How it was measured

- **The mads-mobile spike:** mads's real `ChatRenderer` and `useKeyedConversation`,
  imported unchanged into a WebView.
- **Data:** a fake host speaking the real wire format: 500 turns, live replies of 15k
  characters at 60 text deltas per second.
- **Tools:** a Chrome trace plus a JS CPU profile over the DevTools protocol, with JS
  mapped back to source through the sourcemap.
- **Artifacts:** `~/repo/mads-mobile/spike/profiles/` (traces, CPU profiles, analyzers
  and the CDP driver in `tools/`).

### Main-thread time per React commit, on the phone (plain production build)

| Loaded | Rows / DOM nodes | Frame p50 | Main thread busy | ms per commit | JS | Layout | Paint | GC |
|---|---|---|---|---|---|---|---|---|
| First page | 73 / 2.6k | 8.3 ms | 62% | 11 | 6.6 | 0.7 | 1.9 | 0.2 |
| 10 pages | 730 / 12.7k | 24.8 ms | 100% | 24 | 16.8 | 1.9 | 3.6 | 0.6 |
| All 500 turns | 3,677 / 59k | 132–140 ms | 100% | 148 | 92.7 | 10.4 | 20.2 | 15.2 |

At 500 turns React merges the ~55 batches a second into about 7 commits, each one a
~111 ms long task. Text arrives on screen 110–135 ms after the frame comes in, and
stick-to-bottom gives up, so the view drifts off the bottom while streaming.

**Desktop** (headless Chrome on the Mac, same bundle, same 3.6k rows): about 29 ms
per batch, main thread 58% busy, no long tasks, frame p50 16.7 ms. The cost breaks down
the same way: React 34%, browser 21%, `ChatRenderer` body 13%, GC 8%, reducer 5%.

## Causes, by share of busy time (phone, all 500 turns)

| # | Cause | Share | Where |
|---|---|---|---|
| 1 | **Every message re-renders on every commit.** Nothing in `ChatRenderer` or `activity/` is memoized, so React re-runs and diffs every message, tool group and Markdown component. Much of the GC comes from this as well. | ~37% (React 29%, component bodies 7.5%) | `ChatRenderer.tsx` item map (~L560–800); `activity/ActivityGroup.tsx`; `markdown.tsx` (each block's parse is cached, but the component still re-runs) |
| 2 | **Every timestamp is re-formatted on every render.** About 1,700 `<time>` elements each run `parseMessageTime`, `toISOString()`, `toLocaleString()` and `formatMessageTime`. That last one builds `new Date()` and calls `toDateString()` twice. The stamp is only shown on hover. | ~22% (`ChatRenderer` body); stubbing it moved frame p50 from 132 to 107 ms | `ChatRenderer.tsx:727`, `:742–746`; `messageTime.ts:19–24` |
| 3 | **The reducer copies the whole row map on every wire frame.** `upsertLive` spreads all ~3.7k keys for each text delta, about 1.3 ms per frame. | ~8% | `keyed/conversation.ts:338` (and the other `{ ...s.rows }` copies at :170, :205, :285, :320, :366, :499, :511, :532) |
| 4 | **Layout is forced on every commit.** A `useLayoutEffect` with no dependency list reads `offsetHeight` each commit. Only 14 objects are dirty, but layout walks about 109k layout objects. | ~5% (~7 ms) | `ChatRenderer.tsx:535–542` |
| 5 | **Fresh objects defeat memoization.** Almost every prop an item gets is rebuilt on every render, so a `React.memo` added for #1 would never hit until they're stable. Inside `ChatRenderer`: `joinRows` copies each bubble; `collapseFailedSends`, `rowsToEntries` and `buildItems` build new item and entry arrays; `anchorPermissions` builds a new `anchored` Map that every `ActivityGroup` receives; `onOpenTask` is an inline closure. One level up, `ConversationTab` rebuilds `openTool`, `resolvePermission`, `tasksByToolUse(…)` and `livePermissionRequests` on every render, and passes inline `attachmentUrl`, `onOpenTask` and `onRetry` closures. | (enabler) | `keyed/joinRule.ts:30`; `ChatRenderer.tsx:423–439`, `:597`, `:624`; `ConversationTab.tsx:1467`, `:1473`, `:1497`, `:1525`, `:1858–1885` |

**Ruled out:**
- `orderedRows` (`conversation.ts:117`): 0.1%.
- `buildItems`, `rowsToEntries`, `anchorPermissions` and the rendered loop: under 1% together.
- marked and DOMPurify: 0.3%.
- Style recalculation: 0.6%.
- The `.stream` flex gap.

## Experiments in the spike (mads source unchanged)

- **E1, `content-visibility: auto` on stream items:** paint fell from 20 to 6 ms, but
  frames did not improve. The cost is in JS.
- **E2, render only the last 200 rows (all rows still in state):** frame p50 went back
  to 8.3 ms and commits to 7.2 ms. Rendering less fixes the frames; only the reducer
  copy (#3) still grows with total size.
- **E3, stub out date formatting:** frame p50 went from 132 to 107 ms.

## Fix directions

1. **Memoize per message.**
   - Keep identities stable through the whole chain (#5): the `ConversationTab`
     props, then `joinRows`, `collapseFailedSends`, `rowsToEntries`/`buildItems`
     and the permission and status maps (narrowed to what each item uses). Cache
     by key and reuse the object while its row hasn't changed.
   - Wrap each item in `React.memo`.
   - Pass stable callbacks that take ids instead of inline closures.
   - Then a batch re-renders only the live reply. Biggest win, on both platforms.
2. **Timestamps once per message.**
   - Compute inside the memoized item, or cache by the timestamp string.
   - Build one `now` per render, not one per stamp.
   - Fill `title`/`toLocaleString` lazily on hover, since the stamp is hover-only.
3. **Split live rows from stored rows in the reducer.**
   - Stored history is immutable and rarely changes. Live rows are a small map.
   - A delta then copies a few keys, not 3.7k.
4. **Stop forcing layout on every commit** (`ChatRenderer.tsx:535–542`). The effect
   compares this commit's height with the previous commit's, to size the glide when
   a digest folds into a quiet-check pill. Measuring *only* when the quiet-check
   count changes would break it: the "previous" height would be many commits old,
   from before streaming grew the stream, so a fold would read as growth and never
   glide. Instead, track the height with a `ResizeObserver` (no forced layout) and
   compare only when the quiet-check count goes up.
5. **Render a window of turns** (e.g. the last ~100 drawn, more on scroll).
   - **Phone:** needed regardless. 500 loaded turns hold about 650 MB in the WebView
     renderer, and Android killed other apps at that point.
   - **Desktop:** optional once 1–4 land.

## What was done

Measured with `make ui-perf SCENARIOS=chat` (`tools/perf/docs/PLAN.md`, "Chat
stream"), Chromium, N=3, a 500-turn synthetic conversation (4,507 rows) calibrated
against a real transcript.

- **Fix A** (directions 2–4): `messageStamp` caches each timestamp's ISO string, title
  and label (re-labelled at local midnight and on a time-zone change); the quiet-check
  glide reads a border-box `ResizeObserver` height instead of `offsetHeight` every
  commit; the reducer keeps rows written since the last bulk change in a small `hot`
  layer over `base` (`rowOf`, `allRows`, `findTool`), so a delta copies a few keys.
  Direction 3's live/stored split was not used: which rows count as live is murky, and
  the two layers read the same rows by construction.
- **Fix B** (direction 1, causes #1 and #5): bubbles, entries, items and per-call
  permission lists keep their identity while their rows are unchanged; each item type
  renders through a memoized component handed only what it uses (`ActivityGroup`
  compares the shared status, background and permission maps only on its own calls);
  `ConversationTab` passes stable callbacks. A delta re-renders only the live reply.

| all 4,507 rows | main | A | A+B |
|---|---|---|---|
| ms per delta at 15/s (unsaturated) | 48.7 | 41.9 | 22.5 |
| JS per delta | 31.0 | 24.1 | 7.2 |
| frame p50 / p95 at 60/s | 48 / 52 ms | 34 / 51 ms | 16.7 / 18.3 ms |
| sent → on screen p95 at 60/s | 79 ms | 68 ms | 34 ms |

Direction 4 measured as neutral: layout per delta didn't fall, because the browser lays
out every frame anyway. What remains is layout and paint of the whole stream, about
12 ms per delta at 4,507 rows, which only direction 5 (or `content-visibility`) reduces.

## Verifying a fix

- **Phone:** the spike's CDP tools re-run the same three sizes and give the same
  breakdown (`~/repo/mads-mobile/spike/profiles/tools/`).
- **Desktop:** `make ui-perf BASE=main CAND=<worktree> PROFILE=S N=3 SCENARIOS=chat
  CHECKS=none` (scenarios 10a/10b/10c, paced 10ap/10bp/10cp, and 10cs, the unsaturated
  all-rows run). See `tools/perf/README.md`.
