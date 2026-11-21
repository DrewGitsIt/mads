import { ROUTER_BASE } from "./client";
import type { Envelope } from "./types";

export interface SubscribeParams {
  sessionId?: string;
  types: string[];
}

interface Subscription {
  sessionId?: string;
  types: string[];
  onEvent: (envelope: Envelope) => void;
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
const BACKOFF_MAX_MS = 30000;
// How long a job action (useRetroGeneration, useAgentGeneration,
// useRegistryJob) waits for the bus before giving up; see eventsReadyWithin.
const READY_TIMEOUT_MS = 10_000;

// One EventSource per window, shared by every control-plane subscriber —
// several independent hooks (the rail's task counts, a session workspace's
// pipeline-running dot, the Tasks tab's live task list) legitimately want
// different, overlapping type sets at the same time, and each opening its
// own connection burns through the browser's ~6-per-origin HTTP/1.1 socket
// cap, starving ordinary fetches (e.g. a task Save PUT hangs forever queued
// behind a fistful of otherwise-redundant SSE streams). Server-side
// filtering here is by `types` only, never `session_id`: in practice at
// least one active subscriber is session-agnostic whenever the rail is
// mounted, so a server-side session filter buys nothing and would force a
// reconnect on every session switch. Session scoping is a client-side
// concern instead — see dispatchOne's `sub.sessionId` check.
//
// THE CONTRACT: the bus is live-only. It never replays history; a consumer
// that needs state from before it subscribed reads it over REST, behind the
// ready barrier (eventsReady, below).
//
// - One scalar cursor: the highest seq delivered on this window's bus. Every
//   connection is opened with since=<cursor>, and anything at or below it is
//   dropped as already delivered (the log is one seq-ordered stream, resumed
//   strictly after the cursor).
// - The first connect reads GET /events/head and starts the cursor there, so
//   it never starts from the beginning of the log. since is omitted only
//   when the log was empty (head 0) — which means the same thing.
// - Widening (a subscriber brings a type the open connection doesn't carry),
//   a drop, and a queue overflow all reconnect with since=<cursor>. A newly
//   carried type gets only its events after the cursor — never its history.
//   (The cursor trails the head while the carried types are quiet, so that
//   can include the new type's events from the last few moments: older than
//   the subscribe, but all newer than anything this window was sent.)
// - At zero subscribers the connection closes but the cursor and readiness
//   stay, so a surface round trip (Sessions → Retro → Sessions) resumes from
//   where it left off rather than replaying or skipping.
let source: EventSource | null = null;
// The type set `source` was opened with — what the open connection actually
// carries. A widen is judged against this, not against the current
// subscribers: React runs an old effect's cleanup before the new effect, so
// on a session switch the subscriber union briefly shrinks (no reconnect —
// removing never narrows) and the new session re-adding the same types must
// not look like a widen.
let connectedTypes = new Set<string>();
const subscriptions = new Set<Subscription>();
// undefined until the first head read lands; module-level, so it outlives
// teardown at zero subscribers.
let cursor: number | undefined;
// Bumped by every connect attempt and teardown, so a head read that lands
// after either can't open a connection nobody asked for.
let connectGen = 0;
// A connect is waiting on GET /events/head (the first one, or a recheck after
// an error). It reads the subscriber union when the head lands, so
// subscribes in the meantime fold into it.
let headPending = false;
// Set by a failed connection: the next connect re-reads the head first, in
// case the Data Service's log was reset (wiped, or swapped for another)
// while this window stayed open — a cursor above the new log's end would
// otherwise skip everything until the new log caught up with it.
let recheckHead = false;
let warnedLogReset = false;
// Bumped at teardown: a probe that answers for an earlier life of the bus
// (before the last subscriber left) is ignored.
let lifeGen = 0;
let reconnectAttempt = 0;
// Types the server refused to filter on — dropped from the connection's
// union for the rest of this bus's life (see probeRejectedTypes). A refused
// type fails the WHOLE shared stream, not just its own subscriber, so it
// has to go for everything else to keep flowing.
let rejectedTypes = new Set<string>();
// Consecutive failed connects with no "open" in between. Feeds BusHealth.
let consecutiveFailures = 0;
// Terminal: GET /events/head answered 404/405, so the Router or the Data
// Service predates this UI (it has no head to start a live-only stream
// from). Nothing connects again until a reload; see enterIncompatible.
let incompatible = false;
// The first head read failed and the window is still waiting for its first
// cursor: logged once on the way in, once on the way out.
let waitingLogged = false;
let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
let queue: Envelope[] = [];
let draining = false;

// The ready barrier: resolves once the cursor is known (the first head read
// landed) and stays resolved for the window's life — reconnects, widens and
// teardown never un-ready it.
let resolveReady: (() => void) | undefined;
let ready = newReady();

function newReady(): Promise<void> {
  return new Promise<void>((resolve) => {
    resolveReady = resolve;
  });
}

function markReady(): void {
  resolveReady?.();
  resolveReady = undefined;
}

/**
 * Resolves once the bus knows its cursor (the first GET /events/head
 * landed); already resolved after that, across reconnects and teardown.
 *
 * The bus never replays history, so a consumer that combines a REST snapshot
 * with live events must order them like this:
 *
 *   subscribeToEvents(...)   // 1. register first
 *   await eventsReady()      // 2. the cursor is fixed
 *   read the REST snapshot   // 3. served after the cursor was read
 *   merge                    // 4. live wins, or dedupe by seq
 *
 * Every event is then either at or below the cursor — committed before the
 * head read, so before the snapshot was served, so in it — or after the
 * cursor, which the connection delivers to the subscriber registered in 1.
 * Some events are in both (after the cursor and before the snapshot); the
 * merge in 4 absorbs that overlap. Reading the snapshot before the cursor is
 * fixed leaves a gap on a window's first connect: an event committed between
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
export function eventsReady(): Promise<void> {
  return ready;
}

/** The message a job action shows when the bus isn't ready in time. */
export const EVENTS_NOT_READY_MESSAGE = "Live updates aren't connected yet. Try again in a moment.";

/**
 * eventsReady, for an action the user is waiting on (a job's Start button):
 * rejects with EVENTS_NOT_READY_MESSAGE after `timeoutMs` instead of
 * leaving the button spinning while the services are down.
 */
export function eventsReadyWithin(timeoutMs: number = READY_TIMEOUT_MS): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(EVENTS_NOT_READY_MESSAGE)), timeoutMs);
  });
  return Promise.race([ready, timeout]).finally(() => clearTimeout(timer));
}

