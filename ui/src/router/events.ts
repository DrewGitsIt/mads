import { ROUTER_BASE } from "./client";
import { onSessionHostsChanged, peerOfSession } from "./sessionHosts";
import type { Envelope } from "./types";

export interface SubscribeParams {
  sessionId?: string;
  types: string[];
  /**
   * Mesh mode: called when this subscription moved to another bus (its
   * session's host became known after it subscribed), once that bus is
   * ready. A subscriber that seeded from REST redoes that read here: its
   * snapshot was ordered against the other bus's log, not this one's. A
   * subscriber without it is expected to be remounted by its owner (the
   * session workspace is keyed by host, see SessionsSurface).
   */
  onReseed?: () => void;
}

interface Subscription {
  sessionId?: string;
  types: string[];
  onEvent: (envelope: Envelope) => void;
  onReseed?: () => void;
  /** The bus it's on: LOCAL, or a remote session's (see busKeyFor). */
  bus: string;
}

// The dispatch queue only grows when a subscriber's onEvent makes more frames
// arrive while it is still running (EventSource delivers one frame per task
// otherwise, so a burst after a reconnect drains as it comes). Generous
// margin, not a tuning knob. SSE has no app-level backpressure; if the queue
// would ever exceed this, the durable log + cursor is the recovery mechanism,
// not an in-memory buffer — drop the connection and resume from the cursor
// rather than buffer unboundedly.
const MAX_QUEUE = 500;
// How long a GET /events/head may take before it counts as a failed connect.
const HEAD_TIMEOUT_MS = 10_000;
const BACKOFF_BASE_MS = 1000;
// The reconnect backoff's cap: the one number to change to retune how long a
// down Router or Data Service can keep a window waiting after it is back.
// 2 × the measured worst realistic restart (2026-11-21): a full restart right
// after a rebuild, cold binaries plus the first head read on a ~300k-event log,
// came back in ~1 s; warm restarts take 50–250 ms. The bus talks to localhost,
// so a failed attempt is cheap and a short cap costs little while it's down.
const BACKOFF_MAX_MS = 2000;
// A peer bus's cap: its failures are a dark, refusing or older peer, which
// a fast retry only hammers (and the host audits every refused try). Also
// how often a peer bus whose log can't be followed rechecks its head.
const PEER_BACKOFF_MAX_MS = 30_000;
// How often a paused peer bus (its host refuses this device) tries again on
// its own, besides a peer.* event or a wake: rare, since each refused try
// is audited on the host.
const PAUSED_RECHECK_MS = 5 * 60_000;
// How long a job action (useRetroGeneration, useAgentGeneration,
// useRegistryJob) waits for the bus before giving up; see eventsReadyWithin.
const READY_TIMEOUT_MS = 10_000;
// Before the first cursor, how long the head read must keep failing before
// health says "failing": the Data Service can come up a beat behind the
// Router at startup, and a banner that flashes for that says nothing.
const COLD_FAILING_AFTER_MS = 1000;
// Envelopes are stamped by the producer, which can stamp a moment before the
// Data Service commits (and so before the head read that fixed the cursor
// sees it); the notice window opens this much before the first head read.
const NOTICE_SLACK_MS = 2000;
// Stream opens that fail in a row, each after a head read that answered,
// before the console says the Router may be refusing a subscribed type.
const REFUSED_OPEN_LOG_AFTER = 3;

// THE CONTRACT: the bus is live-only. It never replays history; a consumer
// that needs state from before it subscribed reads it over REST, behind the
// ready barrier (eventsReady, below).
//
// - One scalar cursor: the highest seq delivered on this bus. Every
//   connection is opened with since=<cursor>, and anything at or below it is
//   dropped as already delivered (the log is one seq-ordered stream, resumed
//   strictly after the cursor).
// - The first connect reads GET /events/head and starts the cursor there, so
//   it never starts from the beginning of the log (since=0 only when the
//   log was empty, which means the same thing).
// - Widening (a subscriber brings a type the open connection doesn't carry),
//   a drop, and a queue overflow all reconnect with since=<cursor>. A newly
//   carried type gets only its events after the cursor — never its history.
//   (The cursor trails the head while the carried types are quiet, so that
//   can include the new type's events from the last few moments: older than
//   the subscribe, but all newer than anything this window was sent.)
// - At zero subscribers the connection closes but the cursor and readiness
//   stay, so a surface round trip (Sessions → Retro → Sessions) resumes from
//   where it left off rather than replaying or skipping.
//
// Mesh mode: every rule above holds per bus. A bus is one log: LOCAL is
// this device's (GET /events, GET /events/head), and a remote session's bus
// is its host's log, read through this device's Router
// (GET /peers/{peer}/events?session_id=, GET /peers/{peer}/events/head).
// Each bus has its own cursor, head read, ready barrier, backoff and
// "incompatible" state; seqs from two logs are never compared. busKeyFor
// picks the bus for a subscription, and eventsReady(sessionId) the barrier
// that orders that session's REST reads.

// --- Health (this device's bus only) ----------------------------------------

