# Perf harness: design

Status: in use. Built 2026-11-19..22 to measure the event bus (shipped on
main 2026-11-21, [EVENT-BUS-RESULTS.md](EVENT-BUS-RESULTS.md)), then the
git routes ([docs/GIT-STATE-DESIGN.md](../../../docs/GIT-STATE-DESIGN.md)),
then the chat view's cost per streamed batch (scenarios 10x, below).
How to run it and where every piece lives: [../README.md](../README.md).

## Why

Switching sessions or tabs flooded the Router with requests and small
requests stalled for seconds (DevTools on the live app, 2026-11-19: opening a
delegate moved 199 MB over `/events` in 24 s; `GET /config`, 20 ms idle, took
1.7 s during a switch, most of it waiting for one of Chromium's 6 sockets per
origin). The harness reproduces that on hermetic stacks so a baseline and a
candidate design are measured the same way and a design is picked on
evidence. It reproduced the bus replay first (see EVENT-BUS-RESULTS.md), then
the slow git routes (below).

## Scope

In scope: the shared event bus (replay volume, reconnects), connection
starvation, and the git routes (`repos`, `changeset`, `worktree/refresh`, the
dialogs) with real git, and the chat view's main-thread cost while a reply
streams into a long conversation. Out of scope: a CI perf gate (runs are local only,
`make ui-perf`), mesh and peer buses.

## Safety rules (non-negotiable)

- Every run boots its own stacks through `ui/e2e/lib/topology.ts`: temp
  `HOME`/`MADS_HOME`, ephemeral ports, an empty `MADS_AGENT_BIN_DIR` (no
  real provider CLI can start). Never 17373/17374/17380, the live
  `~/.mads/local.token`, or `device.key`.
- UIs are built with `VITE_ROUTER_URL=""`; a bundle naming a live port is
  refused, and a page that reaches one fails the run.
- The runner refuses to run from, build in or install into the live
  checkouts (`PERF_FORBIDDEN_DIRS`, default `~/repo/mads:~/repo/mads-mesh`).
  The git fixture only ever clones FROM `GITFX_SRC`.
- Real data is read only: calibration and realism open `~/.mads/mads.db`
  with `sqlite3 -readonly`; realism works on a `.backup` copy in the run's
  temp dir, deleted at teardown, never committed or named in a report.
- Every process a repetition starts goes into a pid registry; the runner
  kills whatever is left when Playwright exits, however it exits.
- The Go tools (perfseed, the git shim) build only with `-tags perf`, and the
  release gate fails if a release program links them (README, "Guard rails").

## Components

### 1. Seeder (`services/data/cmd/perfseed`, Go, `-tags perf`)

Writes a workload into a fresh database before the Data Service starts,
through the store's own `Log.WithTx` / `Tx.Append` and record stores, so rows
and events stay consistent; large transactions make 1M events take seconds.
Subcommands: `seed` (a profile), `calibrate` (per-type counts, mean and p95
payload sizes from the live DB, read-only; no content copied), `realism`
(prepares a backup copy: schema stepped down to BASE's, paths pointed at a
scratch dir), `gitfx` (the real-git fixture, below), `append` (scenario
8's live events) and `chat` (scenario 10x's conversation, below). The runner builds it from BASE's tree with this tree's
perfseed source laid over it (`go build -overlay`), so the schema is BASE's.

A profile (`tools/perf/profiles/*.json`) gives event counts and payload size
distributions per type, sessions, active sessions, delegates, links, and the
correctness fixtures (pending permission, queued messages, a provisioning
session, a live pipeline attempt).

| Profile | Events | Purpose |
|---|---|---|
| S | 10k | fast iteration |
| M | 100k | mid |
| L | ~308k | calibrated to the live DB (2026-11-19) |
| XL | 1M | growth |

### 2. Delay proxy (`ui/e2e/perf/proxyServer.mts`, its own Node process)

