import { vi } from "vitest";
import type { DeviceRow, NetworkView, SelfDevice } from "../../../../router/devices";
import { FakeEventSource } from "../../../../router/testFakeEventSource";

/** One recorded request to the stubbed Router. */
export interface Call {
  method: string;
  path: string;
  body?: unknown;
  signal?: AbortSignal | null;
}

type Handler = (call: Call) => unknown | Response;

/**
 * Stubs fetch with a tiny Router for the Paired devices tests: `routes` maps
 * "METHOD /path" (path without the query) to a body or a handler; anything
 * else is a 404. Every call is recorded. EventSource is the fake one, so
 * the stores' LOCAL bus subscription is observable.
 */
export function stubRouter(routes: Record<string, unknown | Handler>): Call[] {
  const calls: Call[] = [];
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(input.toString(), "http://router.test");
      const call: Call = {
        method: init?.method ?? "GET",
        path: url.pathname + (url.search ? url.search : ""),
        body: init?.body ? JSON.parse(init.body as string) : undefined,
        signal: init?.signal,
      };
      calls.push(call);
      const key = `${call.method} ${url.pathname}`;
      const route = routes[key];
      if (route === undefined) return new Response(JSON.stringify({ error: `no route ${key}` }), { status: 404 });
      const out = typeof route === "function" ? (route as Handler)(call) : route;
      if (out instanceof Response) return out;
      if (out === null) return new Response(null, { status: 204 });
      return new Response(JSON.stringify(out), { status: 200 });
    }),
  );
  return calls;
}

export const fp = (c: string) => c.repeat(52).slice(0, 52);

export const SELF: SelfDevice = {
  name: "pop-os",
  fingerprint: fp("S"),
  short: "SSSS-SSSS-SSSS-SSSS",
  platform: "linux",
  hosts_sessions: true,
  require_tailnet: true,
};

const NOW = new Date().toISOString();

export function row(over: Partial<DeviceRow> & { name: string }): DeviceRow {
  return { id: `per_${over.name}`, created_at: NOW, updated_at: NOW, ...over };
}

/** pop-os's view in the mockup: a Mac that doesn't let it in, a phone, a display. */
export const MAC = row({
  name: "Alexs-MacBook-Air",
  fingerprint: fp("M"),
  mesh_url: "http://mac.ts.net:17380",
  viewed: false,
  grant: { level: "control" },
  remote: { platform: "darwin", hosts_sessions: true, as_of: NOW },
  reachability: "denied",
});
export const PHONE = row({
  name: "Drews-iPhone",
  fingerprint: fp("P"),
  viewed: false,
  grant: { level: "chat", ceilings: { full_trust_chat: "refused" } },
  remote: { platform: "ios", hosts_sessions: false },
  last_used_at: new Date(Date.now() - 5 * 60_000).toISOString(),
});
export const DISPLAY = row({
  name: "kitchen-display",
  fingerprint: fp("D"),
  viewed: false,
  grant: { level: "view" },
  remote: { hosts_sessions: false },
});
export const STUDIO = row({
  name: "mac-studio",
  fingerprint: fp("T"),
  mesh_url: "http://studio.ts.net:17380",
  viewed: true,
  remote: { platform: "darwin", hosts_sessions: true, level: "admin", as_of: NOW },
  reachability: "connected",
  rtt_ms: 9,
});

export const shortOf = (f: string) => f.match(/.{4}/g)!.slice(0, 4).join("-");

/** A network as GET /devices/self lists it (Tailscale unless told otherwise). */
export const net = (over: Partial<NetworkView> & { id: string }): NetworkView => ({
  kind: "overlay",
  title: "Tailscale",
  meaning: "vpn_anywhere",
  who_can_knock: "tailnet_only",
  risk: "low",
  chosen: false,
  available: true,
  listening: false,
  ...over,
});