/**
 * Whether live updates are reaching this window. "failing" means the stream
 * has failed to connect repeatedly — every live view (the session rail, task
 * counts, the Tasks tab's run state) is showing whatever it last fetched and
 * nothing will correct it until the stream comes back. Surfaces show that
 * rather than let stale state pass for current: a frozen rail reading
 * "Sleeping" beside a conversation that is plainly awake is how the last
 * outage of this kind (an unregistered type failing the whole stream) was
 * first noticed.
 *
 * This is this device's bus only (LOCAL). A peer bus failing is the peer's
 * reachability, which the session list already shows; a peer bus that is
 * incompatible marks that peer (subscribeToIncompatiblePeers), never the
 * window.
 */
export interface BusHealth {
  /**
   * "incompatible" is terminal: the services are older than this UI (no
   * GET /events/head), so the bus never connects and eventsReady never
   * resolves. Only a restart of mads and a reload get out of it.
   */
  state: "live" | "connecting" | "failing" | "incompatible";
}

// Two misses in a row: a single drop is ordinary (a Router restart, a sleep/
// wake) and reconnects within a second; only a second one is worth a word.
const FAILING_AFTER = 2;

const healthListeners = new Set<(health: BusHealth) => void>();
let health: BusHealth = { state: "connecting" };

function publishHealth(state: BusHealth["state"]): void {
  if (health.state === "incompatible" && state !== "incompatible") return; // terminal
  if (state === health.state) return;
  health = { state };
  for (const listener of healthListeners) listener(health);
}

export function getBusHealth(): BusHealth {
  return health;
}

/** Calls listener on every BusHealth change. Returns an unsubscribe function. */
export function subscribeToBusHealth(listener: (health: BusHealth) => void): () => void {
  healthListeners.add(listener);
  return () => {
    healthListeners.delete(listener);
  };
}

// --- Peers whose log can't be followed --------------------------------------

// Peers whose GET /events/head answered 404/405: their mads predates the
// live-only bus. Their sessions' buses stop; the rest of the window doesn't.
const incompatiblePeers = new Set<string>();
const incompatiblePeerListeners = new Set<() => void>();

/** Peers whose event log this window can't follow (their mads is older). */
export function getIncompatiblePeers(): ReadonlySet<string> {
  return incompatiblePeers;
}

/** Calls listener whenever a peer is found incompatible. */
export function subscribeToIncompatiblePeers(listener: () => void): () => void {
  incompatiblePeerListeners.add(listener);
  return () => {
    incompatiblePeerListeners.delete(listener);
  };
}

function markPeerIncompatible(peer: string): void {
  if (incompatiblePeers.has(peer)) return;
  incompatiblePeers.add(peer);
  for (const l of incompatiblePeerListeners) l();
}

function clearPeerIncompatible(peer: string): void {
  if (!incompatiblePeers.delete(peer)) return;
  for (const l of incompatiblePeerListeners) l();
}

// --- A remote session's live updates ------------------------------------------

/**
 * Whether a remote session's live updates reach this window:
 * - "live": its bus is connected or connecting, as usual;
 * - "unavailable": its host's mads is older than this window (no
 *   GET /events/head there). Its REST reads still run (the bus's barrier
 *   resolves in this degraded mode) but nothing live arrives;
 * - "paused": its host refuses this device the stream (a 403 such as
 *   no_access: its grant there dropped). Resumed by a peer.* event, coming
 *   back online or to the foreground (resumePeerBuses).
 * `epoch` counts recoveries to "live": a consumer that read REST while the
 * bus was degraded reads again (SessionsSurface keys the workspace by it).
 * This device's sessions are always "live" here: the window's BusHealth
 * speaks for them.
 */
export interface SessionLive {
  state: "live" | "unavailable" | "paused";
  peer?: string;
  epoch: number;
}

const LIVE: SessionLive = { state: "live", epoch: 0 };
const sessionLiveListeners = new Set<() => void>();

function notifySessionLive(): void {
  for (const l of sessionLiveListeners) l();
}

/** Calls listener whenever any remote session's SessionLive changes. */
export function subscribeToSessionLive(listener: () => void): () => void {
  sessionLiveListeners.add(listener);
  return () => {
    sessionLiveListeners.delete(listener);
  };
}

/** The live state of `sessionId`'s bus (see SessionLive). */
export function getSessionLive(sessionId: string): SessionLive {
  const key = busKeyFor(sessionId);
  if (key === LOCAL) return LIVE;
  return buses.get(key)?.live ?? LIVE;
}

/**
 * Asks every paused or unavailable peer bus (or only `peer`'s) to try its
 * head again now: something changed that may let it in (a peer.* event, a
 * grant change, coming back online).
 */
export function resumePeerBuses(peer?: string): void {
  for (const bus of buses.values()) bus.resume(peer);
}

// --- Messages ---------------------------------------------------------------

/** The message a job action shows when the bus isn't ready in time. */
export const EVENTS_NOT_READY_MESSAGE = "Live updates aren't connected yet. Try again in a moment.";

/**
 * What the "incompatible" state means to the user: App.tsx's banner, and
 * what a job action says at once rather than "try again".
 */
export const EVENTS_INCOMPATIBLE_MESSAGE =
  "mads services are older than this window, so live updates and lists can't load. Restart mads (scripts/app.sh --restart) and reload.";

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/** GET /events/head is missing: the services predate this UI. */
class IncompatibleServicesError extends Error {}

/** A peer refused this device its log (403, not not_exposed). */
class RefusedByPeerError extends Error {}

// When this window's notice window opened; see windowNoticesSince. Never
// reset in production: it lives as long as the window does.
let noticesSince: number | undefined;

// Test hook state: the cursor a new bus starts primed at (see
// resetEventsBusForTests); undefined in production.
let primedCursor: number | undefined;

