# Event bus performance: conditions, results, decision

Status: decided 2026-11-21. Production build (branch `event-bus-live-fixes`)
measured and reviewed; numbers under "Production build".

This records every condition measured while fixing the "sessions stop syncing /
switching floods the network" problem, so the decision can be checked later.
The harness itself is described in [PERF-HARNESS-PLAN.md](PERF-HARNESS-PLAN.md).

## The problem in one paragraph

The UI keeps one window-wide SSE connection to `GET /events` (the "bus",
`ui/src/router/events.ts`), shared by about 42 subscriptions. On `main` that
bus resends the event log **from seq 0** whenever it (re)connects, for two
reasons:

- **Bug A.** `resumeSince()` takes the *lowest* per-type cursor, and a type the
  window has never received counts as 0. Every subscribed set contains such a
  type (e.g. `agent.deleted`, which most logs never contain), so every connect
  starts from 0.
- **Bug B.** A session switch reconnects even though no new types were added:
  `subscribeToEvents` compared the new subscribers against the *current*
  subscribers, not against what the open connection carries, and React runs
  the old view's cleanups before the new view's effects.

Teardown at zero subscribers (leaving the Sessions tab) also throws the
cursors away. With a ~300k-event log, each reconnect moves 30–330 MB and the
renderer spends up to ~20 s dispatching it, while Chromium's 6-connections-per-
origin limit starves every other request.

## Conditions

### Builds

