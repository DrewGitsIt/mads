import type {
  CapabilitySummary,
  Commit,
  FileChange,
  FileDiff,
  Mentionable,
  MergeResult,
  Session,
  TranscriptMessage,
} from "./types";

/**
 * Structural subset of EventSource a stream source must satisfy — narrow
 * enough that a fake (no real HTTP/SSE transport) can implement it directly
 * instead of polyfilling the full EventSource DOM interface. `type` is
 * `string`, not just `"message"`, so the same interface covers both the
 * data-plane stream (unnamed `"message"` frames, W6.3) and the control-plane
 * stream (named events like `"merge.completed"`, the Router's
 * `id:/event:/data:` wire format — see services/router/internal/api/
 * events.go and ui/src/files/HANDOFF.md §3).
 */
export interface SessionEventSource {
  addEventListener(type: string, listener: (ev: MessageEvent<string>) => void): void;
  removeEventListener(type: string, listener: (ev: MessageEvent<string>) => void): void;
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

  // Files tab / merge-back flow (W6.5) — see ui/src/files/HANDOFF.md §1/§3
  // for which of these are real Router routes today (only
  // streamControlEvents is) vs. documented-but-unimplemented contracts this
  // client is built ahead of.
  getFileChanges(sessionId: string, taskId?: string): Promise<FileChange[]>;
  getCommits(sessionId: string): Promise<Commit[]>;
  getFileDiff(sessionId: string, path: string, taskId?: string): Promise<FileDiff>;
  openInEditor(path: string): Promise<void>;
  mergeSession(sessionId: string, targetBranch?: string): Promise<MergeResult>;
  streamControlEvents(filter: { sessionId?: string; types?: string[] }): SessionEventSource;
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

  // Data Service routes, not yet proxied through the Router (HANDOFF §1) —
  // built against the MADS-UX §3.3-documented shape, same "land ahead of
  // proxying" precedent as getMentionables above.
  async getFileChanges(sessionId: string, taskId?: string): Promise<FileChange[]> {
    const qs = taskId ? `?task_id=${encodeURIComponent(taskId)}` : "";
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/files${qs}`);
    return jsonOrThrow<FileChange[]>(res);
  }

  async getCommits(sessionId: string): Promise<Commit[]> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/commits`);
    return jsonOrThrow<Commit[]>(res);
  }

  // No diff-content endpoint exists anywhere yet (HANDOFF §1) — hits the
  // MADS-UX-named path so it's a rename, not a redesign, once one lands.
  async getFileDiff(sessionId: string, path: string, taskId?: string): Promise<FileDiff> {
    const params = new URLSearchParams({ path });
    if (taskId) params.set("task_id", taskId);
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/changes?${params}`);
    return jsonOrThrow<FileDiff>(res);
  }

  // MADS-UX §3.3's open-in-editor action; no `/system/open-editor` route
  // exists anywhere in the codebase yet (HANDOFF §1).
  async openInEditor(path: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}/system/open-editor`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path }),
    });
    if (!res.ok) {
      throw new Error(`Router request failed: ${res.status} ${res.statusText}`);
    }
  }

  // Only the per-session, unproxied `POST /merge` exists today (HANDOFF
  // §1); this hits the Router-proxied path MADS-UX §3.3's own "Actions"
  // line documents.
  async mergeSession(sessionId: string, targetBranch?: string): Promise<MergeResult> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/merge`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target_branch: targetBranch }),
    });
    return jsonOrThrow<MergeResult>(res);
  }

  // The one real route here today: GET /events?session_id=&types= (HANDOFF
  // §1/§3), already proxied by the Router straight through to the Data
  // Service's control-plane event log.
  streamControlEvents(filter: { sessionId?: string; types?: string[] }): SessionEventSource {
    const params = new URLSearchParams();
    if (filter.sessionId) params.set("session_id", filter.sessionId);
    if (filter.types?.length) params.set("types", filter.types.join(","));
    return new EventSource(`${this.baseUrl}/events?${params}`);
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
