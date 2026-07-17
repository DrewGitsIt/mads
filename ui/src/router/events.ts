import { ROUTER_BASE } from "./client";
import type { Envelope } from "./types";

export interface SubscribeParams {
  since?: number;
  sessionId?: string;
  types?: string[];
}

interface PooledConnection {
  source: EventSource;
  listeners: Set<(envelope: Envelope) => void>;
}

// Callers with byte-identical params (same types + sessionId + since) share
// one EventSource keyed by the resulting URL, refcounted by listener count —
// several independent hooks (the rail's task counts, the command palette's
// task list, a session workspace's pipeline-running dot) legitimately
// subscribe to the exact same filter set at the same time, and each opening
// its own connection burns through the browser's ~6-per-origin HTTP/1.1
// socket cap, starving ordinary fetches (e.g. GET /tasks hangs forever
// queued behind a fistful of identical, otherwise-redundant SSE streams).
const pool = new Map<string, PooledConnection>();

/**
 * Subscribes to the Router's /events SSE stream. Returns an unsubscribe
 * function that detaches this listener, closing the underlying EventSource
 * once no listener needs it anymore.
 *
 * The Router always names each frame's `event:` field after the envelope's
 * type (e.g. "session.created"), never a bare "message" — so callers must
 * pass `types` to receive anything; it both narrows the server-side stream
 * and tells this wrapper which named listeners to attach.
 */
export function subscribeToEvents(
  params: SubscribeParams,
  onEvent: (envelope: Envelope) => void,
): () => void {
  // ROUTER_BASE is "" for same-origin builds (e.g. served by the Router
  // itself) — the URL constructor requires an absolute base, so fall back
  // to the page's own origin in that case.
  const url = new URL("/events", ROUTER_BASE || window.location.origin);
  if (params.since !== undefined) url.searchParams.set("since", String(params.since));
  if (params.sessionId) url.searchParams.set("session_id", params.sessionId);
  if (params.types && params.types.length > 0) {
    url.searchParams.set("types", params.types.join(","));
  }
  const key = url.toString();

  let pooled = pool.get(key);
  if (!pooled) {
    const source = new EventSource(url);
    const listeners = new Set<(envelope: Envelope) => void>();
    const handleMessage = (e: MessageEvent<string>) => {
      const envelope = JSON.parse(e.data) as Envelope;
      for (const listener of listeners) listener(envelope);
    };
    for (const type of params.types ?? []) {
      source.addEventListener(type, handleMessage as EventListener);
    }
    pooled = { source, listeners };
    pool.set(key, pooled);
  }
  pooled.listeners.add(onEvent);

  return () => {
    pooled!.listeners.delete(onEvent);
    if (pooled!.listeners.size === 0) {
      pooled!.source.close();
      pool.delete(key);
    }
  };
}