function unionTypes(): string[] {
  const set = new Set<string>();
  for (const sub of subscriptions) for (const t of sub.types) if (!rejectedTypes.has(t)) set.add(t);
  return [...set];
}

/**
 * Whether live updates are reaching this window. "failing" means the stream
 * has failed to connect repeatedly — every live view (the session rail, task
 * counts, the Tasks tab's run state) is showing whatever it last fetched and
 * nothing will correct it until the stream comes back. Surfaces show that
 * rather than let stale state pass for current: a frozen rail reading
 * "Sleeping" beside a conversation that is plainly awake is how the last
 * outage of this kind (an unregistered type failing the whole stream) was
 * first noticed.
 */
export interface BusHealth {
  /**
   * "incompatible" is terminal: the services are older than this UI (no
   * GET /events/head), so the bus never connects and eventsReady never
   * resolves. Only a restart of mads and a reload get out of it.
   */
  state: "live" | "connecting" | "failing" | "incompatible";
  /** Types dropped from the stream because the server refused them. */
  ignoredTypes: string[];
}

// Two misses in a row: a single drop is ordinary (a Router restart, a sleep/
// wake) and reconnects within a second; only a second one is worth a word.
const FAILING_AFTER = 2;

const healthListeners = new Set<(health: BusHealth) => void>();
let health: BusHealth = { state: "connecting", ignoredTypes: [] };

function setHealth(state: BusHealth["state"]): void {
  if (incompatible && state !== "incompatible") return; // terminal
  const ignoredTypes = [...rejectedTypes];
  if (state === health.state && ignoredTypes.length === health.ignoredTypes.length) return;
  health = { state, ignoredTypes };
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

// The server's refusal names the type: `"x.y" is neither an event type …`.
const REJECTED_TYPE = /^"([^"]+)" is neither/;

/**
 * Finds out which of `types` the server refuses, after the stream failed.
 * EventSource hides the HTTP status and body of a failed connect, so this
 * asks the same question over the batch path (same route, same filter
 * parser) with a cursor past the end, which costs nothing when the filter is
 * fine and returns the validation errors when it isn't.
 *
 * Why this exists: the UI is served live from ui/dist and can ship ahead of
 * a long-running Data Service. A new subscriber type the running build
 * hadn't registered (session.queue_changed, 2026-09-30) got the stream
 * refused, and the reconnect loop below retried the identical URL forever —
 * silently freezing every live view in the window, because the union only
 * ever widens. Newer Data Services accept unknown well-formed types (they
 * just match nothing); this covers the older ones, and anything else a
 * server may refuse. Best-effort: any failure here leaves the ordinary
 * backoff in charge.
 */