// --- One bus ----------------------------------------------------------------

/**
 * One log's live stream: one EventSource shared by every subscriber on it,
 * and everything that keeps it honest (the cursor, the head read, the ready
 * barrier, the reconnect backoff, the dispatch queue).
 *
 * The LOCAL bus is shared by every control-plane subscriber — several
 * independent hooks (the rail's task counts, a session workspace's
 * pipeline-running dot, the Tasks tab's live task list) legitimately want
 * different, overlapping type sets at the same time, and each opening its
 * own connection burns through the browser's ~6-per-origin HTTP/1.1 socket
 * cap, starving ordinary fetches (e.g. a task Save PUT hangs forever queued
 * behind a fistful of otherwise-redundant SSE streams). Server-side
 * filtering there is by `types` only, never `session_id`: in practice at
 * least one active subscriber is session-agnostic whenever the rail is
 * mounted, so a server-side session filter buys nothing and would force a
 * reconnect on every session switch. Session scoping is a client-side
 * concern instead — see dispatchOne's `sub.sessionId` check.
 *
 * Mesh mode adds one bus per remote session that has subscribers (a
 * conversation open on a peer's session): `/peers/{peer}/events`, filtered
 * server-side by that session, so the relayed link carries only that
 * session's events.
 */
class Bus {
  private source: EventSource | null = null;
  // The type set `source` was opened with — what the open connection
  // actually carries. A widen is judged against this, not against the
  // current subscribers: React runs an old effect's cleanup before the new
  // effect, so on a session switch the subscriber union briefly shrinks (no
  // reconnect — removing never narrows) and the new session re-adding the
  // same types must not look like a widen.
  private connectedTypes = new Set<string>();
  readonly subscriptions = new Set<Subscription>();
  // undefined until the first head read lands; outlives teardown at zero
  // subscribers.
  cursor: number | undefined;
  // Bumped by every connect attempt and teardown, so a head read that lands
  // after either can't open a connection nobody asked for.
  private connectGen = 0;
  // A connect is waiting on GET /events/head (the first one, or a recheck
  // after an error). It reads the subscriber union when the head lands, so
  // subscribes in the meantime fold into it.
  private headPending = false;
  // Set by a failed connection: the next connect re-reads the head first, in
  // case the log was reset (wiped, or swapped for another) while this window
  // stayed open — a cursor above the new log's end would otherwise skip
  // everything until the new log caught up with it.
  private recheckHead = false;
  private warnedLogReset = false;
  private reconnectAttempt = 0;
  // Consecutive failed connects with no "open" in between. Feeds BusHealth.
  private consecutiveFailures = 0;
  // GET /events/head answered 404/405 (or, through a peer's Mesh Service,
  // 403 not_exposed), so the log's services predate this UI. Terminal for
  // this device's bus; a peer bus rechecks its head at PEER_BACKOFF_MAX_MS
  // and on a wake, and recovers when it answers (see enterIncompatible).
  incompatible = false;
  // A peer bus whose host refused this device its log (a 403 other than
  // not_exposed): no retries until resume() (see enterPaused).
  private paused = false;
  // Set by resume() of a paused bus: the next head that answers starts the
  // bus over from it, and consumers read again (recover).
  private reseedOnHead = false;
  // Peer buses: their SessionLive.
  live: SessionLive = LIVE;
  // The first head read failed and the bus is still waiting for its first
  // cursor: logged once on the way in, once on the way out.
  private waitingLogged = false;
  // Pending while the first head read has failed and the bus has no cursor
  // yet: fires COLD_FAILING_AFTER_MS after that first failure.
  private coldFailingTimer: ReturnType<typeof setTimeout> | undefined;
  // Whether the open EventSource has fired "open".
  private sourceOpened = false;
  // Stream connects that failed before "open" with no open in between;
  // reset by an open and by a failed head read (then it's the services, not
  // a type).
  private failedOpens = 0;
  private refusedOpenLogged = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  private queue: Envelope[] = [];
  private draining = false;
  private wakeListening = false;

  // The ready barrier: resolves once the cursor is known (the first head
  // read landed) and stays resolved for the bus's life — reconnects, widens
  // and teardown never un-ready it.
  private resolveReady: (() => void) | undefined;
  ready: Promise<void>;

  constructor(
    /** "" for this device, else the peer's name. */
    private readonly peer: string,
    /** Server-side session filter; only peer buses set it. */
    private readonly sessionId: string | undefined,
  ) {
    this.ready = new Promise<void>((resolve) => {
      this.resolveReady = resolve;
    });
    if (primedCursor !== undefined) {
      this.cursor = primedCursor;
      this.markReady();
    }
  }

  private get isLocal(): boolean {
    return this.peer === "";
  }

  /** "/events" here, "/peers/{peer}/events" for a peer, under ROUTER_BASE. */
  private get eventsPath(): string {
    return this.isLocal ? "/events" : `/peers/${encodeURIComponent(this.peer)}/events`;
  }

  private label(): string {
    return this.isLocal ? "events" : `events (${this.peer})`;
  }

  private markReady(): void {
    this.resolveReady?.();
    this.resolveReady = undefined;
  }

  private setHealth(state: BusHealth["state"]): void {
    if (this.isLocal) publishHealth(state);
  }

