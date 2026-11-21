import { useEffect, useState, useSyncExternalStore } from "react";
import { ROUTER_BASE } from "./client";
import { eventsReady, subscribeToEvents } from "./events";
import type { Envelope, PeerReachability } from "./types";

/**
 * Settings → Paired devices, renderer side (docs/MESH-AUTH-DESIGN.md D9 and
 * the A6 backend implementation notes): the Router's /devices routes,
 * and the stores that keep them live off the window's one LOCAL event bus
 * (peer.* for the rows, device.* for the log and the pairing prompt). No
 * EventSource of its own, and every request gives up after FETCH_TIMEOUT_MS
 * so a wedged Router can't hold one of the browser's few connections.
 */

// --- Wire types (pkg/contracts, services/router/internal/mesh) ---------------

/** A grant level as the UI sends it; "none" is the stored nil grant. */
export type Level = "none" | "view" | "chat" | "control" | "admin";

export const LEVELS: Level[] = ["none", "view", "chat", "control", "admin"];

export type FullTrustChat = "allowed" | "refused" | "held";

export interface PeerCeilings {
  full_trust_chat?: FullTrustChat;
  no_shell?: boolean;
  max_trust?: string;
}

export interface PeerRemote {
  platform?: string;
  hosts_sessions?: boolean;
  /** What the device lets this one do there; absent is none. Meaningful only with as_of. */
  level?: Exclude<Level, "none">;
  /** When `level` was read; absent: never reached. */
  as_of?: string;
  /** What it grants this one there before this node's tailnet policy caps it. */
  granted?: Exclude<Level, "none">;
  /** This node's tailnet policy holds `level` below `granted`. */
  tailnet_capped?: boolean;
}

/** GET /devices row (contracts.DeviceStatus). */
export interface DeviceRow {
  id: string;
  name: string;
  dns_name?: string;
  mesh_url?: string;
  hidden?: boolean;
  /** Absent reads as true (rows from before pairing). */
  viewed?: boolean;
  fingerprint?: string;
  /** Level "" is none with its limits kept; no grant at all is none too. */
  grant?: { level: Exclude<Level, "none"> | ""; ceilings?: PeerCeilings };
  pairing?: { paired_at: string; paired_by: string; how: "accept" | "ceremony" | "code" | "introduction" };
  remote?: PeerRemote;
  created_at: string;
  updated_at: string;
  /** Empty for a device this node never dials. */
  reachability?: PeerReachability | "";
  reason?: string;
  rtt_ms?: number;
  access?: string[];
  checked_at?: string;
  last_used_at?: string;
}

/** GET /devices/self. */
export interface SelfDevice {
  name: string;
  fingerprint?: string;
  short?: string;
  platform: string;
  mesh_url?: string;
  hosts_sessions: boolean;
  require_tailnet?: boolean;
  require_tailnet_locked?: boolean;
}

export type PairState = "pending" | "approved" | "declined" | "expired";

/** GET /devices/pair-requests row (contracts.PairRequest). */
export interface PairRequest {
  id: string;
  fingerprint: string;
  name: string;
  platform?: string;
  hosts_sessions?: boolean;
  mesh_url?: string;
  remote?: string;
  /** Its tailnet node name, as WhoIs verified it (not what it claims). */
  tailnet_node?: string;
  /** A device paired or viewed here already has this name. */
  name_collides?: boolean;
  state: PairState;
  level?: string;
  peer_id?: string;
  created_at: string;
  expires_at: string;
  decided_at?: string;
}

export interface PairHost {
  name: string;
  fingerprint: string;
  mesh_url?: string;
}

/** POST /devices/pairing-code. */
export interface PairingCode {
  id: string;
  code: string;
  expires_at: string;
  host: PairHost;
}

/** POST /devices/pair and GET /devices/pair/{id}. */
export interface JoinStatus {
  /** This device's key for the join: GET /devices/pair/{id}. */
  id: string;
  /** The host's own request id. */
  request_id?: string;
  /** This device's short fingerprint: what the host's prompt shows. */
  self?: string;
  state: PairState;
  /** What the host granted, once approved: "none", view, chat or control. */
  level?: string;
  host: PairHost;
  expires_at: string;
  peer_id?: string;
  error?: string;
}

export type RemoveHostResult = "removed" | "offline" | "failed" | "not_admin" | "skipped";

