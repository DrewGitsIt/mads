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
  /** Its Tailscale key's expiry, as its hello last reported it. */
  tailscale_key_expiry?: string;
}

/** One address, as codes, hellos and the Mesh Service report them (contracts.NetAddress). */
export interface NetAddress {
  via: string;
  addr: string;
  kind?: NetKind;
  label?: string;
  network?: string;
}

export type NetKind = "overlay" | "lan" | "public";

/** A row's address with its live state (contracts.AddressStatus): shown only under Troubleshoot connection. */
export interface AddressStatus extends NetAddress {
  source: "code" | "hello" | "user" | "joiner" | "introduced" | "discovered" | "legacy" | string;
  added_at: string;
  last_ok_at?: string;
  state: "in_use" | "works" | "not_on_network" | "not_answering" | "other_device" | "refused" | "untried" | string;
  error?: string;
}

/** The address a connection uses now (contracts.PathStatus): the Status tooltip only. */
export interface PathStatus {
  addr: string;
  kind?: NetKind;
  label?: string;
  rtt_ms?: number;
  since: string;
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
  path?: PathStatus;
  addresses?: AddressStatus[];
  /** tailscale_signed_out | tailscale_key_expiring. */
  diagnosis?: string;
  /** Whether the device has this one paired, as last checked (F6 R3); absent until checked. */
  pairing_there?: PairingThere;
  /** Why it's unknown (devices_unavailable, timeout, no address…). */
  pairing_there_reason?: string;
  pairing_checked_at?: string;
}

/** paired: it answered hello; not_paired: it doesn't have this key; other_key: a different key answered. */
export type PairingThere = "paired" | "not_paired" | "unknown" | "other_key";

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
  /** This device's mesh network (absent from an older Router). */
  mesh?: SelfMesh;
}

/** One network this device is on (or a chosen one it isn't on now): GET /devices/self's mesh.networks. */
export interface NetworkView {
  id: string;
  kind: NetKind | "";
  title: string;
  iface?: string;
  /** vpn_anywhere | local_only | internet. */
  meaning: string;
  /** tailnet_only | vpn_members | anyone_on_network | anyone_on_shared_network | anyone_on_internet. */
  who_can_knock: string;
  risk: "low" | "medium" | "high" | string;
  recommended?: boolean;
  shared?: boolean;
  wireless?: boolean;
  ssid?: string;
  chosen: boolean;
  available: boolean;
  listening: boolean;
  error?: string;
}

export interface TailscaleView {
  present: boolean;
  backend_state?: string;
  needs_login: boolean;
  auth_url?: string;
  dns_name?: string;
  tailnet?: string;
  key_expiry?: string;
  key_expiry_off: boolean;
  admin_url: string;
  /** The control server this node signs in with: a sign-in link may point only there or at Tailscale's. */
  control_url?: string;
  endpoint?: string;
  error?: string;
}

/** A device that started a discovered pairing in the setup window (device.pair_started). */
export interface PairingStarted {
  name: string;
  fingerprint: string;
  short: string;
  remote?: string;
  network?: string;
  at: string;
}

/** GET /devices/self's mesh: what Set up mesh and the Mesh network card show. */
export interface SelfMesh {
  /** False: the Set up mesh empty state. */
  configured: boolean;
  networks: NetworkView[];
  port: number;
  public: boolean;
  tailnet_check: boolean;
  tailnet_check_status?: "on" | "off" | "unavailable" | string;
  tailnet_check_locked?: boolean;
  discoverable_until?: string;
  window_closed_reason?: string;
  pairing_started: PairingStarted[];
  listening: NetAddress[];
  service_seen: boolean;
  service_answering?: boolean;
  tailscale?: TailscaleView;
}

/** GET /devices/discovered. */
export interface DiscoveredList {
  searching: boolean;
  started_at: string;
  networks: { id: string; label: string; kind?: string }[];
  devices: DiscoveredDevice[];
  hidden_count: number;
  hidden_known: boolean;
}