  private unionTypes(): string[] {
    const set = new Set<string>();
    for (const sub of this.subscriptions) for (const t of sub.types) set.add(t);
    return [...set];
  }

  private url(path: string): URL {
    // ROUTER_BASE is "" for same-origin builds (e.g. served by the Router
    // itself) — the URL constructor requires an absolute base, so fall back
    // to the page's own origin in that case.
    return new URL(`${ROUTER_BASE}${path}`, window.location.origin);
  }

  private async fetchHead(): Promise<number> {
    // A head read that hangs would hold every connect (and the ready
    // barrier) forever: give up after HEAD_TIMEOUT_MS, which counts as a
    // failed connect.
    const abort = new AbortController();
    const timer = setTimeout(() => abort.abort(), HEAD_TIMEOUT_MS);
    try {
      const res = await fetch(this.url(`${this.eventsPath}/head`).toString(), {
        headers: { Accept: "application/json" },
        signal: abort.signal,
      });
      if (res.status === 404 || res.status === 405) {
        // Through /peers/{peer}/, the Router's own 404 for a peer it no
        // longer has isn't the peer's answer: retry like any failed read.
        if (!this.isLocal && res.status === 404) {
          const body = (await res.json().catch(() => ({}))) as { code?: string };
          if (body.code === "peer_not_found") throw new Error(`GET ${this.eventsPath}/head: peer not found`);
        }
        throw new IncompatibleServicesError(`GET ${this.eventsPath}/head: ${res.status}`);
      }
      if (!this.isLocal && res.status === 403) {
        // The peer's Mesh Service: not_exposed is a route its table
        // doesn't list (a build from before this route), anything else is
        // it refusing this device (no_access once its grant there drops).
        const body = (await res.json().catch(() => ({}))) as { code?: string };
        if (body.code === "not_exposed") throw new IncompatibleServicesError(`GET ${this.eventsPath}/head: 403 not_exposed`);
        throw new RefusedByPeerError(`GET ${this.eventsPath}/head: 403 ${body.code ?? ""}`.trim());
      }
      if (!res.ok) throw new Error(`GET ${this.eventsPath}/head: ${res.status}`);
      const body = (await res.json()) as { seq?: unknown };
      if (typeof body.seq !== "number" || !Number.isFinite(body.seq) || body.seq < 0) {
        throw new Error(`GET ${this.eventsPath}/head: no seq`);
      }
      return body.seq;
    } finally {
      clearTimeout(timer);
    }
  }

  private buildUrl(types: string[], since: number): string {
    const url = this.url(this.eventsPath);
    url.searchParams.set("types", types.join(","));
    url.searchParams.set("since", String(since));
    if (this.sessionId) url.searchParams.set("session_id", this.sessionId);
    return url.toString();
  }

  private dispatchOne(env: Envelope): void {
    // Belt-and-suspenders: the Go log's Since is documented "strictly
    // greater than" already, so server-side overlap on resume shouldn't
    // happen — but this dedupe is what makes the exactly-once guarantee
    // independent of that implementation detail. Scalar, like the cursor:
    // the stream is one seq-ordered log whatever the types, and a newly
    // carried type is resumed from the same cursor, so nothing below it is
    // still owed to anyone.
    if (env.seq <= (this.cursor ?? 0)) return;
    this.cursor = env.seq;
    this.reconnectAttempt = 0; // live data flowing — connection is healthy
    this.consecutiveFailures = 0;
    this.setHealth("live");
    for (const sub of this.subscriptions) {
      if (sub.sessionId && env.session_id !== sub.sessionId) continue;
      if (!sub.types.includes(env.type)) continue;
      sub.onEvent(env);
    }
  }

  private handleFrame = (e: MessageEvent<string>): void => {
    this.sourceOpened = true; // a frame proves the stream opened
    this.failedOpens = 0;
    this.refusedOpenLogged = false;
    this.queue.push(JSON.parse(e.data) as Envelope);
    if (this.queue.length > MAX_QUEUE) {
      // Never buffer unboundedly — drop what's queued and resume from the
      // durable log at the cursor (our last *dispatched* seq). Nothing
      // already delivered is redelivered; nothing still queued is lost, only
      // re-fetched.
      this.queue = [];
      this.draining = false;
      this.scheduleReconnect();
      return;
    }
    // A subscriber's onEvent can itself trigger more frames reentrantly (e.g.
    // a burst arriving while one is still being handled). Only the outermost
    // call drains — a reentrant call just enqueues and returns — so the
    // queue actually reflects backlog depth instead of every push draining
    // itself one at a time, which would make MAX_QUEUE unreachable.
    if (this.draining) return;
    this.draining = true;
    while (this.queue.length > 0) this.dispatchOne(this.queue.shift()!);
    this.draining = false;
  };

  private onConnectError = (): void => {
    if (!this.sourceOpened) {
      // The head read before this connect answered (every failed connect
      // rechecks it), yet the stream never opened. Repeatedly, that is most
      // likely the Router refusing the request itself (a 400 for a type it
      // doesn't know, from a UI bug) rather than an outage, which the banner
      // can't tell apart: name the type set so it can be checked.
      this.failedOpens++;
      if (this.failedOpens >= REFUSED_OPEN_LOG_AFTER && !this.refusedOpenLogged) {
        this.refusedOpenLogged = true;
        console.error(
          `${this.label()}: the event stream failed to open ${this.failedOpens} times in a row while GET ${this.eventsPath}/head answers — the Router may be refusing a subscribed type (types: ${[...this.connectedTypes].join(",")})`,
        );
      }
    }
    this.consecutiveFailures++;
    if (this.consecutiveFailures >= FAILING_AFTER) this.setHealth("failing");
    this.recheckHead = true;
    this.scheduleReconnect();
  };

