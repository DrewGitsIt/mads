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
let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
let queue: Envelope[] = [];
let draining = false;

function unionTypes(): string[] {
  const set = new Set<string>();
  for (const sub of subscriptions) for (const t of sub.types) set.add(t);
  return [...set];
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
  // A native EventSource auto-reconnects to the exact same URL (same
  // original `since`, or none), which would silently replay a huge or
  // unbounded amount of history. Close it ourselves to suppress that and
  // reconnect manually with an up-to-date cursor instead.
  source.addEventListener("error", scheduleReconnect);
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
      queue = [];
      draining = false;
    }
  };
}
