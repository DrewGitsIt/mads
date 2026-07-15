import type {
  CapabilitySummary,
  Mentionable,
  Session,
  TranscriptMessage,
} from "./types";

/**
 * Structural subset of EventSource a stream source must satisfy — narrow
 * enough that a fake (no real HTTP/SSE transport) can implement it directly
 * instead of polyfilling the full EventSource DOM interface.
 */
export interface SessionEventSource {
  addEventListener(type: "message", listener: (ev: MessageEvent<string>) => void): void;
  removeEventListener(type: "message", listener: (ev: MessageEvent<string>) => void): void;
  close(): void;
}

/**
 * Router API surface the Conversation tab depends on. Several of these
 * routes don't exist on the Router yet (see ui/src/conversation/HANDOFF.md
 * §1) — this interface is the documented contract to build the frontend
 * against ahead of the backend proxying landing, per W6.1's precedent.
 */
export interface RouterClient {
  getSession(id: string): Promise<Session>;
  getTranscript(sessionId: string, page?: string): Promise<TranscriptMessage[]>;
  streamSession(sessionId: string): SessionEventSource;
  sendMessage(
    sessionId: string,
    body: { text: string; mentions?: string[] },
  ): Promise<void>;
  interrupt(sessionId: string): Promise<void>;
  bounce(sessionId: string): Promise<void>;
  getMentionables(query: string): Promise<Mentionable[]>;
  getSkillCapabilities(agentId?: string): Promise<CapabilitySummary[]>;
}

async function jsonOrThrow<T>(res: Response): Promise<T> {
  if (!res.ok) {
    throw new Error(`Router request failed: ${res.status} ${res.statusText}`);
  }
  return (await res.json()) as T;
}

/** fetch-backed RouterClient. Only exercised directly by client.test.ts. */
export class FetchRouterClient implements RouterClient {
  constructor(private readonly baseUrl: string = "") {}

  async getSession(id: string): Promise<Session> {
    const res = await fetch(`${this.baseUrl}/sessions/${id}`);
    return jsonOrThrow<Session>(res);
  }

  async getTranscript(
    sessionId: string,
    page?: string,
  ): Promise<TranscriptMessage[]> {
    const qs = page ? `?page=${encodeURIComponent(page)}` : "";
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/transcript${qs}`);
    return jsonOrThrow<TranscriptMessage[]>(res);
  }

  streamSession(sessionId: string): SessionEventSource {
    return new EventSource(`${this.baseUrl}/sessions/${sessionId}/stream`);
  }

  async sendMessage(
    sessionId: string,
    body: { text: string; mentions?: string[] },
  ): Promise<void> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      throw new Error(`Router request failed: ${res.status} ${res.statusText}`);
    }
  }

  async interrupt(sessionId: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/interrupt`, {
      method: "POST",
    });
    if (!res.ok) {
      throw new Error(`Router request failed: ${res.status} ${res.statusText}`);
    }
  }

  async bounce(sessionId: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/bounce`, {
      method: "POST",
    });
    if (!res.ok) {
      throw new Error(`Router request failed: ${res.status} ${res.statusText}`);
    }
  }

  async getMentionables(query: string): Promise<Mentionable[]> {
    const res = await fetch(
      `${this.baseUrl}/mentionables?q=${encodeURIComponent(query)}`,
    );
    return jsonOrThrow<Mentionable[]>(res);
  }

  async getSkillCapabilities(agentId?: string): Promise<CapabilitySummary[]> {
    const qs = agentId ? `?agent=${encodeURIComponent(agentId)}` : "";
    const res = await fetch(`${this.baseUrl}/capabilities${qs}`);
    return jsonOrThrow<CapabilitySummary[]>(res);
  }
}

// Thin fetch helpers used by the Launchpad surface (W6.2) — predates the
// RouterClient interface above; unifying the two idioms is follow-up work.
// Thin fetch wrapper shared by every surface — no ad-hoc fetches in
// components. The Router's address is exposed via VITE_ROUTER_URL; the
// default matches the Router's own `-addr` flag default (localhost:8080).
export const ROUTER_BASE =
  (import.meta.env.VITE_ROUTER_URL as string | undefined) ?? "http://localhost:8080";

async function readJSON<T>(res: Response): Promise<T> {
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`router request failed: ${res.status} ${body}`);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export async function routerGet<T>(
  path: string,
  params?: Record<string, string | undefined>,
): Promise<T> {
  const url = new URL(path, ROUTER_BASE);
  if (params) {
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined) url.searchParams.set(key, value);
    }
  }
  const res = await fetch(url);
  return readJSON<T>(res);
}

export async function routerPost<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(new URL(path, ROUTER_BASE), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return readJSON<T>(res);
}