Between the browser and the Router; the page's origin is the proxy, so
Chromium's per-origin socket limit applies as it does live. Path rules
(`delayRules.ts`, `PERF_DELAYS=<regex>=<ms>,...`) add latency after the
request is accepted, so the socket is really held. Defaults mimic the live
measurements: `/sessions/*/repos` 10 s, `/changeset` 6 s,
`/worktree/refresh` 16 s. SSE is piped unbuffered (backpressure honoured).
The open conversation's `/output` stream is held open with heartbeats
(`PERF_HOLD_OUTPUT=0` turns that off). It logs every request (arrival,
delay, headers, end, bytes, SSE frames) and a busy-socket timeline, read by
the worker over IPC so nothing measured waits on the worker.

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

### 3. Collectors

Settle and every timing come from the **page probe** (`probe.ts`, an init
script: fetch/EventSource wrappers, resource and longtask observers, a 50 ms
lag probe, DOM markers by MutationObserver, all in page time) and the
proxy's log, never from CDP network events (they arrive late during a big
replay). CDP adds main-thread task/script time (`Performance.getMetrics`)
and SSE message counts; `ps` samples the stack's own pids; `lsof` cross-checks
sockets; the git shim logs git calls (below).

Deterministic metrics (exact; ⚠ in the report when one build's repetitions
disagree): `/events` bytes and frames (proxy), SSE messages (CDP),
(re)connects, connects with no `since` (replay from 0), streams that declared
a start, native EventSource auto-reconnects, `/events` batch pages and bytes,
short and delayed requests, held `/output` streams, peak busy sockets.

Timings (median and IQR, candidate/baseline ratio): click → settled and
click → marker (all legs and per leg), main-thread task and script time,
lag max, last `/events` byte, all-6-sockets-busy time, queued time (total,
p50/p95/max, count over 1 s, max queue depth), cancelled and unanswered
requests, abandoned requests (below), longtasks, Router/Data/children CPU,
peak RSS, Data Service connections closed.

**Settled** for a leg = its data marker holds (a named DOM predicate per
scenario), then, from the click on, all three of:
- no non-delayed short request in flight at the proxy or queued in the
  browser, and none started for 500 ms;
- every open `/events` stream has forwarded the newest seq it owes up to the
  log head read at the click (§2b), or owes none; a stream with bytes but no
  `id:` lines falls back to 1.5 s of byte idle (counted);
- the lag probe has had no sample over 100 ms for 500 ms.
Settled is the instant all three first held (the quiet windows not
counted). A leg capped at `PERF_SETTLE_CAP_MS` (180 s) fails; a stream
owing frames with no byte for 30 s fails the repetition.

### 4. Scenarios (`ui/e2e/perf/scenarios.ts`)

| # | Action |
|---|---|
| 1 | cold window load |
| 2 | switch session A → B → A, quiet between legs |
| 3 | open a delegate from the heaviest parent (transcript file) |
| 3b | the same, a delegate with no transcript file (log fallback) |
| 3t | open a delegate from a typical parent (~1,000 subagent events) |
| 4 | Retro → Sessions round trip |
| 5 | session → launchpad (control: nothing should replay) |
| 6 | with long-lived streams pinned (dock background output + a live pipeline attempt): S's Changes and Conversation tabs, a delegate, S → A → S |
| 7 | network drop and resume (every socket cut, as on sleep/wake) |
| 7b | offline for a while (connections refused: backoff, health, probes), then back |
| 8 | long-lived window: `PERF_LONGLIVED_N` events (default 50k at `PERF_LONGLIVED_RATE`/s) arrive live via `perfseed append`, then open a delegate |
| 9a–9h | real git, below |
| 10a–10cp | a live reply streaming into a long conversation, below |

Each scenario runs N times (default 5) per build and delay mode, with a
live-event smoke after it (a session renamed through the Router API shows
on its rail card within 2 s).

### 5. Correctness checks (`ui/e2e/perf/checks.ts`)

A faster design must not win by dropping state. Each check is a user-visible
assertion on what the page shows, whatever carries it there, run on a fresh
stack per build, delays off; each passes on main unless marked known-fail.
a–e came from the 2026-11-19 subscriber audit (state that then arrived only
through replay):
- **a-permission**: a pending permission request shows "awaiting approval"
  on the rail after a cold load and after a reload;
- **b-queue**: the queued-message list matches the server, on open and after
  a reload;
- **c-retro-progress**: an in-progress retro shows its progress (UI-only
  state: reported, excluded from bus comparisons);