export interface DiscoveredDevice {
  name: string;
  platform?: string;
  fingerprint: string;
  short: string;
  network: string;
  network_label?: string;
  via: "tailscale" | "mdns" | string;
  /** A row for it exists here. */
  paired: boolean;
  /** Which sides have the pairing (F6 R5): absent from an older Router. */
  pairing?: "none" | "both" | "here_only" | "there_only";
  peer_name?: string;
  compatible: boolean;
  seen_at: string;
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
  /** code | discovered ("" reads as code). */
  mode?: "code" | "discovered" | "";
  /** A discovered request's six digits, shown on both screens. */
  check_code?: string;
  /** Tailscale vouched for both ends: no number to compare. */
  vouched?: boolean;
  /** Why a vouched request still needs its number compared: another_request | name_collides. */
  vouch_withheld?: string;
  /** The joiner said the number it showed wasn't this host's: undone here. */
  joiner_mismatch?: boolean;
  addresses?: NetAddress[];
  network?: string;
  network_label?: string;
  state: PairState;
  level?: string;
  peer_id?: string;
  created_at: string;
  expires_at: string;
  decided_at?: string;
  /** The joiner's key is already paired here: its row and current level. Approval replaces that (F6 R2). */
  repair?: { peer_id: string; level: string };
}

export interface PairHost {
  name: string;
  fingerprint: string;
  mesh_url?: string;
  addresses?: NetAddress[];
}

/** POST /devices/pairing-code. */
export interface PairingCode {
  id: string;
  code: string;
  expires_at: string;
  host: PairHost;
  /** The networks the code's addresses are on (the dialog names them, not the addresses). */
  networks?: { id: string; label: string; kind?: string }[];
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
  mode?: "code" | "discovered" | "";
  check_code?: string;
  vouched?: boolean;
  /** A discovered join's last step: needed (did the host show this number?), matched, mismatch. */
  confirm?: "needed" | "matched" | "mismatch" | "";
  /** The network the host was reached over ("Tailscale"). */
  network?: string;
  /** Set once the host is pinned, when that network became this device's mesh network. */
  adopted_network?: { id: string; label: string; kind?: string };
  /** Why it wasn't: shared | gateway_unknown. */
  not_adopted?: string;
}

/** POST /devices/pair with update_addresses: an already-paired host's code. */
export interface UpdatedAddresses {
  update_addresses: true;
  peer_id: string;
  name: string;
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
  /** What the removed device said when asked to forget this one (F6 R1). */
  there?: "removed" | "not_paired" | "offline" | "failed";
  there_error?: string;
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
    /** The answer's other fields (host_networks, tips, name, reason; a join's retryable and retry_after…). */
    readonly extra: Record<string, unknown> = {},
  ) {
    super(message);
  }
}

/**
 * Whether a failed join may be tried again with the same code (F6 R7: the
 * Router marks couldn't connect, timeouts, a host not ready and rate
 * limits), and after how many seconds. Anything else needs a new code.
 */