/** POST /devices/{fp}/remove-everywhere. */
export interface RemoveResult {
  device: PairHost;
  here: "removed" | "absent";
  hosts: {
    name: string;
    fingerprint: string;
    result: RemoveHostResult;
    error?: string;
    /** The devices it had introduced on that host, as that host answered. */
    introduced_by_this?: PairHost[];
    also_removed?: number;
  }[];
  cannot_manage: { name: string; fingerprint: string; level?: string }[];
  /** The devices the removed one introduced here. */
  introduced_by_this?: PairHost[];
  /** Those removed with it (also_introduced). */
  also_removed?: PairHost[];
}

/** POST /devices/{fp}/introduce. */
export interface IntroduceResult {
  host: string;
  device: string;
  level: string;
  as: string;
}

/** An error answer from the Router: its code (when it gave one) and message. */
export class DevicesError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message);
  }
}

// --- Requests -------------------------------------------------------------------

export const FETCH_TIMEOUT_MS = 10_000;

/** Codes whose own message is for the CLI: what the UI says instead. */
const FRIENDLY: Record<string, string> = {
  bad_mesh_url: "That device's address isn't on your tailnet. Use the device's tailnet name.",
  pair_request_not_found: "That pairing request is gone: it expired, or was answered.",
  expired: "This request expired before it was answered. Nothing was paired.",
  already_answered: "This request was already answered, here or from the CLI.",
};

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${ROUTER_BASE}${path}`, {
      ...init,
      headers: init?.body ? { "Content-Type": "application/json" } : undefined,
      signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
    });
  } catch (err) {
    if (err instanceof DOMException && (err.name === "TimeoutError" || err.name === "AbortError")) {
      throw new DevicesError("This device's mads didn't answer in time. Try again.", 0, "timeout");
    }
    throw err;
  }
  if (res.status === 204) return undefined as T;
  const body = (await res.json().catch(() => ({}))) as {
    error?: string;
    code?: string;
    errors?: { field: string; message: string }[];
  };
  if (!res.ok) {
    const msg =
      FRIENDLY[body.code ?? ""] || body.error || body.errors?.map((e) => e.message).join("; ") || `${init?.method ?? "GET"} ${path}: ${res.status}`;
    throw new DevicesError(msg, res.status, body.code);
  }
  return body as T;
}

const json = (method: string, body: unknown): RequestInit => ({ method, body: JSON.stringify(body) });
const fp = (f: string) => encodeURIComponent(f);

export const devicesApi = {
  list: () => call<DeviceRow[]>("/devices"),
  self: () => call<SelfDevice>("/devices/self"),
  setSettings: (s: { require_tailnet: boolean }) => call<SelfDevice>("/devices/settings", json("PUT", s)),
  activity: (opts: { device?: string; limit?: number } = {}) => {
    const q = new URLSearchParams();
    if (opts.device) q.set("device", opts.device);
    q.set("limit", String(opts.limit ?? 100));
    return call<Envelope[]>(`/devices/activity?${q}`);
  },
  createCode: () => call<PairingCode>("/devices/pairing-code", { method: "POST" }),
  deleteCode: (id: string) => call<void>(`/devices/pairing-code/${encodeURIComponent(id)}`, { method: "DELETE" }),
  pairRequests: () => call<PairRequest[]>("/devices/pair-requests"),
  approve: (id: string, level: Level) =>
    call<PairRequest>(`/devices/pair-requests/${encodeURIComponent(id)}/approve`, json("POST", { level })),
  decline: (id: string) => call<PairRequest>(`/devices/pair-requests/${encodeURIComponent(id)}/decline`, { method: "POST" }),
  join: (code: string) => call<JoinStatus>("/devices/pair", json("POST", { code })),
  joinStatus: (id: string) => call<JoinStatus>(`/devices/pair/${encodeURIComponent(id)}`),
  setGrant: (fingerprint: string, level: Level, ceilings?: PeerCeilings) =>
    call<unknown>(`/devices/${fp(fingerprint)}/grant`, json("PUT", ceilings ? { level, ceilings } : { level })),
  introduce: (fingerprint: string, host: string, level: Level) =>
    call<IntroduceResult>(`/devices/${fp(fingerprint)}/introduce`, json("POST", { host, level })),
  removeEverywhere: (fingerprint: string, opts: { hosts?: string[]; alsoIntroduced?: boolean } = {}) =>
    call<RemoveResult>(
      `/devices/${fp(fingerprint)}/remove-everywhere`,
      json("POST", { ...(opts.hosts ? { hosts: opts.hosts } : {}), ...(opts.alsoIntroduced ? { also_introduced: true } : {}) }),
    ),
  /** Show sessions on: the existing /peers, with the row's name, address and key. */
  view: (row: Pick<DeviceRow, "name" | "mesh_url" | "fingerprint">) =>
    call<unknown>("/peers", json("POST", { name: row.name, mesh_url: row.mesh_url, fingerprint: row.fingerprint })),
  /** Show sessions off: a paired row stays, no longer viewed. */
  unview: (name: string) => call<void>(`/peers/${encodeURIComponent(name)}`, { method: "DELETE" }),
};

// --- The pairing code (pkg/paircode), decoded here to preview the host -------

export interface DecodedCode {
  host: string;
  meshUrl: string;
  fingerprint: string;
  expiresAt: Date;
}

export type DecodeResult = { ok: true; code: DecodedCode } | { ok: false; error: string };

const NOT_A_CODE = "That isn't a mads pairing code. Copy the whole line from the other device.";
const CODE_VERSION = 1;
const FINGERPRINT_RE = /^[A-Z2-7]{52}$/;

function base64urlDecode(s: string): Uint8Array | null {
  if (!/^[A-Za-z0-9_-]*$/.test(s) || s.length % 4 === 1) return null;
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4);
  try {
    const bin = atob(b64);
    return Uint8Array.from(bin, (c) => c.charCodeAt(0));
  } catch {
    return null;
  }
}

/**
 * Reads a pasted pairing code the way pkg/paircode.Parse does (whitespace
 * anywhere ignored, the version checked by name), for the preview only: the
 * host decides whether it's still good.
 */
export function decodePairingCode(raw: string): DecodeResult {
  const s = raw.replace(/\s+/g, "");
  if (s.length > 4096) return { ok: false, error: NOT_A_CODE };
  const m = /^mads-pair-(\d+):(.*)$/.exec(s);
  if (!m) return { ok: false, error: NOT_A_CODE };
  const version = Number(m[1]);
  if (version > CODE_VERSION) {
    return { ok: false, error: "That pairing code is from a newer mads. Update mads here, or make the code on a device running this version." };
  }
  if (version !== CODE_VERSION) {
    return { ok: false, error: "That pairing code is from an older mads. Update mads on the other device." };
  }
  const bytes = base64urlDecode(m[2]);
  if (!bytes) return { ok: false, error: `${NOT_A_CODE.slice(0, -1)} (it's damaged).` };
  let w: { n?: unknown; u?: unknown; f?: unknown; e?: unknown; s?: unknown };
  try {
    w = JSON.parse(new TextDecoder().decode(bytes)) as typeof w;
  } catch {
    return { ok: false, error: `${NOT_A_CODE.slice(0, -1)} (it's damaged).` };
  }
  const secret = typeof w.s === "string" ? base64urlDecode(w.s) : null;
  if (
    typeof w.n !== "string" ||
    typeof w.u !== "string" ||
    !/^http:\/\/[^/:]+:\d+\/?$/.test(w.u) ||
    typeof w.f !== "string" ||
    !FINGERPRINT_RE.test(w.f) ||
    typeof w.e !== "number" ||
    !secret ||
    secret.length !== 32
  ) {
    return { ok: false, error: `${NOT_A_CODE.slice(0, -1)} (it's damaged).` };
  }
  const expiresAt = new Date(w.e * 1000);
  return { ok: true, code: { host: w.n, meshUrl: w.u, fingerprint: w.f, expiresAt } };
}