  private scheduleReconnect(): void {
    this.source?.close();
    this.source = null;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    const cap = this.isLocal ? BACKOFF_MAX_MS : PEER_BACKOFF_MAX_MS;
    const delay = Math.random() * Math.min(cap, BACKOFF_BASE_MS * 2 ** this.reconnectAttempt);
    this.reconnectAttempt++;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      if (this.subscriptions.size > 0) this.openConnection();
    }, delay);
  }

  private clearColdFailing(): void {
    if (this.coldFailingTimer) clearTimeout(this.coldFailingTimer);
    this.coldFailingTimer = undefined;
  }

  private openConnection(): void {
    if ((this.incompatible && this.isLocal) || this.paused) return;
    this.source?.close();
    this.source = null;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = undefined;
    // Whatever the old connection queued is redelivered by the new one (it
    // resumes from the cursor, which only dispatching advances).
    // Dispatching it anyway could push the cursor past events of a type the
    // new connection adds, and they would then be dropped as already seen.
    this.queue = [];
    const gen = ++this.connectGen;
    // The window's notice epoch opens with its first head read (see
    // windowNoticesSince), unless a notice-gated subscriber already asked.
    if (this.cursor === undefined && noticesSince === undefined) noticesSince = Date.now() - NOTICE_SLACK_MS;
    if (this.cursor !== undefined && !this.recheckHead && !this.incompatible) {
      this.openSource();
      return;
    }
    // First connect on this bus: learn where the log ends and start there.
    // After a failed connection: check the log still reaches the cursor.
    // A failed read is a failed connect — same backoff, same health.
    this.headPending = true;
    this.fetchHead().then(
      (head) => {
        if (gen !== this.connectGen) return;
        this.headPending = false;
        this.recheckHead = false;
        if (!this.isLocal) this.peerHeadAnswered();
        if (this.incompatible || this.reseedOnHead) this.recover(head);
        if (this.cursor === undefined) {
          this.cursor = head;
          this.clearColdFailing();
          this.markReady();
          if (this.waitingLogged) {
            this.waitingLogged = false;
            console.info(`${this.label()}: reached the event log; live updates connected`);
          }
        } else if (head < this.cursor) {
          // The log is behind us: it was reset. Nothing past its end exists,
          // so resume from its end rather than wait for it to pass the old one.
          if (!this.warnedLogReset) {
            this.warnedLogReset = true;
            console.warn(
              `${this.label()}: the event log restarted below this window's cursor (${head} < ${this.cursor}); resuming from ${head}`,
            );
          }
          this.cursor = head;
        }
        if (this.subscriptions.size > 0) this.openSource();
      },
      (err: unknown) => {
        if (gen !== this.connectGen) return;
        this.headPending = false;
        if (err instanceof IncompatibleServicesError) {
          this.enterIncompatible(err.message);
          return;
        }
        if (err instanceof RefusedByPeerError) {
          this.enterPaused(err.message);
          return;
        }
        this.consecutiveFailures++;
        this.failedOpens = 0; // the services are down; not a refused type
        // Before the first cursor every view is waiting on the barrier, so
        // say so once the head has been failing for COLD_FAILING_AFTER_MS
        // (not after FAILING_AFTER misses, which a fast retry can reach in
        // milliseconds, and not on the first miss, which a cold start makes).
        // Only for this device's bus: a peer's is its reachability's to say.
        if (this.cursor === undefined) {
          if (this.isLocal && !this.waitingLogged && !this.coldFailingTimer) {
            const detail = errorText(err);
            this.coldFailingTimer = setTimeout(() => {
              this.coldFailingTimer = undefined;
              if (this.cursor !== undefined || this.incompatible || this.subscriptions.size === 0) return;
              this.setHealth("failing");
              this.waitingLogged = true;
              console.error(`events: can't read the event log's head (${detail}); retrying — views wait until it answers`);
            }, COLD_FAILING_AFTER_MS);
          }
        } else if (this.consecutiveFailures >= FAILING_AFTER) {
          this.setHealth("failing");
        }
        this.scheduleReconnect();
      },
    );
  }

  /**
   * The log's services are older than this UI: no GET /events/head, so
   * there is no live-only start point to connect from. Retrying can't fix
   * that and connecting without a head would bring back the replay this bus
   * no longer handles, so the bus stops for good: no reconnects, the ready
   * barrier stays shut.
   *
   * For this device that's the whole window: health says "incompatible" so
   * the shell explains why every view is waiting (App.tsx's banner). For a
   * peer it's that peer's sessions only: the peer is marked stale (where
   * its reachability shows) and every other bus keeps running.
   */
  private enterIncompatible(detail: string): void {
    const already = this.incompatible;
    this.incompatible = true;
    this.clearColdFailing();
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = undefined;
    this.source?.close();
    this.source = null;
    if (this.isLocal) {
      this.listenForWake(false);
      console.error(
        `events: ${detail} — the mads services are older than this window. Restart mads (scripts/app.sh --restart) and reload.`,
      );
      this.setHealth("incompatible");
      return;
    }
    // A peer: degraded, not stopped. The barrier opens so REST reads run
    // (there is no live stream to order them against), the peer reads as
    // needing an update, and the head is rechecked at the slow cap and on
    // a wake: an updated peer recovers without a reload.
    if (!already) {
      console.error(`${this.label()}: ${detail} — mads on ${this.peer} is older than this window; its sessions' live updates are off until it's updated`);
    }
    this.markReady();
    markPeerIncompatible(this.peer);
    this.setLive("unavailable");
    this.scheduleRecheck();
  }

  /** A peer bus refused by its host: wait for resume(), no retries. */
  private enterPaused(detail: string): void {
    const already = this.paused;
    this.paused = true;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = undefined;
    this.source?.close();
    this.source = null;
    if (!already) console.warn(`${this.label()}: ${detail} — ${this.peer} doesn't let this device view; live updates paused`);
    // REST reads run (and say what the host refuses) rather than wait.
    this.markReady();
    this.setLive("paused");
    this.schedulePausedRecheck();
  }

  // A paused bus's own, rare retry (PAUSED_RECHECK_MS).
  private schedulePausedRecheck(): void {
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      if (this.subscriptions.size > 0) this.resume();
    }, PAUSED_RECHECK_MS);
  }

  /**
   * This peer's head answered: its mads can be followed, whatever an
   * earlier bus for one of its sessions found. Its mark clears; a sibling
   * bus still marked unavailable starts over (one with subscribers tries
   * its head now, an idle one on its next subscribe), so no bus that has
   * since gone idle keeps the peer reading "needs update".
   */
  private peerHeadAnswered(): void {
    for (const bus of buses.values()) {
      if (bus === this || bus.peer !== this.peer || !bus.incompatible) continue;
      if (bus.subscriptions.size === 0) bus.forgetIncompatible();
      else bus.resume();
    }
    clearPeerIncompatible(this.peer);
  }

  /**
   * An idle unavailable bus starts over as if new: no cursor and a shut
   * barrier, so its next subscriber's REST read waits for a real head read
   * (its degraded barrier ordered nothing). The one exception to "never
   * un-ready", and only with nobody waiting on this bus.
   */
  private forgetIncompatible(): void {
    this.incompatible = false;
    this.recheckHead = false;
    this.reseedOnHead = false;
    this.cursor = undefined;
    this.ready = new Promise<void>((resolve) => {
      this.resolveReady = resolve;
    });
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = undefined;
    this.setLive("live");
  }

  // The slow head recheck of an unavailable peer bus.
  private scheduleRecheck(): void {
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      if (this.subscriptions.size > 0) this.openConnection();
    }, PEER_BACKOFF_MAX_MS);
  }

  /** An unavailable peer bus's head answered: live again from that head. */
  private recover(head: number): void {
    this.incompatible = false;
    this.reseedOnHead = false;
    // Whatever was read while degraded wasn't ordered against this log:
    // start at its head and have consumers read again (onReseed, epoch).
    this.cursor = head;
    this.setLive("live", true);
    for (const sub of this.subscriptions) sub.onReseed?.();
  }

  private setLive(state: SessionLive["state"], recovered = false): void {
    if (this.isLocal) return;
    const epoch = this.live.epoch + (recovered ? 1 : 0);
    if (this.live.state === state && !recovered) return;
    this.live = { state, peer: this.peer, epoch };
    notifySessionLive();
  }

  /** Try again now (resumePeerBuses): a paused or unavailable peer bus. */
  resume(peer?: string): void {
    if (this.isLocal || (peer !== undefined && peer !== this.peer)) return;
    if (!this.paused && !this.incompatible) return;
    if (this.paused) {
      this.paused = false;
      this.recheckHead = true;
      this.reseedOnHead = true;
    }
    if (this.subscriptions.size === 0 || this.headPending) return;
    this.reconnectAttempt = 0;
    this.openConnection();
  }

  private openSource(): void {
    if (this.cursor === undefined) return; // only ever called once the head is known
    const types = this.unionTypes();
    this.connectedTypes = new Set(types);
    this.sourceOpened = false;
    const source = new EventSource(this.buildUrl(types, this.cursor));
    this.source = source;
    for (const type of types) source.addEventListener(type, this.handleFrame as EventListener);
    source.addEventListener("open", () => {
      this.sourceOpened = true;
      this.failedOpens = 0;
      this.refusedOpenLogged = false;
      this.consecutiveFailures = 0;
      this.setHealth("live");
    });
    // A native EventSource auto-reconnects to the exact same URL (the same
    // original `since`), which would redeliver everything since that connect
    // and ignore the backoff. Close it ourselves to suppress that and
    // reconnect manually with an up-to-date cursor instead.
    source.addEventListener("error", this.onConnectError);
  }

  // Waking up is a reason to stop waiting out the backoff: the network or
  // the machine is back. Only a disconnected bus (between attempts)
  // reconnects; one that is connecting or connected is left alone. Listening
  // only while anything is subscribed.
  private wake = (): void => {
    if (!this.isLocal && (this.paused || this.incompatible)) {
      this.resume();
      return;
    }
    if (this.incompatible || this.subscriptions.size === 0 || this.source || this.headPending) return;
    this.reconnectAttempt = 0;
    this.openConnection();
  };

  private onVisibilityChange = (): void => {
    if (document.visibilityState === "visible") this.wake();
  };

  private listenForWake(on: boolean): void {
    if (on === this.wakeListening) return;
    this.wakeListening = on;
    if (typeof window !== "undefined") {
      if (on) window.addEventListener("online", this.wake);
      else window.removeEventListener("online", this.wake);
    }
    if (typeof document !== "undefined") {
      if (on) document.addEventListener("visibilitychange", this.onVisibilityChange);
      else document.removeEventListener("visibilitychange", this.onVisibilityChange);
    }
  }

  teardown(): void {
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = undefined;
    this.reconnectAttempt = 0;
    this.source?.close();
    this.source = null;
    this.connectGen++;
    this.headPending = false;
    this.connectedTypes = new Set();
    this.consecutiveFailures = 0;
    this.clearColdFailing();
    this.failedOpens = 0;
    this.queue = [];
    this.draining = false;
    this.listenForWake(false);
  }

  /**
   * The server-side `types` filter is the union of every active
   * subscription's types. A subscribe that brings a type the open
   * connection doesn't carry reconnects at once (a deliberate widen: no
   * backoff) from the cursor. Unsubscribing never narrows the filter, to
   * avoid reconnect thrash as components mount/unmount — the connection only
   * tears down fully at zero subscribers. A subscribe never jumps the
   * reconnect backoff: while a retry is pending it joins that retry (only
   * coming back online or to the foreground cuts the wait short), and while
   * the first head read is pending it joins that connect.
   */
  add(sub: Subscription): void {
    this.subscriptions.add(sub);
    if (this.incompatible && this.isLocal) return;
    this.listenForWake(true);
    if (this.paused || this.incompatible) {
      // A peer bus waiting on resume() or its slow recheck.
      if (!this.reconnectTimer && !this.headPending) {
        if (this.incompatible) this.scheduleRecheck();
        else this.schedulePausedRecheck();
      }
      return;
    }
    if (this.source) {
      if (this.unionTypes().some((t) => !this.connectedTypes.has(t))) {
        this.reconnectAttempt = 0;
        this.openConnection(); // deliberate widen — no backoff, resumes from the cursor
      }
    } else if (!this.headPending && !this.reconnectTimer) {
      this.openConnection();
    }
    // Otherwise a connect is already on its way (the head read, or a
    // backoff retry) and reads the union when it opens.
  }

  remove(sub: Subscription): void {
    if (!this.subscriptions.delete(sub)) return;
    if (this.subscriptions.size === 0) {
      // The cursor and readiness are deliberately kept: the next subscriber
      // resumes from here.
      this.teardown();
      this.setHealth("connecting");
    }
  }
}