| Name | Branch / worktree | What it is |
|---|---|---|
| **main** | `main` @ fab34ca1 | Today's bus: per-type cursors, min rule (bug A), bug B, cursors dropped at teardown. |
| **R0** | `spike-base` | main + bug-B fix (`connectedTypes`: widen only past what the open connection carries). Server: `from=head\|head-N`, a `: start <seq>` SSE comment, `GET /events/head`. Bug A left in on purpose. |
| **R1** | `spike-r1` (on R0) | Live-only bus. **One scalar cursor** (highest seq delivered). First connect reads `/events/head` and connects `since=head`; widen, drop and overflow resume from the cursor; cursor kept across teardown; the bus never sends history. REST snapshots for state that only replay provided: pending approvals (new `GET /permission-requests`, one request for all sessions), queue, pipeline-running flag, provisioning steps. A subscribe during a pending backoff waits for it. |
| **R2** | `spike-r2` (on R0) | Minimal patch to today's design. **Per-type cursors**; a type with no cursor defaults to the window start W (head at first connect) instead of 0; cursors kept across teardown; connect resumes from the min. First connect opens immediately (`from=head`) and fetches W in parallel. The same REST snapshots as R1. A subscribe during a backoff reconnects at once (today's behaviour). |
| **R3** | `spike-r3` (on R0) | Bounded replay. Unchanged except that a connect which would replay from 0 uses `from=head-K` instead (K = 2000 events matching the connection's filter). No REST snapshots. |

All spikes are throwaway code. The chosen design was rebuilt for production
(see the end).

### Workloads

| Name | What |
|---|---|
| **S / M / L / XL** | Synthetic logs from `perfseed`: 10k / 100k / ~308k / 1M events. L is calibrated (counts and payload sizes only, read-only) from the live database on 2026-11-19. S, M and XL are L scaled. The heaviest session holds ~24% of all `session.subagent_event`, matching the live log. |
| **Realism** | A read-only `.backup` copy of the live `~/.mads/mads.db`, with every path rewritten to a scratch dir, service URLs blanked and secrets stripped from the environment. Deleted at teardown; results hold no names or ids. |

All comparison runs below are **L**, **N=3**, **Chromium**, with base and
candidate interleaved ABBA in one run, unless noted.

### Delay modes

| Mode | Meaning |
|---|---|
| **off** | No artificial latency. Isolates the bus. |
| **on** | The delay proxy holds the slow git routes as they behaved live: `/sessions/*/repos` 10 s, `/changeset` 6 s, `/worktree/refresh` 16 s. Shows socket starvation. |

### Scenarios

| # | Action measured |
|---|---|
| 1 | Cold window load |
| 2 | Switch session A → B and back (per leg) |
| 3 | Open a delegate of the **heaviest** parent (~15k subagent events), transcript file present |
| 3b | Same, no transcript file (the view pages history over REST) |
| 3t | Open a delegate of a **typical** parent (~1k subagent events) |
| 4 | Sessions → Retro → Sessions |
| 5 | Session → launchpad (control: nothing should move) |
| 6 | Switches and opens while long-lived streams are pinned (background process in the dock, Tasks tab on a live pipeline attempt) |
| 7 | Every connection cut, network back at once |
| 7b | Network refused for 20 s, then back (a real sleep/wake) |
| 8 | **Long-lived window**: load, add 50,000 events live while the window stays open (≈ a day of activity), then open the first delegate (typical parent) |

### Metrics

| Metric | Definition |
|---|---|
| **/events bytes** | Bytes the server sent on `/events` during the action, counted at the proxy (exact; repeatable to a few bytes). |
| **settled** | Click → the scenario's data marker is visible (timed inside the page) **and** no short request is in flight or started for 500 ms **and** every `/events` stream has caught up to the log head at the click **and** the main thread answered a lag probe within 100 ms for 500 ms. |
| **main-thread task** | CDP `TaskDuration` delta during the action. |
| **all-6-busy** | Time during which all 6 sockets to the origin were in use (proxy and lsof agree). |
| **checks** | User-visible assertions that must survive the change. **a** pending approval on the rail (after cold load, reload, Retro → Sessions); **b** queued-message list; **c** retro progress (UI-only, not a bus signal); **d** pipeline running badge; **e** provisioning steps. Each step is labelled per build: pass, known-fail on main, **FIXED**, or **REGRESSION**. |
| **live smoke** | After each scenario, a session renamed through the API shows on the rail within 2 s. |

## Results

### Validation: the harness reproduces the live symptoms (main)

| | Live DevTools (2026-11-19) | Harness, seeded L | Harness, realism |
|---|---|---|---|
| Open delegate | 199 MB | 197–200 MB | 206–212 MB |
| Retro → Sessions | 34 MB | 32–35 MB | 35–38 MB |
| Session switch | 52 MB per leg | 49.5–52.9 MB per leg | 54–56 MB per leg |
| Launchpad | none | 0 | 0 |

### R0–R3 vs main (L, delays off, medians)

`main` shows the range of its medians across the four comparison runs.

**/events bytes**

| # | main | R0 | R1 | R2 | R3 |
|---|---|---|---|---|---|
| 1 | 30.9 MB | 30.9 MB | 16 B | 16 B | 1.07 MB |
| 2 | 99.1 MB | 0 | 0 | 0 | 0 |
| 3 / 3b / 3t | 196.6 MB | 196.6 MB | 16 B | 16 B | 2.58 MB |
| 4 | 32.1 MB | 32.1 MB | 32 B | 32 B | 1.46 MB |
| 5 | 0 | 0 | 0 | 0 | 0 |
| 6 | 327.8 MB | 278.3 MB | 64 B | 64 B | 5.36 MB |
| 7 / 7b | 49.5 MB | 49.5 MB | 16 B | 16 B | 1.33 MB |

**settled (ms)**

| # | main | R0 | R1 | R2 | R3 |
|---|---|---|---|---|---|
| 1 | 1823–2283 | 2822 | 150 | 150 | 251 |
| 2 | 1894–2210 | 284 | 322 | 340 | 283 |
| 3 | 20544–25295 | 22784 | 236 | 213 | 267 |
| 3b | 14625–25147 | 17778 | 1803* | 543 | 526 |
| 3t | 2445–7620 | 3423 | 165 | 77 | 97 |
| 4 | 754–1185 | 783 | 140 | 120 | 151 |
| 5 | 90–123 | 81 | 113 | 95 | 103 |
| 6 | 5069–10129 | 4533 | 1125 | 802 | 1312 |
| 7 | 1369–1629 | 1112 | 992 | 269 | 328 |
| 7b | 23732–39889 | 29199 | 25794 | 30782 | 32345 |

\* Machine noise, not design. Interleaved in one run, R1 and R2 settle 3b at
523–724 ms vs 552–737 ms with identical REST paging (`20261121-004637`). The
7 gap is R1 honouring a pending backoff while R2 reconnects on the next
subscribe; that policy was decided separately (below). 7b is dominated by the
20 s outage for every build.

**main-thread task (ms)**

| # | main | R0 | R1 | R2 | R3 |
|---|---|---|---|---|---|
| 1 | 1461–1827 | 2252 | 101 | 101 | 165 |
| 2 | 939–970 | 159 | 143 | 142 | 140 |
| 3 | 19358–23814 | 21475 | 147 | 127 | 162 |
| 3t | 1175–2520 | 1770 | 103 | 64 | 89 |
| 4 | 408–631 | 408 | 81 | 70 | 86 |
| 6 | 2392–3810 | 2110 | 501 | 399 | 505 |

**all-6-busy (ms):** scenario 2 drops from 546–694 (main) to 141–179 for every
spike; scenario 6 from 459–697 to 148–369. Other scenarios stay under 30 ms.

### Long-lived window (scenario 8; L, delays off)

| | main | R1 | R2 |
|---|---|---|---|
| /events bytes, opening the first delegate after +50k events | 227.2 MB | 16 B | **30.6 MB** |
| frames | 164,815 | 1 | 21,556 |
| reconnect resumes from | seq 0 | head | window start W |
| settled (ms) | 3351–3527 | 74–82 | 393–413 |
| after +150k events: bytes / settled | — | 16 B / 77–78 ms | **91.7 MB / 1661–1682 ms** |

R2's cost grows in step with how long the window has been open (3× events →
3× bytes, ~4× settle time): opening a new type resumes the whole union from W.
R1's doesn't. The short-lived scenarios couldn't show this.