// --- Live stores ---------------------------------------------------------------------

const ROW_EVENTS = ["peer.created", "peer.updated", "peer.deleted", "peer.reachability_changed"];
export const DEVICE_EVENTS = [
  "device.paired",
  "device.unpaired",
  "device.grant_changed",
  "device.introduced",
  "device.refused",
  "device.code_created",
  "device.pair_requested",
  "device.pair_declined",
  "device.settings_changed",
];

/**
 * A tiny store: a snapshot, a loader, and the bus subscription that makes it
 * reload. The first listener subscribes, then reads behind the bus's ready
 * barrier (THE CONTRACT in router/events.ts): every event after the cursor
 * reloads, and a reload always reads the whole state, so the latest read
 * (single-flight, `again`) is what stands.
 */
export interface LiveStore<T> {
  subscribe(listener: () => void): () => void;
  get(): Snapshot<T>;
  refresh(): Promise<void>;
  /** Applies a local change at once, ahead of the event that confirms it. */
  set(value: T): void;
  reset(): void;
}

export interface Snapshot<T> {
  value: T | undefined;
  loaded: boolean;
  error?: string;
}

type Subscribe = (onEvent: () => void) => () => void;

function liveStore<T>(load: () => Promise<T>, subscribeBus: Subscribe): LiveStore<T> {
  let snap: Snapshot<T> = { value: undefined, loaded: false };
  const listeners = new Set<() => void>();
  let unsubscribe: (() => void) | null = null;
  let inflight: Promise<void> | null = null;
  let again = false;
  const emit = (next: Snapshot<T>) => {
    snap = next;
    for (const l of listeners) l();
  };
  const refresh = (): Promise<void> => {
    if (inflight) {
      again = true;
      return inflight;
    }
    inflight = load()
      .then(
        (value) => emit({ value, loaded: true }),
        (err: unknown) => emit({ ...snap, loaded: true, error: err instanceof Error ? err.message : String(err) }),
      )
      .finally(() => {
        inflight = null;
        if (again) {
          again = false;
          void refresh();
        }
      });
    return inflight;
  };
  return {
    subscribe(listener) {
      listeners.add(listener);
      if (listeners.size === 1) {
        const off = subscribeBus(() => void refresh());
        unsubscribe = off;
        void eventsReady().then(() => {
          if (unsubscribe === off) void refresh();
        });
      }
      return () => {
        listeners.delete(listener);
        if (listeners.size === 0) {
          unsubscribe?.();
          unsubscribe = null;
        }
      };
    },
    get: () => snap,
    refresh,
    set: (value) => emit({ value, loaded: true }),
    reset() {
      unsubscribe?.();
      unsubscribe = null;
      listeners.clear();
      inflight = null;
      again = false;
      snap = { value: undefined, loaded: false };
    },
  };
}

