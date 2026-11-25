import { ROUTER_BASE } from "./client";
import type { SessionHost } from "./types";

/**
 * Where each listed session lives in mesh mode (docs/MESH-MODE-DESIGN.md
 * D4). Its own module so the event bus (which routes a remote session's
 * subscriptions to its host) and the mesh store (which fills it in) don't
 * import each other.
 */

/** The base URL a peer's Router API is reached at, through this Router. */
export function peerBase(peerName: string): string {
  return `${ROUTER_BASE}/peers/${encodeURIComponent(peerName)}`;
}

// Filled from GET /sessions?scope=all rows, read by the event bus to send a
// session-scoped subscription to that session's host.
const sessionHosts = new Map<string, string>();
const hostListeners = new Set<() => void>();

/** Records where each listed session lives; called by the sessions store. */
export function recordSessionHosts(sessions: { id: string; host?: SessionHost }[]): void {
  let changed = false;
  for (const s of sessions) {
    const peer = s.host && !s.host.self ? s.host.name : undefined;
    if (peer === undefined) {
      if (sessionHosts.delete(s.id)) changed = true;
    } else if (sessionHosts.get(s.id) !== peer) {
      sessionHosts.set(s.id, peer);
      changed = true;
    }
  }
  if (changed) for (const l of hostListeners) l();
}

/** The peer a session lives on, or undefined for this device (or unknown). */
export function peerOfSession(sessionId: string): string | undefined {
  return sessionHosts.get(sessionId);
}

/** Called whenever recordSessionHosts changes the map. */
export function onSessionHostsChanged(listener: () => void): () => void {
  hostListeners.add(listener);
  return () => {
    hostListeners.delete(listener);
  };
}


/** Clears the map only: the event bus's listener is registered once, at import. */
export function clearSessionHostsForTests(): void {
  sessionHosts.clear();
}