// --- The buses ----------------------------------------------------------------

const LOCAL = "";
const buses = new Map<string, Bus>();

// Which bus a subscription belongs on: a session that lives on a peer gets
// that session's own peer bus; everything else (including session-agnostic
// subscribers, and sessions whose host isn't known yet) is LOCAL.
function busKeyFor(sessionId: string | undefined): string {
  const peer = sessionId ? peerOfSession(sessionId) : undefined;
  return peer && sessionId ? `${peer}\u0000${sessionId}` : LOCAL;
}

function busFor(key: string): Bus {
  let bus = buses.get(key);
  if (!bus) {
    if (key === LOCAL) {
      bus = new Bus("", undefined);
    } else {
      const [peer, sessionId] = key.split("\u0000");
      bus = new Bus(peer, sessionId);
    }
    buses.set(key, bus);
  }
  return bus;
}

// The sessions store learns hosts after some subscribers already exist (a
// conversation mounts before the first scope=all list lands). Move those
// onto the bus their session's host calls for, and once that bus is ready
// tell them to re-seed: whatever they read over REST was ordered against
// the other log.
onSessionHostsChanged(() => {
  const moving: Subscription[] = [];
  for (const bus of buses.values()) {
    for (const sub of bus.subscriptions) if (busKeyFor(sub.sessionId) !== sub.bus) moving.push(sub);
  }
  for (const sub of moving) {
    buses.get(sub.bus)?.remove(sub);
    sub.bus = busKeyFor(sub.sessionId);
    const to = busFor(sub.bus);
    to.add(sub);
    const key = sub.bus;
    if (sub.onReseed) {
      void to.ready.then(() => {
        if (sub.bus === key && to.subscriptions.has(sub)) sub.onReseed?.();
      });
    }
  }
});