const DEVICES_STORE_EVENTS = [...ROW_EVENTS, ...DEVICE_EVENTS];
const SELF_STORE_EVENTS = ["device.settings_changed"];
const PAIR_REQUESTS_STORE_EVENTS = ["device.pair_requested", "device.pair_declined", "device.paired"];
const DEVICE_ACTIVITY_EVENTS = [...DEVICE_EVENTS, "session.device_message"];

export const devicesStore = liveStore(devicesApi.list, (on) => subscribeToEvents({ types: DEVICES_STORE_EVENTS }, on));
export const selfStore = liveStore(devicesApi.self, (on) => subscribeToEvents({ types: SELF_STORE_EVENTS }, on));
export const pairRequestsStore = liveStore(devicesApi.pairRequests, (on) =>
  subscribeToEvents({ types: PAIR_REQUESTS_STORE_EVENTS }, on),
);

export function useLive<T>(store: LiveStore<T>): Snapshot<T> {
  return useSyncExternalStore(store.subscribe, store.get);
}

/** The paired-device rows, live. */
export const usePairedDevices = () => useLive(devicesStore);
/** This device, live. */
export const useSelfDevice = () => useLive(selfStore);

/**
 * The device activity log (one device's, or every device's), newest first:
 * re-read on every device.* event and on any event a device caused.
 */
export function useDeviceActivity(device: string | undefined, limit = 100): Snapshot<Envelope[]> {
  const [snap, setSnap] = useState<Snapshot<Envelope[]>>({ value: undefined, loaded: false });
  useEffect(() => {
    let live = true;
    let inflight = false;
    let again = false;
    const load = () => {
      if (inflight) {
        again = true;
        return;
      }
      inflight = true;
      devicesApi
        .activity({ device, limit })
        .then(
          (value) => live && setSnap({ value, loaded: true }),
          (err: unknown) => live && setSnap((s) => ({ ...s, loaded: true, error: err instanceof Error ? err.message : String(err) })),
        )
        .finally(() => {
          inflight = false;
          if (again && live) {
            again = false;
            load();
          }
        });
    };
    // Subscribe, then read behind the bus's ready barrier (THE CONTRACT in
    // router/events.ts); every event re-reads the whole log page.
    const off = subscribeToEvents({ types: DEVICE_ACTIVITY_EVENTS }, () => load());
    void eventsReady().then(() => {
      if (live) load();
    });
    return () => {
      live = false;
      off();
    };
  }, [device, limit]);
  return snap;
}

export function resetDevicesForTests(): void {
  devicesStore.reset();
  selfStore.reset();
  pairRequestsStore.reset();
}
