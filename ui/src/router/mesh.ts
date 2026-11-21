import { useSyncExternalStore } from "react";
import { FetchRouterClient, ROUTER_BASE, router, type RouterClient } from "./client";
import {
  eventsReady,
  getIncompatiblePeers,
  getSessionLive,
  resumePeerBuses,
  subscribeToEvents,
  subscribeToIncompatiblePeers,
  subscribeToSessionLive,
  type SessionLive,
} from "./events";
import { clearSessionHostsForTests, peerBase } from "./sessionHosts";
import type { Envelope, PeerStatus, SessionHost } from "./types";

/**
 * Mesh mode, renderer side (docs/MESH-MODE-DESIGN.md D3–D5): the devices
 * this window views, whether the session list shows This device or All
 * devices, and a RouterClient per peer for the per-host reads a remote
 * session's conversation needs (D3b).
 *
 * The Router does the routing: a remote session's own /sessions/{id}/...
 * calls go through the ordinary `router` client and land on its host. Only
 * reads with no session id in the path (models, providers, agents) need
 * `clientForHost`.
 */

// --- Peer API (the Router's /peers, see services/router/README.md) --------

async function meshFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${ROUTER_BASE}${path}`, {
    ...init,
    headers: init?.body ? { "Content-Type": "application/json" } : undefined,
  });
  if (res.status === 204) return undefined as T;
  const body = (await res.json().catch(() => ({}))) as { error?: string };
  if (!res.ok) throw new Error(body.error || `${init?.method ?? "GET"} ${path}: ${res.status}`);
  return body as T;
}

export const meshApi = {
  listPeers: () => meshFetch<PeerStatus[]>("/peers"),
  addPeer: (req: { dns_name?: string; mesh_url?: string; name?: string }) =>
    meshFetch<PeerStatus>("/peers", { method: "POST", body: JSON.stringify(req) }),
  updatePeer: (name: string, patch: { name?: string; hidden?: boolean }) =>
    meshFetch<PeerStatus>(`/peers/${encodeURIComponent(name)}`, { method: "PUT", body: JSON.stringify(patch) }),
  removePeer: (name: string) => meshFetch<void>(`/peers/${encodeURIComponent(name)}`, { method: "DELETE" }),
};

// --- Per-host clients -----------------------------------------------------

const peerClients = new Map<string, RouterClient>();


/** The client for a session's host: the ordinary one for this device. */
export function clientForHost(host: SessionHost | undefined): RouterClient {
  if (!host || host.self) return router;
  let client = peerClients.get(host.name);
  if (!client) {
    client = new FetchRouterClient(peerBase(host.name));
    peerClients.set(host.name, client);
  }
  return client;
}

/** Cache key for per-host in-flight reads: "" for this device. */
export function hostKey(host: SessionHost | undefined): string {
  return !host || host.self ? "" : host.name;
}

// --- Scope and peer list (one store per window) -----------------------------

/**
 * What the session list shows: this device, every device, or one other
 * device (`peer:<name>`). One device is the merged list filtered by host.
 */
export type MeshScope = "self" | "all" | `peer:${string}`;

export interface MeshSnapshot {
  peers: PeerStatus[];
  /** What the session list shows. Only "all" with at least one peer changes anything. */
  scope: MeshScope;
  loaded: boolean;
}

const SCOPE_KEY = "mads.mesh.scope";
const PEER_EVENT_TYPES = ["peer.created", "peer.updated", "peer.deleted", "peer.reachability_changed"];

function readScope(): MeshScope {
  try {
    const v = localStorage.getItem(SCOPE_KEY);
    return v === "all" || v?.startsWith("peer:") ? (v as MeshScope) : "self";
  } catch {
    return "self";
  }
}

/** The one device a `peer:<name>` scope shows, or undefined. */
export function scopePeer(scope: MeshScope): string | undefined {
  return scope.startsWith("peer:") ? scope.slice("peer:".length) : undefined;
}

let snapshot: MeshSnapshot = { peers: [], scope: readScope(), loaded: false };
const listeners = new Set<() => void>();
let unsubscribeBus: (() => void) | null = null;
let loading: Promise<void> | null = null;

// Passive watchers see changes but don't keep the store live: the sessions
// store follows the scope this way, so a window with no mesh UI mounted
// (and tests) never fetches /peers on its account.
const watchers = new Set<() => void>();

function setSnapshot(next: MeshSnapshot): void {
  snapshot = next;
  for (const l of listeners) l();
  for (const w of watchers) w();
}

/** Calls listener on every change without activating the store. */
export function watchMesh(listener: () => void): () => void {
  watchers.add(listener);
  return () => {
    watchers.delete(listener);
  };
}

/**
 * A peer whose event log this window can't follow (its mads predates the
 * live-only bus: GET /events/head 404s there) reads as needing an update,
 * wherever reachability shows: its sessions' live updates are off, the rest
 * of the window carries on.
 */
export const EVENTS_INCOMPATIBLE_PEER_REASON = "its mads is older than this window, so its sessions' live updates are off: update mads there";

function withEventStaleness(peers: PeerStatus[]): PeerStatus[] {
  const stale = getIncompatiblePeers();
  if (stale.size === 0) return peers;
  return peers.map((p) =>
    stale.has(p.name) ? { ...p, reachability: "version_mismatch", reason: EVENTS_INCOMPATIBLE_PEER_REASON } : p,
  );
}

subscribeToIncompatiblePeers(() => setSnapshot({ ...snapshot, peers: withEventStaleness(snapshot.peers) }));

/** Re-reads GET /peers; peer.* events call it, and so do the Devices controls. */
export function refreshPeers(): Promise<void> {
  if (loading) return loading;
  loading = meshApi
    .listPeers()
    .then(
      (read) => {
        const peers = withEventStaleness(Array.isArray(read) ? read : []);
        if (JSON.stringify(peers) !== JSON.stringify(snapshot.peers) || !snapshot.loaded) {
          setSnapshot({ ...snapshot, peers, loaded: true });
        }
      },
      // A Router from before mesh mode has no /peers: no peers, no toggle.
      () => {
        if (!snapshot.loaded) setSnapshot({ ...snapshot, loaded: true });
      },
    )
    .finally(() => {
      loading = null;
    });
  return loading;
}

/** The peer a peer.* event is about: its row's name, or a reachability change's peer. */
function peerOfEvent(env: Envelope): string | undefined {
  const p = env.payload as { name?: unknown; peer?: unknown } | undefined;
  const name = typeof p?.name === "string" ? p.name : typeof p?.peer === "string" ? p.peer : undefined;
  return name || undefined;
}

export function subscribeMesh(listener: () => void): () => void {
  listeners.add(listener);
  if (listeners.size === 1) {
    // Subscribe, then read behind the bus's ready barrier (THE CONTRACT in
    // router/events.ts); every peer.* event re-reads the whole list.
    // A peer.* event (a grant change heard through the peer's hello, a
    // reachability change) is also a reason for a paused or unavailable
    // peer bus to try again (router/events.ts SessionLive).
    // Only that peer's buses: each refused retry is audited on its host.
    const off = subscribeToEvents({ types: PEER_EVENT_TYPES }, (env) => {
      const peer = peerOfEvent(env);
      if (peer) resumePeerBuses(peer);
      void refreshPeers();
    });
    unsubscribeBus = off;
    void eventsReady().then(() => {
      if (unsubscribeBus === off) void refreshPeers();
    });
  }
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) {
      unsubscribeBus?.();
      unsubscribeBus = null;
    }
  };
}

export function getMeshSnapshot(): MeshSnapshot {
  return snapshot;
}

export function setMeshScope(scope: MeshScope): void {
  try {
    localStorage.setItem(SCOPE_KEY, scope);
  } catch {
    // Private mode or a full quota: the toggle still works for this window.
  }
  if (scope !== snapshot.scope) setSnapshot({ ...snapshot, scope });
}

/**
 * True when the list should include peers: All devices with a visible peer,
 * or one device that's still registered and visible. Anything else (that
 * device removed or hidden since) falls back to this device.
 */
export function meshShowsAll(s: MeshSnapshot = snapshot): boolean {
  const visible = s.peers.filter((p) => !p.hidden);
  if (s.scope === "all") return visible.length > 0;
  const peer = scopePeer(s.scope);
  return peer !== undefined && visible.some((p) => p.name === peer);
}

/** The scope in effect: what the list shows given which devices exist now. */
export function effectiveScope(s: MeshSnapshot = snapshot): MeshScope {
  return meshShowsAll(s) ? s.scope : "self";
}

/** Reads the mesh state without starting it (no /peers fetch, no stream). */
export function useMesh(): MeshSnapshot {
  return useSyncExternalStore(watchMesh, getMeshSnapshot);
}

/**
 * Keeps the mesh state live for the window: GET /peers now and on every
 * peer.* event. Mounted once, by App; everything else reads with useMesh,
 * so a component rendered on its own (and its tests) never touches the
 * network on mesh mode's account.
 */
export function useMeshLive(): MeshSnapshot {
  return useSyncExternalStore(subscribeMesh, getMeshSnapshot);
}

export function resetMeshForTests(): void {
  unsubscribeBus?.();
  unsubscribeBus = null;
  listeners.clear();
  watchers.clear();
  loading = null;
  snapshot = { peers: [], scope: readScope(), loaded: false };
  clearSessionHostsForTests();
  peerClients.clear();
}

export { onSessionHostsChanged, peerBase, peerOfSession, recordSessionHosts } from "./sessionHosts";

/**
 * A remote session's live-update state (router/events.ts SessionLive):
 * "live" for this device's sessions and connected peer buses.
 */
export function useSessionLive(sessionId: string | undefined): SessionLive {
  return useSyncExternalStore(subscribeToSessionLive, () =>
    sessionId ? getSessionLive(sessionId) : NO_SESSION_LIVE,
  );
}

const NO_SESSION_LIVE: SessionLive = { state: "live", epoch: 0 };
