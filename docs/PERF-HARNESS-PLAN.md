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
- why `repos`, `changeset` and `worktree/refresh` are slow. We mock their
  latency and investigate the cause later.
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

- Builds each ref's UI and binaries into separate temp dirs.
- Runs baseline and candidate alternately (ABAB…) on the same seeded
  workload, so machine noise hits both.
- Writes `perf-results/<timestamp>/` (gitignored): raw JSON and a markdown
  table per scenario. Deterministic metrics are shown as exact values;
  timings as median and IQR, with the candidate/baseline ratio.

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