async function probeRejectedTypes(types: string[]): Promise<string[]> {
  if (types.length === 0 || typeof fetch !== "function") return [];
  try {
    const url = new URL(buildUrl(types, Number.MAX_SAFE_INTEGER));
    const res = await fetch(url.toString(), { headers: { Accept: "application/json" } });
    if (res.ok) return [];
    const body = (await res.json()) as { errors?: { field?: string; message?: string }[] };
    const refused: string[] = [];
    for (const e of body.errors ?? []) {
      if (e.field !== "types") continue;
      const m = REJECTED_TYPE.exec(e.message ?? "");
      if (m && types.includes(m[1])) refused.push(m[1]);
    }
    return refused;
  } catch {
    return [];
  }
}

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/** GET /events/head is missing: the services predate this UI. */
class IncompatibleServicesError extends Error {}

async function fetchHead(): Promise<number> {
  // A head read that hangs would hold every connect (and the ready barrier)
  // forever: give up after HEAD_TIMEOUT_MS, which counts as a failed connect.
  const abort = new AbortController();
  const timer = setTimeout(() => abort.abort(), HEAD_TIMEOUT_MS);
  try {
    const res = await fetch(new URL("/events/head", ROUTER_BASE || window.location.origin).toString(), {
      headers: { Accept: "application/json" },
      signal: abort.signal,
    });
    if (res.status === 404 || res.status === 405) {
      throw new IncompatibleServicesError(`GET /events/head: ${res.status}`);
    }
    if (!res.ok) throw new Error(`GET /events/head: ${res.status}`);
    const body = (await res.json()) as { seq?: unknown };
    if (typeof body.seq !== "number" || !Number.isFinite(body.seq) || body.seq < 0) {
      throw new Error("GET /events/head: no seq");
    }
    return body.seq;
  } finally {
    clearTimeout(timer);
  }
}

function buildUrl(types: string[], since: number): string {
  // ROUTER_BASE is "" for same-origin builds (e.g. served by the Router
  // itself) — the URL constructor requires an absolute base, so fall back
  // to the page's own origin in that case.
  const url = new URL("/events", ROUTER_BASE || window.location.origin);
  url.searchParams.set("types", types.join(","));
  if (since > 0) url.searchParams.set("since", String(since));
  return url.toString();
}

function dispatchOne(env: Envelope): void {
  // Belt-and-suspenders: the Go log's Since is documented "strictly greater
  // than" already, so server-side overlap on resume shouldn't happen — but
  // this dedupe is what makes the exactly-once guarantee independent of
  // that implementation detail. Scalar, like the cursor: the stream is one
  // seq-ordered log whatever the types, and a newly carried type is resumed
  // from the same cursor, so nothing below it is still owed to anyone.
  if (env.seq <= (cursor ?? 0)) return;
  cursor = env.seq;
  reconnectAttempt = 0; // live data flowing — connection is healthy
  consecutiveFailures = 0;
  setHealth("live");
  for (const sub of subscriptions) {
    if (sub.sessionId && env.session_id !== sub.sessionId) continue;
    if (!sub.types.includes(env.type)) continue;
    sub.onEvent(env);
  }
}

function handleFrame(e: MessageEvent<string>): void {
  queue.push(JSON.parse(e.data) as Envelope);
  if (queue.length > MAX_QUEUE) {
    // Never buffer unboundedly — drop what's queued and resume from the
    // durable log at the cursor (our last *dispatched* seq). Nothing already delivered
    // is redelivered; nothing still queued is lost, only re-fetched.
    queue = [];
    draining = false;
    scheduleReconnect();
    return;
  }
  // A subscriber's onEvent can itself trigger more frames reentrantly (e.g.
  // a burst arriving while one is still being handled). Only the outermost
  // call drains — a reentrant call just enqueues and returns — so the queue
  // actually reflects backlog depth instead of every push draining itself
  // one at a time, which would make MAX_QUEUE unreachable.
  if (draining) return;
  draining = true;
  while (queue.length > 0) dispatchOne(queue.shift()!);
  draining = false;
}