/**
 * Resolves once the bus for `sessionId` knows its cursor (its first
 * GET /events/head landed); already resolved after that, across reconnects
 * and teardown. Without a session id, or for a session on this device (or
 * whose host isn't known yet), that's this device's bus; for a session on a
 * peer, that session's peer bus.
 *
 * The bus never replays history, so a consumer that combines a REST snapshot
 * with live events must order them like this:
 *
 *   subscribeToEvents(...)       // 1. register first
 *   await eventsReady(sessionId) // 2. the cursor is fixed
 *   read the REST snapshot       // 3. served after the cursor was read
 *   merge                        // 4. live wins, or dedupe by seq
 *
 * Every event is then either at or below the cursor — committed before the
 * head read, so before the snapshot was served, so in it — or after the
 * cursor, which the connection delivers to the subscriber registered in 1.
 * Some events are in both (after the cursor and before the snapshot); the
 * merge in 4 absorbs that overlap. Reading the snapshot before the cursor is
 * fixed leaves a gap on a bus's first connect: an event committed between
 * the snapshot and the head read is in neither.
 *
 * A snapshot that is served from a live session child's memory rather than
 * the Data Service's log (pending approvals, the message queue) is not
 * ordered against the log the same way; see useSessionAgentState's
 * approvals seed for what that leaves.
 *
 * It only resolves once something has subscribed (the head is read on the
 * first connect), and stays pending while the head can't be read — the
 * consumer's REST read then waits with it, as its live updates do.
 */