export function joinRetry(err: unknown): { retryable: boolean; after: number } {
  if (!(err instanceof DevicesError) || err.extra.retryable !== true) return { retryable: false, after: 0 };
  const after = typeof err.extra.retry_after === "number" && err.extra.retry_after > 0 ? err.extra.retry_after : 0;
  return { retryable: true, after };
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

/** A pairing request may first wait for this device's Mesh Service to report where it listens (up to 15 s, T1), then dial. */
export const PAIR_REQUEST_TIMEOUT_MS = 30_000;

async function call<T>(path: string, init?: RequestInit, timeoutMs = FETCH_TIMEOUT_MS): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${ROUTER_BASE}${path}`, {
      ...init,
      headers: init?.body ? { "Content-Type": "application/json" } : undefined,
      signal: AbortSignal.timeout(timeoutMs),
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
    const extra: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(body)) if (k !== "error" && k !== "code" && k !== "errors") extra[k] = v;
    throw new DevicesError(msg, res.status, body.code, extra);
  }
  return body as T;
}

const json = (method: string, body: unknown): RequestInit => ({ method, body: JSON.stringify(body) });
const fp = (f: string) => encodeURIComponent(f);

export const devicesApi = {
  list: () => call<DeviceRow[]>("/devices"),
  self: () => call<SelfDevice>("/devices/self"),
  setSettings: (s: { require_tailnet: boolean }) => call<SelfDevice>("/devices/settings", json("PUT", s)),
  /** The mesh network (0 or 1 id), the Tailscale check, the internet. */
  setMesh: (s: { networks?: string[]; tailnet_check?: boolean; public?: boolean; confirm_public?: boolean }) =>
    call<SelfDevice>("/devices/settings", json("PUT", s)),
  /** Opens, renews or closes this device's setup window. */
  discoverable: (on: boolean, renew = false) =>
    call<{ on: boolean; until?: string }>("/devices/discoverable", json("POST", renew ? { on, renew } : { on })),
  discovered: () => call<DiscoveredList>("/devices/discovered"),
  requestDiscovered: (fingerprint: string) =>
    call<JoinStatus>(`/devices/discovered/${fp(fingerprint)}/request`, json("POST", {}), PAIR_REQUEST_TIMEOUT_MS),
  confirmJoin: (id: string, match: boolean) => call<JoinStatus>(`/devices/pair/${encodeURIComponent(id)}/confirm`, json("POST", { match })),
  /** Checks every paired row now (F6 R3): the rows, with pairing_there. */
  check: () => call<{ devices: DeviceRow[] }>("/devices/check", json("POST", {})),
  updateAddresses: (code: string) => call<UpdatedAddresses>("/devices/pair", json("POST", { code, update_addresses: true })),
  redial: (fingerprint: string) =>
    call<{ reachability: string; reason?: string; path?: PathStatus; addresses?: AddressStatus[] }>(`/devices/${fp(fingerprint)}/redial`, json("POST", {})),
  tailscaleLogin: () => call<{ auth_url?: string; backend_state?: string; needs_login: boolean; control_url?: string }>("/devices/tailscale/login", json("POST", {})),
  activity: (opts: { device?: string; limit?: number } = {}) => {
    const q = new URLSearchParams();
    if (opts.device) q.set("device", opts.device);
    q.set("limit", String(opts.limit ?? 100));
    return call<Envelope[]>(`/devices/activity?${q}`);
  },
  createCode: () => call<PairingCode>("/devices/pairing-code", { method: "POST" }),
  deleteCode: (id: string) => call<void>(`/devices/pairing-code/${encodeURIComponent(id)}`, { method: "DELETE" }),
  pairRequests: () => call<PairRequest[]>("/devices/pair-requests"),
  /** shown: what the prompt showed for a discovered request (the check code compared, or Tailscale's vouch). */
  approve: (id: string, level: Level, shown?: { check_code_confirmed?: boolean; shown_vouched?: boolean }) =>
    call<PairRequest>(`/devices/pair-requests/${encodeURIComponent(id)}/approve`, json("POST", { level, ...shown })),
  decline: (id: string) => call<PairRequest>(`/devices/pair-requests/${encodeURIComponent(id)}/decline`, { method: "POST" }),
  /** repair: the host no longer has this device paired (409 repair_offered); approval updates the row here in place. */
  join: (code: string, repair = false) =>
    call<JoinStatus>("/devices/pair", json("POST", repair ? { code, repair: true } : { code }), PAIR_REQUEST_TIMEOUT_MS),
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

/** One address a code carries: where the host may be reached. */
export interface CodeAddress {
  addr: string;
  kind?: string;
  label?: string;
}

export interface DecodedCode {
  host: string;
  /** The first address, as http://host:port (v1 codes carry only this). */
  meshUrl: string;
  /** Every usable address, in the host's preferred order (a v1 code's one). */
  addresses: CodeAddress[];
  fingerprint: string;
  expiresAt: Date;
  version: number;
}

export type DecodeResult = { ok: true; code: DecodedCode } | { ok: false; error: string };

const NOT_A_CODE = "That isn't a mads pairing code. Copy the whole line from the other device.";
const CODE_VERSION = 2;
const FINGERPRINT_RE = /^[A-Z2-7]{52}$/;
const HOST_PORT_RE = /^(\[[0-9A-Fa-f:.]+\]|[^/:\s[\]]+):\d{1,5}$/;
/** At most this many characters of another device's text are shown (paircode.MaxShown). */
const MAX_SHOWN = 200;
/** What paircode.CleanText keeps: Go's unicode.IsGraphic (letters, marks, numbers, punctuation, symbols, spaces), never a format character. */
const GRAPHIC = /^[\p{L}\p{M}\p{N}\p{P}\p{S}\p{Zs}]$/u;
const FORMAT = /^\p{Cf}$/u;

/**
 * Text from another device made safe to show, as pkg/paircode.CleanText
 * does (the two share testdata/cleantext.json): control and format
 * characters (bidi overrides and isolates, zero-width) dropped, at most
 * MAX_SHOWN characters, then "…".
 */
export function cleanText(s: string): string {
  let out = "";
  let n = 0;
  for (const c of s) {
    if (!GRAPHIC.test(c) || FORMAT.test(c)) continue;
    if (n === MAX_SHOWN) return `${out}…`;
    out += c;
    n++;
  }
  return out;
}

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
 * anywhere ignored, the version checked by name; v1 and v2), for the
 * preview only: the host decides whether it's still good.
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
  if (version < 1) {
    return { ok: false, error: "That pairing code is from an older mads. Update mads on the other device." };
  }
  const damaged = { ok: false as const, error: `${NOT_A_CODE.slice(0, -1)} (it's damaged).` };
  const bytes = base64urlDecode(m[2]);
  if (!bytes) return damaged;
  let w: { n?: unknown; u?: unknown; f?: unknown; e?: unknown; s?: unknown; a?: unknown };
  try {
    w = JSON.parse(new TextDecoder().decode(bytes)) as typeof w;
  } catch {
    return damaged;
  }
  const secret = typeof w.s === "string" ? base64urlDecode(w.s) : null;
  if (typeof w.n !== "string" || typeof w.f !== "string" || !FINGERPRINT_RE.test(w.f) || typeof w.e !== "number" || !secret || secret.length !== 32) {
    return damaged;
  }
  let addresses: CodeAddress[] = [];
  if (version === 1) {
    if (typeof w.u !== "string" || !/^http:\/\/[^/:]+:\d+\/?$/.test(w.u)) return damaged;
    addresses = [{ addr: w.u.replace(/^http:\/\//, "").replace(/\/$/, "") }];
  } else {
    if (!Array.isArray(w.a) || w.a.length === 0 || w.a.length > 8) return damaged;
    for (const a of w.a as unknown[]) {
      if (!a || typeof a !== "object") return damaged;
      const c = a as { v?: unknown; h?: unknown; k?: unknown; l?: unknown };
      // An adapter this mads doesn't know is skipped, never fatal.
      if (c.v !== "direct") continue;
      if (typeof c.h !== "string" || !HOST_PORT_RE.test(c.h)) return damaged;
      addresses.push({ addr: c.h, kind: typeof c.k === "string" ? c.k : undefined, label: typeof c.l === "string" ? cleanText(c.l) : undefined });
    }
    if (addresses.length === 0) {
      return { ok: false, error: "That isn't a mads pairing code this mads can use: it has no address this mads knows how to reach." };
    }
  }
  const expiresAt = new Date(w.e * 1000);
  return { ok: true, code: { host: cleanText(w.n), meshUrl: `http://${addresses[0].addr}`, addresses, fingerprint: w.f, expiresAt, version } };
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
// The setup window, pairings started in it, and where the Mesh Service listens.
const SELF_STORE_EVENTS = ["device.settings_changed", "device.discoverable", "device.pair_started", "device.pairing_abandoned", "mesh.listening"];
const PAIR_REQUESTS_STORE_EVENTS = ["device.pair_requested", "device.pair_declined", "device.paired"];
const DEVICE_ACTIVITY_EVENTS = [...DEVICE_EVENTS, "device.pair_started", "device.pairing_abandoned", "device.level_uncapped", "peer.reachability_changed", "session.device_message"];

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