### Delays on (scenarios 2 and 6)

| | main | R0–R3 |
|---|---|---|
| s2 all-6-busy (ms) | 14640–14689 | 14986–15085 |
| s2 worst queued request (ms) | 8395–8441 | ~10060 |
| s6 all-6-busy (ms) | 8679–8807 | 10330–10559 |

With the slow git routes mocked, every spike starves **slightly more** than
main: without the replay holding the main thread, the slow git requests go out
sooner and small requests queue behind the full 10 s `repos` delay. The main
thread still improves (s2 task 1420 → 580–690 ms). **After the bus fix, the
git routes are the starvation bottleneck.**

### Correctness

| Check | R0 | R1 | R2 | R3 |
|---|---|---|---|---|
| a: approval after Retro → Sessions (fails on main) | still fails | **FIXED** | **FIXED** | still fails |
| a, b, d (other steps) | pass | pass | pass | pass |
| c, e reload part-way (fail on main) | still fail | still fail | still fail | still fail |
| live smoke | pass | pass | pass | pass |

One REGRESSION flag (R0, e "from the launchpad") was a flaky check: the same
step failed on main in another run.

### Realism (copy of the live DB, delays off; settled ms)

| # | main | R1 | R2 |
|---|---|---|---|
| 1 | 2713–2918 | 137 | 147 |
| 2 | 2072–2532 | 268 | 251 |
| 3 | 15870–17203 | 554 | 638 |
| 4 | 719–1255 | 100 | 117 |
| 6 | 6420–7332 | 649 | 617 |
| 7 | 1394–1733 | 170 | 509 |

R1 and R2 send 16–64 B on `/events` in every realism scenario.

## Decision

**R1, adjusted.** R1 and R2 tie on every short-lived scenario, but R2's cost
grows with window age (scenario 8) and keeps today's "history only sometimes"
behaviour for late-mounting views. R3 still replays megabytes and doesn't fix
the approval bug. R0 is only a part of the fix.

Adjustments agreed with Drew (2026-11-21):

- **Order safety is 100%, via a ready barrier.** The bus reads `/events/head`
  before connecting (chosen over a server-sent start event for simplicity),
  and exposes `ready`. Every view that combines a REST snapshot with live
  events subscribes, awaits `ready`, then reads the snapshot; heard/seq guards
  handle the overlap. (R1 as spiked lacked the barrier: a snapshot read
  before the head left a small gap.)
- **Reconnects** keep exponential backoff, and reset it and reconnect
  immediately on the browser's `online` event and when the window becomes
  visible again. Doomed attempts while offline stay calm; wake is fast.
- **Knowingly lost across a reload:** "Done · your turn", "Crashed", and a
  seeded approval's original start time (shows as the load time).

Socket contention after the fix (the git routes, timeouts and cancellation)
is the next piece of work, approached through request ordering and per-route
speedups.

### Harness lessons (why early numbers were wrong)

- Playwright's `context.route()` live-port guard made every request wait on
  the test worker: cold-load "stalls" of 6–7 s were the harness.
- With DevTools' Network domain on, 73k SSE messages backed up the harness's
  own connection: the "11–14 s session switch" was really ~1.4 s.
- An early "settled" ended at the first 500 ms pause and ignored requests
  started after the marker. Both were biased, in opposite directions.