function onConnectError(): void {
  consecutiveFailures++;
  if (consecutiveFailures >= FAILING_AFTER) setHealth("failing");
  recheckHead = true;
  const attempted = unionTypes();
  const life = lifeGen;
  scheduleReconnect();
  void probeRejectedTypes(attempted).then((refused) => {
    const fresh = refused.filter((t) => !rejectedTypes.has(t));
    if (fresh.length === 0 || subscriptions.size === 0 || life !== lifeGen) return;
    for (const t of fresh) rejectedTypes.add(t);
    console.warn(
      `events: the server refused ${fresh.join(", ")}; live updates continue without ${fresh.length === 1 ? "it" : "them"}`,
    );
    setHealth(health.state);
    // Don't sit out the backoff: the cause is known and removed.
    if (reconnectTimer) clearTimeout(reconnectTimer);
    reconnectTimer = undefined;
    openConnection();
  });
}

function scheduleReconnect(): void {
  source?.close();
  source = null;
  if (reconnectTimer) clearTimeout(reconnectTimer);
  const delay = Math.random() * Math.min(BACKOFF_MAX_MS, BACKOFF_BASE_MS * 2 ** reconnectAttempt);
  reconnectAttempt++;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = undefined;
    if (subscriptions.size > 0) openConnection();
  }, delay);
}

function openConnection(): void {
  if (incompatible) return;
  source?.close();
  source = null;
  if (reconnectTimer) clearTimeout(reconnectTimer);
  reconnectTimer = undefined;
  // Whatever the old connection queued is redelivered by the new one (it
  // resumes from the cursor, which only dispatching advances). Dispatching
  // it anyway could push the cursor past events of a type the new
  // connection adds, and they would then be dropped as already seen.
  queue = [];
  const gen = ++connectGen;
  if (cursor !== undefined && !recheckHead) {
    openSource();
    return;
  }
  // First connect on this window: learn where the log ends and start there.
  // After a failed connection: check the log still reaches the cursor.
  // A failed read is a failed connect — same backoff, same health.
  headPending = true;
  fetchHead().then(
    (head) => {
      if (gen !== connectGen) return;
      headPending = false;
      recheckHead = false;
      if (cursor === undefined) {
        cursor = head;
        markReady();
        if (waitingLogged) {
          waitingLogged = false;
          console.info("events: reached the event log; live updates connected");
        }
      } else if (head < cursor) {
        // The log is behind us: it was reset. Nothing past its end exists,
        // so resume from its end rather than wait for it to pass the old one.
        if (!warnedLogReset) {
          warnedLogReset = true;
          console.warn(`events: the event log restarted below this window's cursor (${head} < ${cursor}); resuming from ${head}`);
        }
        cursor = head;
      }
      if (subscriptions.size > 0) openSource();
    },
    (err: unknown) => {
      if (gen !== connectGen) return;
      headPending = false;
      if (err instanceof IncompatibleServicesError) {
        enterIncompatible(err.message);
        return;
      }
      consecutiveFailures++;
      // Before the first cursor every view is waiting on the barrier, so
      // say so at once rather than after FAILING_AFTER misses.
      if (cursor === undefined) {
        setHealth("failing");
        if (!waitingLogged) {
          waitingLogged = true;
          console.error(`events: can't read the event log's head (${errorText(err)}); retrying — views wait until it answers`);
        }
      } else if (consecutiveFailures >= FAILING_AFTER) {
        setHealth("failing");
      }
      scheduleReconnect();
    },
  );
}

/**
 * The Router or the Data Service is older than this UI: it has no
 * GET /events/head, so there is no live-only start point to connect from.
 * Retrying can't fix that and connecting without a head would bring back
 * the replay this bus no longer handles, so the bus stops for good: no
 * reconnects, the ready barrier stays shut, and health says "incompatible"
 * so the shell explains why every view is waiting (App.tsx's banner).
 */
function enterIncompatible(detail: string): void {
  incompatible = true;
  if (reconnectTimer) clearTimeout(reconnectTimer);
  reconnectTimer = undefined;
  source?.close();
  source = null;
  listenForWake(false);
  console.error(
    `events: ${detail} — the mads services are older than this window. Restart mads (scripts/app.sh --restart) and reload.`,
  );
  setHealth("incompatible");
}

function openSource(): void {
  const types = unionTypes();
  connectedTypes = new Set(types);
  source = new EventSource(buildUrl(types, cursor ?? 0));
  for (const type of types) source.addEventListener(type, handleFrame as EventListener);
  source.addEventListener("open", () => {
    consecutiveFailures = 0;
    setHealth("live");
  });
  // A native EventSource auto-reconnects to the exact same URL (the same
  // original `since`), which would redeliver everything since that connect
  // and ignore the backoff. Close it ourselves to suppress that and
  // reconnect manually with an up-to-date cursor instead.
  source.addEventListener("error", onConnectError);
}