export function eventsReady(sessionId?: string): Promise<void> {
  return busFor(busKeyFor(sessionId)).ready;
}

/**
 * eventsReady() — this device's bus only — for an action the user is
 * waiting on (a job's Start button; every caller is a host-local job):
 * rejects with EVENTS_NOT_READY_MESSAGE after `timeoutMs` instead of
 * leaving the button spinning while the services are down, and at once with
 * EVENTS_INCOMPATIBLE_MESSAGE when this device's bus is (or becomes)
 * "incompatible", which no amount of waiting fixes.
 */
export function eventsReadyWithin(timeoutMs: number = READY_TIMEOUT_MS): Promise<void> {
  if (health.state === "incompatible") return Promise.reject(new Error(EVENTS_INCOMPATIBLE_MESSAGE));
  let timer: ReturnType<typeof setTimeout> | undefined;
  let unlisten: (() => void) | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(EVENTS_NOT_READY_MESSAGE)), timeoutMs);
    unlisten = subscribeToBusHealth((h) => {
      if (h.state === "incompatible") reject(new Error(EVENTS_INCOMPATIBLE_MESSAGE));
    });
  });
  return Promise.race([eventsReady(), timeout]).finally(() => {
    clearTimeout(timer);
    unlisten?.();
  });
}

/**
 * Subscribes to the Router's /events SSE stream via a single window-wide
 * connection shared by every subscriber (or, for a session on a mesh peer,
 * that session's own peer stream). Returns an unsubscribe function; the
 * underlying EventSource closes once the last subscriber on its bus
 * detaches.
 *
 * Live-only (see THE CONTRACT above): the subscriber hears what happens from
 * now on, never history. A consumer that needs earlier state reads it over
 * REST after `await eventsReady(sessionId)`.
 */
export function subscribeToEvents(
  params: SubscribeParams,
  onEvent: (envelope: Envelope) => void,
): () => void {
  const sub: Subscription = {
    sessionId: params.sessionId,
    types: params.types,
    onEvent,
    onReseed: params.onReseed,
    bus: busKeyFor(params.sessionId),
  };
  busFor(sub.bus).add(sub);
  return () => buses.get(sub.bus)?.remove(sub);
}

/**
 * Whether `env` happened at or after `since` (a Date.now() value) — for the
 * notice-style side effects a subscriber should fire only for news.
 *
 * Folding a delivered event into state is always right (it converges on the
 * log); popping an error banner or staging a sticky "Done" is only right for
 * what happened while this window was open. Gate those on the window's
 * notice epoch, not the subscriber's own mount time:
 *
 *   const since = windowNoticesSince();          // when the effect subscribes
 *   ...if (happenedSince(env, since)) notify();
 *
 * The cursor outlives teardown, so a surface round trip (Sessions → Retro →
 * Sessions) resumes and delivers what happened while the surface was gone.
 * Those are news to the user — a session that finished during the visit
 * still owes its "Done · your turn" — so a gate keyed to the remount would
 * lose them. Notices are lost only across a reload or a new window, where
 * the cursor starts again at the head.
 *
 * By time, not by seq: a seq boundary would need the log's head at the
 * moment of subscribing (the cursor trails it), which is a request per
 * subscribe. The Data Service stamps envelopes with the same machine's clock
 * the window reads. An envelope without a readable stamp counts as news.
 */
export function happenedSince(env: Envelope, since: number): boolean {
  const at = Date.parse(env.timestamp);
  return !Number.isFinite(at) || at >= since;
}

/**
 * The window's notice epoch for happenedSince: when the window's first head
 * read was sent (which fixes the cursor), less NOTICE_SLACK_MS for
 * producer-stamped timestamps — the same value for the window's life after
 * that (surface round trips and remounts included). Not the first
 * notice-gated subscriber's mount: a window that opens on Retro fixes its
 * cursor there, and the rail mounting later is delivered everything after
 * that cursor, stamped before the rail mounted, which is still news. Called
 * before any head read was sent, it opens the window then instead. Reset
 * only by resetEventsBusForTests.
 */
export function windowNoticesSince(): number {
  if (noticesSince === undefined) noticesSince = Date.now() - NOTICE_SLACK_MS;
  return noticesSince;
}

/**
 * Test hook: forget everything, every bus's cursor and readiness included.
 * `primeCursor` sets the cursor (and readiness) of every bus as if a head
 * read had returned it, so a first subscribe opens synchronously
 * (since=<primeCursor>), which is what feature tests expect; undefined
 * exercises the head read.
 */
export function resetEventsBusForTests(primeCursor?: number): void {
  for (const bus of buses.values()) {
    bus.teardown();
    bus.subscriptions.clear();
  }
  buses.clear();
  primedCursor = primeCursor;
  noticesSince = undefined;
  incompatiblePeers.clear();
  health = { state: "connecting" };
}

/** Test read of a bus's cursor (this device's, or `sessionId`'s). */
export function getEventsCursorForTests(sessionId?: string): number | undefined {
  return buses.get(busKeyFor(sessionId))?.cursor;
}
