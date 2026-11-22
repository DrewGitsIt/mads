# Perf harness plan: event bus replay and connection starvation

Status: draft for review (2026-11-19). Branch `perf-harness` off `main` (fab34ca1).

## Why

Switching sessions or tabs floods the Router with requests, and small requests
stall for seconds. DevTools captures from the live app (2026-11-19):

| Action | `/events` stream | Note |
|---|---|---|
| Open a delegate | 199 MB over 23.9 s | adds `session.subagent_event` to the window bus |
| Retro → Sessions | 34 MB after 12.7 s, still streaming | Sessions surface remounts |
| Session switch | 52 MB | |
| Session → launchpad | none | control: nothing widens or tears down |

`GET /config` answers in 20 ms against an idle Router. During a switch it took
818 ms stalled (waiting for one of Chromium's 6 sockets) plus 871 ms waiting on
a busy server.

Suspected causes, all in `ui/src/router/events.ts` on `main`:
- `resumeSince()` (:162) is the minimum per-type cursor, and a type never
  delivered counts as 0. So a fresh window, or a widen to a new type,
  connects without `since`, and the Data Service replays the log from seq 0
  for every type in the union.
- The cursors are dropped when the last subscriber leaves (:292-298), and
  `App.tsx:227` mounts only the active surface. Leaving the Sessions tab
  therefore resets them.

These are hypotheses until the harness reproduces them. The harness's job is to
measure the baseline and the candidate designs the same way, so we pick a
design on evidence.

## Scope

In scope: the shared event bus (replay volume, reconnects) and connection
starvation as it relates to the bus and long-lived streams.

Out of scope for now:
- why `repos`, `changeset` and `worktree/refresh` are slow — for the bus
  work we mocked their latency. Since 2026-11-21 the harness also measures
  them with real git ("Git routes" below).
- a CI perf gate. Runs are local only (`make ui-perf`).
- mesh and peer buses. `main` has no peers.

## Safety rules (non-negotiable)

- Every run boots its own stack through `ui/e2e/lib/topology.ts`: temp
  `MADS_HOME`, ephemeral ports, stub agent binaries (`MADS_AGENT_BIN_DIR`).
  Never use 17373/17374/17380, the live `~/.mads/local.token`, or `device.key`.
- Build UIs with `VITE_ROUTER_URL=""`. Never run anything in
  `~/repo/mads-mesh` or the main checkout.
- Real data is read only. Calibration reads `~/.mads/mads.db` with
  `sqlite3 -readonly`. The realism check works on a copy made with
  `.backup` into the run's temp home, deleted at teardown, and never committed.
- Every process the harness starts, it stops. No orphans.

## Components

### 1. Seeder (`services/data/cmd/perfseed`, Go)

It writes a workload into a fresh database before the Data Service starts.
It uses the store's own `Log.WithTx` / `Tx.Append` and record stores, so rows
and events stay consistent. Batches are written in large transactions, so 1M
events takes seconds, not hours.

A workload profile is a JSON file:
- event count per type, payload size distribution per type
- number of sessions and active sessions, delegates per session, links
- number of watched repos (for later git work)
- pending permission requests, queued messages, a provisioning session (the
  correctness fixtures below)

| Profile | Events | Purpose |
|---|---|---|
| S | 10k | fast iteration |
| M | 100k | mid |
| L | 300k | today: calibrated to the live DB's per-type counts and sizes |
| XL | 1M | growth |

Calibration: `perfseed calibrate` reads the live DB read-only, emits per-type
counts, mean and p95 payload size, and writes the L profile. No payload
content is copied. Today's shape is 307k events and 260 MB; 125k of them,
about 37 MB, are in the window's subscribed types excluding subagent events.

### 2. Delay proxy (`ui/e2e/perf/delayProxy.ts`)

A small Node reverse proxy sits between the browser and the Router.
- The browser's origin is the proxy, so Chromium's per-origin limit still
  applies.
- Path rules add server-side latency after the request is accepted, so the
  browser socket is really held. Defaults mimic the live measurements:
  `/sessions/*/repos` 10 s, `/changeset` 6 s, `/worktree/refresh` 16 s.
- SSE is streamed through unbuffered.
- No product code changes. Not usable for an HTTP/2 spike; that's out of
  scope.

### 2b. Harness contract: where a stream starts

Settle waits until every open `/events` stream has forwarded the newest
event it will actually send: the highest seq matching its `types`, after
where it starts, up to the log head read (from the database) at the click.
Without help the harness takes "where it starts" from the request's
`since` (missing = 0), which is right for main, where the client asks for
history. A design whose **server** decides where a stream starts (a
live-only bus that starts at the head, a bounded replay) must say so, or
the harness waits for frames that never come:

- the stream's first lines MAY include an SSE comment `: start <seq>`,
  meaning "this stream sends events with seq > <seq>";
- the proxy reads it; the target becomes the newest matching seq in
  (<seq>, head], so a stream starting at or past the head is caught up at
  once;
- without it, the `since` rule stands. The report counts streams that
  declared a start (`events_declared_start`).

A stream that still owes frames and moves no byte for 30 s fails the
repetition with the seq it reached and the one it owes.

### 3. Collectors (Playwright plus the Chrome DevTools Protocol)

| Metric | Source | Kind |
|---|---|---|
| Bytes received on `/events` per scenario | CDP `Network.dataReceived` | deterministic |
| SSE messages received on `/events` | CDP `Network.eventSourceMessageReceived` | deterministic |
| Request count by path | CDP `Network.requestWillBeSent` | deterministic |
| Peak concurrent connections to the origin | CDP `connectionId` of in-flight requests, cross-checked with `lsof` on the test browser's pid and port | deterministic |
| Stalled/queued time per request (p50/p95/max) | CDP `ResourceTiming` (`sendStart` vs `requestTime`) | timing |
| Click → settled | a named DOM marker per scenario, plus 500 ms with no new short requests | timing |
| Main-thread long-task total | `PerformanceObserver("longtask")` via `addInitScript` | timing |
| Router/Data CPU time and peak RSS | `ps` sampling of the stack's own pids | timing |

"Deterministic" means the same build and workload should give the same number
on every repetition. The runner flags any run where they don't.

### 4. Scenarios (`ui/e2e/perf/*.perf.ts`)

1. Cold window load
2. Switch session A → B (and back)
3. Open a delegate from its parent
4. Retro → Sessions round trip
5. Session → launchpad (control)
6. Steps 2–4 with long-lived streams pinned: a running background process in
   the dock and a Tasks tab with a live attempt
7. Network drop and resume (proxy cuts all connections, as on sleep/wake)

Each runs N times (default 5) per build and profile.

### 5. Correctness checks

A faster design must not win by dropping state. The 2026-11-19 subscriber
audit found five things that today arrive only through replay. Each becomes an
assertion that runs after scenarios 1, 4 and 7:
- a pending permission request shows "awaiting approval" on the rail after a reload
- the queued-message list matches the server
- an in-progress retro shows its progress
- `SessionWorkspace`'s pipeline `running` flag is right
- provisioning steps render for a session still provisioning

The audit also lists 37 more subscribers judged safe. A cheap smoke for those
is that the rail, task counts and links update within 2 s of a seeded live
event after each scenario.

### 6. Runner and report (`make ui-perf`)

`make ui-perf BASE=<ref> CAND=<ref-or-worktree> PROFILE=L N=5 BROWSER=chromium|electron`

- `SCENARIOS` takes ids (`1,3b`) and aliases (`tools/perf/scenarioSets.sh`),
  resolved before anything else decides on them:
  - empty or `all`: every scenario, 9x included, so `GITFX=auto` builds the
    real-git fixture (empty under `REALISM=1` means `bus`: the two don't mix);
  - `bus`: 1–8, 3b, 3t, 7b — the event-bus regression set; no fixture build,
    the right choice for bus work;
  - `git`: 9a–9h (delays off by default);
  - aliases mix with ids (`bus,9a`); `none` runs checks only.
- Builds each ref's UI and binaries into separate temp dirs.
- Runs baseline and candidate alternately (ABAB…) on the same seeded
  workload, so machine noise hits both.
- Writes `tools/perf/results/<timestamp>/` (gitignored): raw JSON and a markdown
  table per scenario. Deterministic metrics are shown as exact values;
  timings as median and IQR, with the candidate/baseline ratio.

## Git routes with real git (scenarios 9x, checks f–i)

Added 2026-11-21, after the bus fix showed the git routes are the remaining
socket bottleneck. Opening or switching a session fires `GET
/sessions/{id}/repos`, `GET …/changeset?vs=start` (twice: ContextRail and
ChangesTab), and `POST …/worktree/refresh` (on every switch and window
focus); none is ever aborted. Planned fixes: (A) request hygiene — abort on
switch/unmount, one shared changeset store, no refresh per focus; (B) the
Router computes in the background, pushes over the bus, GET answers from
cache with an "updating" marker. The harness measures main, then each.

### Fixture (`perfseed gitfx`, shape in `tools/perf/profiles/gitfx.shape.json`)

`GITFX=auto` (on for any 9x scenario in the resolved list — so also the
default `all` and `git` — or check f/g/h/i) builds it once per run
inside the run's temp tree, `$TMP/gitfx/live`:
- `origin.git`: `git clone --mirror --no-local $GITFX_SRC` (default
  `~/repo/mads`) — **the only git command that touches the source** — cut
  loose (`remote remove origin`), its `main` moved back to the source's own
  `origin/main` (so local main is ahead of it as the source's is), every
  ref but heads and tags dropped. Every clone's origin is this bare repo,
  so `GET /push`'s `git fetch` never leaves the temp tree.
- `repos/mads` (main) + 10 linked worktrees `repos/mads-wtNN` on the
  source's farthest branches (100–141 commits past origin/main), 3 of them
  tracking their origin branch, the rest none (to-push counts against
  origin's default branch); `repos/mads-iso` on `perf-far` (the farthest
  branch, tracking origin/main) as the isolated sessions' primary;
  `repos/typical`.
- Session worktrees `home/session-worktrees/<id>` on `mads/session/<id>`
  from `perf-far`, as the runner's isolate makes them. A fixture stack's
  `$HOME`/`$MADS_HOME` is `<root>/home` (fresh each repetition), because the
  runner expects a session's worktree at `$MADS_HOME/session-worktrees/<id>`.
- Shadow baselines made by `services/session/shadow` itself (`Baseline`)
  under `<root>/xdg/mads/shadow/<id>`; fixture stacks get
  `XDG_DATA_HOME=<root>/xdg`, so a session child started for a fixture
  session finds its baseline (as a resumed session does).
- Then the session's work after the baseline (edits committed, edits left,
  added, deleted, untracked files), and the sessions, ledgers and recorded
  commits written through the store. All fixture git runs with
  `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1` (no hooks, no
  signing) and its cwd checked to be inside the root.

Calibration (counts only, read-only, 2026-11-21): the live session that
took 10 s on `/repos` and 6 s on `/changeset` (current_dir, folder `/`) has
2103 ledger rows over 908 files in 309 dirs (249 still exist), resolving to
11 checkouts of one repo; 63 uncommitted (2 + 61 in two checkouts); 0
recorded commits. Heavy live sessions record 120–250 commits; the median
ledger is 13 files. No live session is isolated.

| Session | Shape |
|---|---|
| `multi` "perf git: multi-repo heavy" | folder `repos/` (not a repo); 2103 rows / ~902 files / 309 dirs (60 gone) over 11 checkouts; 380 files edited and committed in 30 unrecorded commits; 63 uncommitted (2 + 61) — the live session's shape |
| `isolated` "perf git: isolated heavy" | worktree; 920 rows / 410 files / 120 dirs; 250 files edited in 40 recorded commits; 40 uncommitted, 10 added, 5 deleted; 12 untracked before the baseline, 13 after |
| `land` "perf git: isolated to land" | as `isolated`, all committed, nothing untracked (Land is offered) |
| `typical` | in place, 30 rows / 14 files / 5 dirs, 3 recorded commits, 4 uncommitted |

Every repetition restores the tree from a pristine APFS clone (`cp -cRp`)
and refreshes every checkout's and every shadow index (a copy's new inodes
and ctimes would otherwise make the first status — and every changeset,
which copies the shadow index — rehash whole trees). ~11 s, outside the
measured window.

### Scenarios (delays off only: git is real)

| # | Action |
|---|---|
| 9a | switch A (isolated heavy) → B (multi-repo heavy) → A, quiet between legs |
| 9b | A → B → A → B, 1 s apart, one leg (requests left behind) |
| 9c | open A's Changes tab |
| 9d | window focus on A (blur, visibilitychange, focus), twice |
| 9e | `land` session: open the Push dialog, then the Land dialog; never confirmed |
| 9f | A's child (fake provider) writes a file every 2 s for 20 s (`PERF_BUSY_S`), Conversation tab showing |
| 9g | outside edit then focus: A's Changes tab open, 31 s idle (past the focus skip), a new untracked file written straight into A's worktree (no agent, no `file.changed`), then focus; marker = the totals show one more file. If the focus hasn't shown it within 10 s, the leg reopens A (→ typical → A) as a user would — main and A recompute a change set only on open; `outside_shown_on_focus` says which |
| 9h | idle focus: A's Changes tab open, nothing changed, focus twice, each 31 s after the last (past the skip); each leg's git calls, change-set roots diffed and Router CPU count for 10 s from the focus (`gitTailMs`: B's refresh is a 202 with the work behind it). Where a probe gate should save work |

Settled for 9x waits for the git routes too (they are short requests now).

### Metrics

| Metric | Source |
|---|---|
| change-set roots diffed (whole window, per leg) | the shim marks a call made with `GIT_INDEX_FILE` set (`idx`); `add -A` into a temp shadow index is one root of one change-set compute (`pkg/changes/gitops.go`), so 0 = no change-set recompute |
| Router CPU per leg | `ps` samples at the leg's ends (9h: to the end of its 10 s tail) |
| per-route latency, p50/max, for repos, changeset, worktree/refresh, worktree, commit-plan, push, land | server: the proxy's arrival → response headers; client: the page probe's fetch → headers (includes queueing) |
| abandoned requests | a non-stream request naming session X still in flight at the proxy when the view left X (scenarios record each switch, `ctx.shown`): count, git-route count, aborted count, socket time held after leaving (sum, max) |
| git calls and CPU, whole window and per leg | the **git shim** (`tools/perf/gitshim`, first on every fixture stack's PATH; `PERF_REAL_GIT` is the real one): one JSON line per call with start/end, parent pid, subcommand, and the call's rusage (user + system). Top subcommands by CPU per build |
| peak concurrent git processes | `ps` every 100 ms, git processes in the Router's tree by pid ancestry (also reports how many it caught: sampling misses most calls) |
| all-6-busy, queued, settled, main thread | unchanged |

### Checks

- **f-git-converge** (must pass on main): after a change, the Changes tab's
  file count, the chip's "N uncommitted" / "N to push" and the Land step's
  "N commits on …" match a fresh server compute (GET changeset, GET
  worktree, GET repos — taken after the UI's last read, so the oracle's
  side effects can't help) within **N = 15 s** (`PERF_CONVERGE_S`). Steps:
  a fake-provider turn writes two files in `isolated`; a commit outside
  mads there (to land +1); a commit outside mads in `typical` (to push +1).
  15 s = the repo watcher's worst case (1 s quiet, 5 s max) + the UI's 1.5 s
  refetch debounce + two changeset computes (4–5 s each on main; one may be
  in flight when the change lands). Main measured 3–9 s.
- **g-git-updating** (known-fail on main; must pass once B lands). The
  contract assumed for B: an element `[data-testid="git-updating"]`,
  visible, somewhere on the open session's workspace, present while the
  Router is recomputing any git-derived value the session shows (its change
  set, repo facts, header counts), removed once the recomputed values are
  on screen. It must appear within 2 s (`PERF_UPDATING_WITHIN_MS`) of a
  change the server hears of (an agent's file write) and clear within N s
  with the file count matching a fresh compute. An optional
  `data-what="changeset repos counts"` is not checked. Main's "Updating…"
  (Changes tab, first answer only) doesn't count.
- **h-git-dialogs** (must pass on main): opened right after a change, the
  Commit dialog's file list equals a fresh `GET /commit-plan`; the Land
  dialog's "N commits to squash" a fresh `GET /land`; the Push dialog's
  "N commits:" a fresh `GET /push`. A cached design must not show stale
  counts here.

- **i-git-outside-edits** (must pass on main, A and B; guards the planned
  focus/open "probe gate" that skips a change-set recompute when a cheap
  fingerprint says nothing changed). Edits made straight in `isolated`'s
  worktree (no agent: no `file.changed`, no ledger row) must show in the
  Changes tab within **N = 10 s** (`PERF_OUTSIDE_S`) of a focus, or else of
  a reopen (→ typical → A, timed from the click back on A), with totals (files, +added, −deleted) equal to a
  fresh compute. Steps: a new untracked file; a re-edit of an
  already-dirty tracked file (content and size change); a **same-size**
  re-edit of another (one letter of its first line bumped: new content and
  mtime, same size); a deleted tracked file (one the change set doesn't
  list yet). The re-edits are the probe's
  blind spot: `git status` output doesn't move when an already-dirty file
  changes again, so a status hash alone would skip them (a correct probe
  adds the size and mtime of the dirty paths; the same-size step catches
  one that keys on size). A last step, not an assertion, records whether a
  focus with nothing changed recomputed the change set (change-set roots
  diffed, git calls and CPU in the 10 s after it, from the shim log).
  - Focus skip: the UI skips a focus within 30 s of its last recount
    (Phase A), and B's bodiless refresh and first change-set read recompute
    only a change set older than 30 s, so each step waits until **31 s**
    (`PERF_FOCUS_SKIP_MS`) after anything last refreshed A (the UI's
    focus/reopen, or the oracle), then edits, then focuses; the reopen is the
    fallback, not a substitute. Main and A recompute a change set only on
    open (main's focus recounts the header counts only; `repo.changed`
    ignores the index), so there the steps pass on the reopen; B passes on
    the focus. Each step's detail says which.
  - Oracle: main and A compute on every `GET /changeset`; B answers from its
    cache (the answer carries `updating`), so there the oracle forces a
    recompute (`POST /worktree/refresh {parts:["changeset"], max_age_ms:0}`)
    and waits for an answer computed after the ask. Taken after the UI's
    last read. It must also differ from the previous step's (a gated or
    stale oracle would otherwise agree with a stale UI); the harness's own
    prediction of the totals (+1 file / +3 lines, +2, +1 −1, +1 file −L) is
    only for stopping the poll early.
  - N = 10 s: B's path is a 202, the heavy session's change set recomputed
    (~1 s on b2), the batch announced, a cached GET; main's is the reopen's
    GET computing it (3.6–6.7 s measured under load) behind the mount's
    events barrier. No repo-watcher term (f's 15 s has one): the trigger is
    explicit.
  - Edits only touch paths inside the run's git fixture (`outsideEdits.ts`
    `inFixture`: realpath under the fixture root, never under `~/repo`);
    dirty files are listed with `git --no-optional-locks status`.

### Check i, 9g, 9h: origin/main vs b2 (2026-11-22, `tools/perf/results/20261122-002834`)

`make ui-perf BASE=origin/main CAND=../mads-git-b2 PROFILE=S N=1
SCENARIOS=9g,9h CHECKS=i`. Check i passes on both builds. Main shows every
edit only on the reopen (4.6–4.7 s after the click back on A; the focus showed
nothing in 10 s). b2 shows each one on the focus (2.0 s). The idle focus
records no change-set recompute on main (129 git calls, 1.0 s git CPU in
10 s, the header recount) and a recompute on b2 (1 root diffed, 142 calls,
1.3 s).

| | main | b2 |
|---|---|---|
| 9g settled (edit + focus; main: + reopen) | 14.7 s | 1.9 s |
| 9g git calls / git CPU / roots diffed / Router CPU | 828 / 8.6 s / 3 / 0.97 s | 142 / 1.3 s / 1 / 0.19 s |
| 9h settled per focus | 1.6 s | 4 ms (202) |
| 9h per focus: git calls / git CPU / roots diffed / Router CPU | 129 / 1.0–1.1 s / 0 / 90–100 ms | 142 / 1.3–1.4 s / 1 / 180–190 ms |

On b2, an idle focus costs about one change-set compute that finds nothing
new. That is what the probe gate is meant to save: 9h's roots diffed should
fall to 0, and check i must still pass.

### Validation on main (2026-11-21, `tools/perf/results/20261121-152238`)

`make ui-perf BASE=main PROFILE=S N=3 SCENARIOS=9a,9b,9c,9d,9e,9f
CHECKS=f,g,h`, Chromium. Medians [min–max] of 3. The machine was loaded
(load average up to 37: the live app, Spotlight indexing the fixture
copies — the run's tree is now marked `.metadata_never_index`), which shows
in 9a's spread.

| | 9a switch | 9b rapid | 9c Changes | 9d focus ×2 | 9e Push+Land | 9f busy 20 s |
|---|---|---|---|---|---|---|
| settled, all legs (ms) | 32275 [22892–109615] | 36093 [28338–60689] | 4408 [4032–4538] | 7617 [7008–9833] | 11040 [8759–14422] | 35733 [32188–36873] |
| repos server p50 / max (ms) | 8.3 s / 19.2 s (one rep 25.8 / 60.7 s) | 8.5 s / 24.6 s | – | – | – | 3.1 s / 3.4 s (×9) |
| changeset server p50 / max | 4.5 s / 4.6 s | 5.6 s / 6.7 s (client max 14.8 s) | 4.3 s | – | – | 3.3 s / 3.9 s (×10; client max 16.2 s) |
| worktree/refresh p50 / max | 4.2 s / 13.0 s | 14.9 s / 17.2 s | – | 3.5 s / 4.1 s | – | – |
| push / land (server) | – | – | – | – | 5.6 s / 5.3 s | – |
| git calls / git CPU (s) | 1947 / 41.1 | 2658 / 50.2 | 328 / 5.8 | 470 / 9.2 | 527 / 7.3 | 3583 / 62.9 |
| peak concurrent git (ps) | 2 | 4 | 1 | 0–1 | 0–1 | 1 |
| all-6-busy (ms) | 123 | 16010 | 0 | 0 | 0 | 48 |
| queued max (ms) | 4253 | 15803 | 1 | 1 | 1 | 13614 |
| abandoned (git) / held after leaving, sum / max | 0 | 5 (5) / 54.8 s / 23.5 s | – | – | – | – |

- **Main reproduces the live slowness.** `/repos` 8–25 s (live ~10 s),
  changeset 3.6–6.7 s (live ~6 s), refresh 3.3–17 s (live up to 16 s), all
  six sockets busy for 13–19 s of a rapid switch, 5 git requests abandoned
  per rapid switch holding sockets 19–24 s after the view left.
- The cost is mostly `git rev-parse --show-toplevel`: 1500–2400 calls a
  window at ~15 ms CPU each (`/usr/bin/git` is Xcode's trampoline), one per
  ledger directory per `/repos`, per reconcile (every 30 s, every active
  session), per recount. All serial: peak concurrency 1–4.
- 9f queues 10–14 s with all six sockets *not* busy: the ContextRail's and
  ChangesTab's identical `GET …/changeset` are serialised by Chromium's
  per-URL cache lock (client max 16 s vs server max 4 s).
- 9a/9b settle on main is dominated by the first `/repos` of the
  multi-repo session plus the refetches its `repo.changed`/recounts set off.
- Checks (`20261121-152238`, then `20261121-160542` for f–h after a harness fix: a
  full-width Changes tab covered the rail): f passes (matched 8.9 s,
  5.1–5.8 s, 3.0–3.3 s after the change); g known-fail as expected; h passes
  (Commit 81 files = fresh plan; Land 41 to squash; Push 182 commits).

## Validation: the harness must reproduce the symptoms first

On `main` with profile L, before any spike:
- scenario 3 shows the `/events` replay with roughly 140 MB+ of subagent
  history (live: 199 MB)
- scenario 4 shows a full-union replay (live: 34 MB+)
- scenario 5 shows no replay
- with the delay proxy on, small requests in scenario 2 stall for seconds
  while 6 connections are held

The realism check runs scenarios 1–5 against a copy of the live DB. The
seeded L profile should land within ±20% of its replay bytes.

If the baseline doesn't reproduce these, the harness or the hypothesis is
wrong. We fix that before testing any design.

## Candidate spikes (after validation)

Each spike is a throwaway branch in its own worktree. Only the winner goes
through the full flow: builder, reviewer plus tester, architecture review.

| Spike | Idea |
|---|---|
| R1 | Live-only bus: start at the log head; one cursor resumes after a drop; history comes from REST snapshots. Needs a "tail" start on the stream and seeds for the five correctness items. |
| R2 | Keep per-type cursors, but keep them across teardown and start a fresh window at the head |
| R3 | Bounded replay: no `since` becomes head − K |
| C1 | Request timeouts/cancellation; close hidden Tasks and background-output streams |
| C2 | Bus over one WebSocket (separate 255-socket pool) |

The R spikes are measured against baseline first; C1 and C2 are layered on
the best R. Final numbers for the chosen combination are rerun in Electron.

## Phases

1. Plan review (this doc)
2. Seeder, calibration and profiles, with Go unit tests
3. Proxy, collectors, scenarios 1–5, runner, report
4. Validation against baseline and the live-DB copy; findings to Drew
5. Scenarios 6–7 and the correctness checks
6. Spikes R1–R3, compare, decide with Drew; then C1/C2 on the winner

## Open questions

- Settled markers: some scenarios may need a `data-testid` added to the UI.
  That's a small product diff, listed in the phase 3 review.
- Electron runs need the built `dist-electron`. Confirm `electron.spec.ts`'s
  launch path works with the proxy origin as `ROUTER_BASE`.