// Waking up is a reason to stop waiting out the backoff: the network or the
// machine is back, and a timer that grew to 30 s while it was gone would
// otherwise keep every live view stale for that long. Only a disconnected
// bus (between attempts) reconnects; one that is connecting or connected is
// left alone. Listening only while anything is subscribed.
function wake(): void {
  if (incompatible || subscriptions.size === 0 || source || headPending) return;
  reconnectAttempt = 0;
  openConnection();
}

function onVisibilityChange(): void {
  if (document.visibilityState === "visible") wake();
}

let wakeListening = false;

function listenForWake(on: boolean): void {
  if (on === wakeListening) return;
  wakeListening = on;
  if (typeof window !== "undefined") {
    if (on) window.addEventListener("online", wake);
    else window.removeEventListener("online", wake);
  }
  if (typeof document !== "undefined") {
    if (on) document.addEventListener("visibilitychange", onVisibilityChange);
    else document.removeEventListener("visibilitychange", onVisibilityChange);
  }
}

function teardown(): void {
  if (reconnectTimer) clearTimeout(reconnectTimer);
  reconnectTimer = undefined;
  reconnectAttempt = 0;
  source?.close();
  source = null;
  connectGen++;
  lifeGen++;
  headPending = false;
  connectedTypes = new Set();
  rejectedTypes = new Set();
  consecutiveFailures = 0;
  queue = [];
  draining = false;
  listenForWake(false);
}

/**
 * Subscribes to the Router's /events SSE stream via a single window-wide
 * connection shared by every subscriber. Returns an unsubscribe function;
 * the underlying EventSource closes once the last subscriber detaches.
 *
 * Live-only (see THE CONTRACT above): the subscriber hears what happens from
 * now on, never history. A consumer that needs earlier state reads it over
 * REST after `await eventsReady()`.
 *
 * The server-side `types` filter is the union of every active subscription's
 * types. A subscribe that brings a type the open connection doesn't carry
 * reconnects at once (a deliberate widen: no backoff) from the cursor.
 * Unsubscribing never narrows the filter, to avoid reconnect thrash as
 * components mount/unmount — the connection only tears down fully at zero
 * subscribers. A subscribe never jumps the reconnect backoff: while a
 * retry is pending it joins that retry (only coming back online or to the
 * foreground cuts the wait short), and while the first head read is pending
 * it joins that connect.
 */
export function subscribeToEvents(
  params: SubscribeParams,
  onEvent: (envelope: Envelope) => void,
): () => void {
  const sub: Subscription = { sessionId: params.sessionId, types: params.types, onEvent };
  subscriptions.add(sub);
  if (incompatible) {
    return () => {
      subscriptions.delete(sub);
    };
  }
  listenForWake(true);

  if (source) {
    if (unionTypes().some((t) => !connectedTypes.has(t))) {
      reconnectAttempt = 0;
      openConnection(); // deliberate widen — no backoff, resumes from the cursor
    }
  } else if (!headPending && !reconnectTimer) {
    openConnection();
  }
  // Otherwise a connect is already on its way (the head read, or a backoff
  // retry) and reads the union when it opens.

  return () => {
    if (!subscriptions.delete(sub)) return;
    if (subscriptions.size === 0) {
      // The cursor and readiness are deliberately kept: the next subscriber
      // resumes from here.
      teardown();
      setHealth("connecting");
    }
  };
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

// When the first notice-gated subscriber in this window subscribed; see
// windowNoticesSince. Never reset in production: it lives as long as the
// window (and the cursor) does.
let noticesSince: number | undefined;

/**
 * The window's notice epoch for happenedSince: set by the first call, the
 * same value for the window's life after that (surface round trips and
 * remounts included). Reset only by resetEventsBusForTests.
 */
export function windowNoticesSince(): number {
  if (noticesSince === undefined) noticesSince = Date.now();
  return noticesSince;
}

/**
 * Test hook: forget everything, the cursor and readiness included.
 * `primeCursor` sets the cursor (and readiness) as if a head read had
 * returned it, so a first subscribe opens synchronously — since omitted at
 * 0 — which is what feature tests expect; undefined exercises the head read.
 */
export function resetEventsBusForTests(primeCursor?: number): void {
  teardown();
  subscriptions.clear();
  cursor = primeCursor;
  noticesSince = undefined;
  recheckHead = false;
  warnedLogReset = false;
  incompatible = false;
  waitingLogged = false;
  ready = newReady();
  if (primeCursor !== undefined) markReady();
  health = { state: "connecting", ignoredTypes: [] };
}

/** Test read of the bus cursor. */
export function getEventsCursorForTests(): number | undefined {
  return cursor;
}