- **d-pipeline-running**: the workspace's pipeline `running` flag and badge
  count are right, on open and after a reload;
- **e-provisioning**: provisioning steps render for a session still
  provisioning (fake provider with `MADS_FAKE_START_DELAY_MS`).
f–i are the git checks (below). `CHECKS=a,b|none` picks them.

### 6. Runner and report (`make ui-perf`, `tools/perf/run.sh`)

`make ui-perf BASE=<ref> CAND=<ref-or-worktree> PROFILE=L N=5 BROWSER=chromium|electron`

- `SCENARIOS` takes ids (`1,3b`) and aliases (`tools/perf/scenarioSets.sh`),
  resolved before anything else decides on them:
  - empty or `all`: every scenario, 9x included, so `GITFX=auto` builds the
    real-git fixture (empty under `REALISM=1` means `bus`: the two don't mix);
  - `bus`: 1–8, 3b, 3t, 7b — the event-bus regression set; no fixture build;
  - `git`: 9a–9h (delays off by default);
  - `chat`: 10a–10c, 10ap–10cp (delays off only);
  - aliases mix with ids (`bus,9a`); `none` runs checks only.
- `MODES=on,off` (default both; 9x and 10x only: off), `CHECKS` (default a–e; none
  in realism), `REALISM=1`, `GITFX`, `PERF_PROBE=0` / `PERF_PROBE_CAND=0`
  (lite probe: markers only).
- Builds each side's binaries and UI into separate temp dirs (a ref via a
  temp detached `git worktree add`, removed after). This tree's
  `providers/fake` is overlaid on every build (the harness's dialects).
- Within a scenario and mode the builds alternate **ABBA** (A B, B A, A B,
  …) on fresh copies of one seeded workload, so drift lands on both alike.
- Writes `tools/perf/results/<timestamp>/` (gitignored): `plan.json`,
  `manifest.json`, `raw/`, `results.json`, `report.md`, `stack-logs/`,
  failure screenshots. Deterministic metrics exact, timings as median and
  IQR with the candidate/baseline ratio; failed and missing repetitions are
  listed, never dropped.

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
| git calls and CPU, whole window and per leg | the **git shim** (`tools/perf/gitshim`, `-tags perf`, first on every fixture stack's PATH; `PERF_REAL_GIT` is the real one): one JSON line per call with start/end, parent pid, subcommand, and the call's rusage (user + system). Top subcommands by CPU per build |
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

## Chat stream (scenarios 10x)

Added 2026-11-24 for [docs/CHAT-STREAM-PER-BATCH-COST.md](../../../docs/CHAT-STREAM-PER-BATCH-COST.md):
every streamed batch re-renders every message in the chat view, so the cost
per batch grows with the rows loaded. The fix comes in two steps (A:
timestamps, the forced layout, the reducer's copy; B: per-item
memoization); 10x measures main, then each.

### Conversation (`perfseed chat`)

One long claude-code transcript, read by claude-code's own reader
(`providers/claudecode` `KeyedTranscript`) and stored as the fake
provider's history file (`$MADS_HOME/fake-transcripts/<ref>.json`) with a
fake session resuming it. The Router's fake serves it while the session is
dormant; the session child's fake resumes it from
`MADS_FAKE_PERF_HISTORY_DIR` (perf.go), so the rows are exactly
claude-code's and the fake can stream a live reply into the same
conversation. (A claude-code session can't stream hermetically: there is no
claude CLI.) `CHAT_SRC`:
- `synthetic` (default): a claude-code jsonl generated in the measured
  shape, `CHAT_TURNS=500` turns → 3,539 rows (500 user, 763 agent, 1,138
  tool calls and results), 1.2 MB of text; per turn a prompt, sometimes a
  note, 1–4 tool calls (Bash, Read, Edit, Grep) with results, a Markdown
  reply; timestamps over 30 days.
- a real transcript (`.jsonl`): copied into the run's temp dir (the
  original only read), deleted with it; the manifest and report carry its
  shape only (source "real", turns, rows by role, text bytes).

### Scenarios

Three sizes, each unpaced and paced (`stream_speed` = `PERF_CHAT_PACE`,
default fast, 1500 words a minute: useReveal commits every frame):

| # | Loaded before the stream |
|---|---|
| 10a / 10ap | the mount's last page (100 rows) |
| 10b / 10bp | 9 more pages by scroll-up paging (scrollTop 0 → useOlderPages), `PERF_CHAT_MID_PAGES=10` |
| 10c / 10cp | every page, by Ctrl+↑↑ (loadAllOlder, merged as one) |

Prepare (not measured): set the pace, wake the child with a warm-up turn
(through the Router), open the session from the rail, load the size, go
back to the tail with Ctrl+↓↓ (following), then a full GC
(`HeapProfiler.collectGarbage`) and the heap and DOM counts. The leg: type
`perf-stream <chars> <rate> <delta>` in the composer and press Enter; the
fake streams `PERF_CHAT_CHARS=15000` characters of generated Markdown
(headings, paragraphs with inline code and bold, bullets, fenced code) as
`PERF_CHAT_DELTA=25`-character deltas at `PERF_CHAT_RATE=60` a second (600
deltas, 10 s). Its marker: turn end received and, unpaced, the whole reply
on screen. The proxy passes `/output` through for 10x (nothing held).

### Metrics

Window: the first text delta the page receives → turn end, in page time.
- **In-page recorder** (`chat.ts` CHAT_PROBE, an init script; records only
  once armed): every `text_delta` / `turn_end` the app's `/output`
  EventSource gets (a listener of its own per kind); every React commit,
  via the DevTools global hook (`__REACT_DEVTOOLS_GLOBAL_HOOK__`, which
  React calls in production builds; every root's commits count); every
  frame (rAF); and in the task after each frame (layout clean, nothing
  forced) the scroller's distance from its bottom, its `data-stick` mode,
  and the letters and digits of the reply as rendered (its text minus the
  hover `<time>` and code blocks' Copy buttons).
- From those: deltas/s, commits/s and per delta, frame interval p50/p95,
  frames over 50 ms, long tasks (the probe's longtask observer), delta →
  on screen p50/p95 (the first sample after a delta that renders its
  running count; paced, the reveal's lag by design), frames off the bottom.
- **CDP trace** (`PERF_CHAT_TRACE=0` turns it off), started before the
  leg and read after the window, sliced between the recorder's own
  `performance.mark`s (`perf-chat-first-delta`, `perf-chat-turn-end`) on
  their thread: busy = top-level task time / window; ms per commit and per
  delta; self time by bucket (JS, style, layout, paint/composite, GC,
  other) per commit, with the spike's buckets.
- After loading: rows loaded (pages × 100, capped), items rendered, DOM
  nodes (page, stream), JS heap used after a full GC (MB).

### Checks (every repetition; a failure fails it)

- the wire carried the whole reply: what the page's EventSource received
  equals the reply the agent stored (`GET /transcript`), 15,000 characters;
- all of it is on screen, in order: the rendered letters and digits equal
  the wire's (Markdown syntax is all punctuation; the generator writes no
  ordered lists, links or HTML); paced, after Show all;
- stick-to-bottom held: `data-stick` stayed `follow` all window, and within
  5 s after the scroller is within 10 px of its bottom with the reply's end
  in view (a runway page turn glides; after Show all the last pin takes
  ~1.4 s).

## History

- 2026-11-19: plan; seeder, calibration, proxy, collectors, scenarios 1–5.
- 2026-11-20: three review rounds moved settle into the page probe (CDP
  arrived late), added scenarios 6–8, 3b/3t/7b, checks a–e and realism; the
  baseline reproduced the live replay (scenario 3 ~140 MB+ of subagent
  history at L) before any spike was measured. Spikes R0–R3 compared and R2
  rebuilt for production (EVENT-BUS-RESULTS.md).
- 2026-11-21/22: real-git fixture, the git shim, scenarios 9a–9h and checks
  f–i for the git routes (Phase A and B, docs/GIT-STATE-DESIGN.md and
  docs/git-routes-overview.html).
- 2026-11-22: moved to `tools/perf/` as durable dev tooling (was
  `ui/e2e/perf/` + `docs/`).
- 2026-11-24: chat stream scenarios 10x, `perfseed chat`, the fake's
  `perf-stream` dialect and resume from `MADS_FAKE_PERF_HISTORY_DIR`.
