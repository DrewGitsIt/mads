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

// A full session's event history rarely exceeds a few hundred envelopes —
// generous margin, not a tuning knob. SSE has no app-level backpressure; if
// the dispatch queue would ever exceed this, the durable log + cursor is the
// recovery mechanism, not an in-memory buffer — drop the connection and
// resume from resumeSince() rather than buffer unboundedly.
const MAX_QUEUE = 500;
const BACKOFF_BASE_MS = 1000;
const BACKOFF_MAX_MS = 30000;

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
let source: EventSource | null = null;
const subscriptions = new Set<Subscription>();
// Per type, not one scalar: a single "highest seq ever delivered" cursor
// conflates "seen for type A" with "seen for type B." If a subscriber
// widens the union to a type this connection has never carried, and some
// other already-covered type's traffic had already pushed a scalar cursor
// past an earlier, never-delivered event of the new type, that event would
// look already-seen and be skipped forever — the resumed connection's
// `since` would start past it, and even if the server resent it anyway,
// the scalar dedupe below would still drop it as "already delivered."
let lastDeliveredSeqByType = new Map<string, number>();
let reconnectAttempt = 0;
// Types the server refused to filter on — dropped from the connection's
// union for the rest of this bus's life (see probeRejectedTypes). A refused
// type fails the WHOLE shared stream, not just its own subscriber, so it
// has to go for everything else to keep flowing.
let rejectedTypes = new Set<string>();
// Consecutive failed connects with no "open" in between. Feeds BusHealth.
let consecutiveFailures = 0;
let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
let queue: Envelope[] = [];
let draining = false;

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
  state: "live" | "connecting" | "failing";
  /** Types dropped from the stream because the server refused them. */
  ignoredTypes: string[];
}

// Two misses in a row: a single drop is ordinary (a Router restart, a sleep/
// wake) and reconnects within a second; only a second one is worth a word.
const FAILING_AFTER = 2;

const healthListeners = new Set<(health: BusHealth) => void>();
let health: BusHealth = { state: "connecting", ignoredTypes: [] };

function setHealth(state: BusHealth["state"]): void {
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

function buildUrl(types: string[], since: number): string {
  // ROUTER_BASE is "" for same-origin builds (e.g. served by the Router
  // itself) — the URL constructor requires an absolute base, so fall back
  // to the page's own origin in that case.
  const url = new URL("/events", ROUTER_BASE || window.location.origin);
  url.searchParams.set("types", types.join(","));
  if (since > 0) url.searchParams.set("since", String(since));
  return url.toString();
}

// The resume point for a (re)connection: the minimum high-water-mark across
// every currently-subscribed type, defaulting an as-yet-unseen type to 0.
// Taking the min (not the max, and not the old single scalar) guarantees
// that widening to a type this connection has never carried resumes from
// 0 for that type's history — replayed envelopes of already-covered types
// are simply deduped below, not lost.
function resumeSince(): number {
  let since = Infinity;
  for (const type of unionTypes()) {
    since = Math.min(since, lastDeliveredSeqByType.get(type) ?? 0);
  }
  return Number.isFinite(since) ? since : 0;
}

function dispatchOne(env: Envelope): void {
  // Belt-and-suspenders: the Go log's Since is documented "strictly greater
  // than" already, so server-side overlap on resume shouldn't happen — but
  // this dedupe is what makes the exactly-once guarantee independent of
  // that implementation detail. Per-type, for the same reason resumeSince
  // resumes per-type: a scalar cursor would let one type's traffic mask
  // another type's still-undelivered history.
  const lastForType = lastDeliveredSeqByType.get(env.type) ?? 0;
  if (env.seq <= lastForType) return;
  lastDeliveredSeqByType.set(env.type, env.seq);
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
    // durable log at our last *dispatched* seq. Nothing already delivered
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
  const attempted = unionTypes();
  scheduleReconnect();
  void probeRejectedTypes(attempted).then((refused) => {
    const fresh = refused.filter((t) => !rejectedTypes.has(t));
    if (fresh.length === 0 || subscriptions.size === 0) return;
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
  const delay = Math.random() * Math.min(BACKOFF_MAX_MS, BACKOFF_BASE_MS * 2 ** reconnectAttempt);
  reconnectAttempt++;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = undefined;
    if (subscriptions.size > 0) openConnection();
  }, delay);
}

function openConnection(): void {
  source?.close();
  const types = unionTypes();
  source = new EventSource(buildUrl(types, resumeSince()));
  for (const type of types) source.addEventListener(type, handleFrame as EventListener);
  source.addEventListener("open", () => {
    consecutiveFailures = 0;
    setHealth("live");
  });
  // A native EventSource auto-reconnects to the exact same URL (same
  // original `since`, or none), which would silently replay a huge or
  // unbounded amount of history. Close it ourselves to suppress that and
  // reconnect manually with an up-to-date cursor instead.
  source.addEventListener("error", onConnectError);
}

/**
 * Subscribes to the Router's /events SSE stream via a single window-wide
 * connection shared by every subscriber. Returns an unsubscribe function;
 * the underlying EventSource closes once the last subscriber detaches.
 *
 * The server-side `types` filter is the union of every active subscription's
 * types, recomputed on each subscribe/unsubscribe. Widening the union (a new
 * type not already covered) closes the current connection and opens a new
 * one immediately, resuming from `since=resumeSince()` — the minimum
 * per-type high-water-mark across the union — so nothing is lost. Unsubscribing never narrows the filter, to avoid
 * reconnect thrash as components mount/unmount — the connection only tears
 * down fully at zero subscribers.
 */
export function subscribeToEvents(
  params: SubscribeParams,
  onEvent: (envelope: Envelope) => void,
): () => void {
  const sub: Subscription = { sessionId: params.sessionId, types: params.types, onEvent };
  const before = unionTypes();
  subscriptions.add(sub);
  const widened = unionTypes().some((t) => !before.includes(t));

  if (!source) {
    openConnection();
  } else if (widened) {
    if (reconnectTimer) clearTimeout(reconnectTimer);
    reconnectTimer = undefined;
    reconnectAttempt = 0;
    openConnection(); // deliberate widen — no backoff, resumes from resumeSince()
  }

  return () => {
    subscriptions.delete(sub);
    if (subscriptions.size === 0) {
      if (reconnectTimer) clearTimeout(reconnectTimer);
      reconnectTimer = undefined;
      reconnectAttempt = 0;
      source?.close();
      source = null;
      lastDeliveredSeqByType = new Map();
      rejectedTypes = new Set();
      consecutiveFailures = 0;
      setHealth("connecting");
      queue = [];
      draining = false;
    }
  };
}