- The delegate scenario first modelled only the heaviest parent (40 s); a
  typical parent (~4 s live) needed its own scenario (3t).

## Production build

Branch `event-bus-live-fixes` @ 2dcb4484 (on `event-bus-live`), measured
2026-11-21 against main. Design as decided above, plus decisions made during
review:

- **Services older than the UI** (`/events/head` 404/405): the bus stops for
  good and a "Restart mads" banner says so. No legacy fallback (Drew: fail
  loudly; the UI is served live from `ui/dist` and `scripts/app.sh` reuses
  running services, so this skew is real after an upgrade).
- **Data Service down:** views keep waiting (order safety), the banner shows
  after 1 s of failed head reads, job buttons give up after 10 s, and the
  Router logs Data Service outages (dial errors only, rate-limited, one line
  on recovery).
- **Backoff cap 2 s** = 2 × the measured worst realistic restart (~1 s: a full
  restart right after a rebuild, cold binaries, first head read on a
  ~300k-event log; warm restarts 50–250 ms).
- **Removed** as unreachable once `/events/head` is required: the
  rejected-type probe and the per-link fallback for servers without bulk
  links.
- Every mount read in the UI (not only snapshot merges) waits for
  `eventsReady()`; a table test covers 33 readers.

### Results (L, N=3, Chromium, delays off; main → production)

| # | /events bytes | settled (ms) | main-thread task (ms) |
|---|---|---|---|
| 1 cold load | 30.9 MB → 0 | 1921 → 158 | 1555 → 107 |
| 2 switch | 99.1 MB → 0 | 1963 → 344 | 971 → 138 |
| 3 delegate, heaviest | 196.6 MB → 0 | 21691 → 218 | 20062 → 126 |
| 3b no transcript | 196.6 MB → 0 | 14942 → 546 | 13639 → 282 |
| 3t typical | 196.6 MB → 0 | 2434 → 74 | 1149 → 64 |
| 4 Retro round trip | 32.1 MB → 0 | 753 → 121 | 408 → 68 |
| 5 launchpad | 0 → 0 | 93 → 90 | 43 → 39 |
| 6 pinned streams | 327.8 MB → 0 | 5434 → 817 | 2421 → 387 |
| 7 drop/resume | 49.5 MB → 0 | 1255 → 645 | 419 → 25 |
| 7b 20 s offline | 49.5 MB → 0 | recovers 2.3–6.0 s → **0.28–1.40 s** after the network returns | 1147 → 488 |
| 8 long-lived window | 227.2 MB → 0 | 3465 → 76 | 1748 → 76 |

Realism (copy of the live DB): cold load 2925 → 144 ms, switch 2056 → 276 ms,
delegate 15622 → 551 ms, pinned streams 6703 → 631 ms, 7b recovery 3.4–9.6 s →
0.10–1.30 s, 0 B on `/events` throughout. Electron agrees (cold load 2635 →
106 ms, typical delegate 3782 → 68 ms).

Correctness: check a "after Retro → Sessions" **FIXED**; no REGRESSION in any
run; check d shows the badge count on both builds; c and e "reload part-way"
remain known-fail on both (e is a product issue: the strip mounts only after
provisioning finishes). Live smoke passed every run.

Delays on (git routes mocked slow): settle times equal main's (s2 14779 →
15085 ms, s6 21543 → 21548 ms) but total queued time is 1.66–2.06× main's and
6–8 more short requests go out per action (REST seeds, `/permission-requests`,
mount reads now released together after `ready`). The slow git routes are the
remaining bottleneck; that is the next piece of work.

Runs: `perf-results/20261121-122846` (off), `-125519` (on), `-130554`
(Electron), `-131522` (realism).

## Results directories

All under `perf-results/` on the machine that ran them (gitignored):

| Run | Dir |
|---|---|
| main baseline, L | 20261120-195520 |
| R0 / R1 / R2 / R3 vs main, delays off | 20261120-205628 / -212258 / -214851 / -221258 |
| R0 / R1 / R2 / R3 vs main, delays on (2, 6) | 20261120-223810 / -224927 / -225923 / -230946 |
| realism R2 / R1 | 20261120-232041 / -233550 |
| scenario 8: main vs R1 / main vs R2 / R2 vs R1 / 150k | 20261121-001711 / -002518 / -003318 / -004050 |
| 3b interleaved R2 vs R1 | 20261121-004637 |
