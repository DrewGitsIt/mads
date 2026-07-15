import type {
  Agent,
  AgentCapabilities,
  AgentLeaderboardEntry,
  BehaviorInfo,
  Capability,
  CapabilitySummary,
  CapabilityUsage,
  Commit,
  Config,
  CostMetrics,
  DiagnoseResult,
  FileChange,
  FileDiff,
  GenerateAgentBody,
  Mentionable,
  MergeResult,
  Model,
  Note,
  NoteType,
  PipelineRun,
  PreflightCheck,
  Profile,
  Provider,
  ProviderInfo,
  Retro,
  Session,
  Task,
  TaskOutcomes,
  TaskStatus,
  Template,
  TranscriptMessage,
  Workspace,
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

  // --- Tasks (W6.4, MADS-UX §3.3) ---
  listTasks(sessionId: string, status?: TaskStatus): Promise<Task[]>;
  getTask(id: string): Promise<Task>;
  createTask(task: Partial<Task>): Promise<Task>;
  updateTask(task: Task): Promise<Task>; // full replace — see HANDOFF.md §5
  confirmTask(id: string): Promise<void>;
  discardTask(id: string): Promise<void>;
  importTasks(tasks: Partial<Task>[]): Promise<Task[]>;
  taskCycles(sessionId: string): Promise<string[][]>;
  addNote(taskId: string, note: { type: NoteType; content: string }): Promise<Note>;
  confirmNote(taskId: string, noteId: number): Promise<void>;
  discardNote(taskId: string, noteId: number): Promise<void>;
  getSessionChanges(sessionId: string): Promise<FileChange[]>;
  streamTaskEvents(sessionId: string): SessionEventSource;

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

  // --- Sessions (session-agnostic) ---
  listSessions(): Promise<Session[]>;
  createSession(payload: Partial<Session>): Promise<{ session: Session; addr: string }>;

  // --- Config ---
  getConfig(): Promise<Config>;
  updateConfig(config: Config): Promise<Config>;

  // --- Profiles ---
  listProfiles(): Promise<Profile[]>;
  createProfile(profile: Profile): Promise<Profile>;
  updateProfile(profile: Profile): Promise<Profile>;
  deleteProfile(id: string): Promise<void>;
  activateProfile(id: string): Promise<Config>;

  // --- Credentials ---
  listCredentials(): Promise<string[]>;
  createCredential(name: string, value: string): Promise<{ name: string }>;
  deleteCredential(name: string): Promise<void>;
  listCredentialEnvCandidates(): Promise<string[]>;
  importCredentials(names: string[]): Promise<{ imported: string[] }>;

  // --- Preflight ---
  getPreflightChecks(): Promise<PreflightCheck[]>;

  // --- Providers / behaviors / models ---
  listProviders(): Promise<ProviderInfo[]>;
  listProviderNames(): Promise<Provider[]>;
  listProviderBehaviors(): Promise<BehaviorInfo[]>;
  listModels(provider: string): Promise<Model[]>;

  // --- Metrics (W6.9 Dashboard) ---
  metricsCost(): Promise<CostMetrics>;
  metricsAgentLeaderboard(): Promise<AgentLeaderboardEntry[]>;
  metricsTaskOutcomes(): Promise<TaskOutcomes>;
  metricsCapabilityUsage(): Promise<CapabilityUsage[]>;
  metricsPipelineRuns(): Promise<PipelineRun[]>;

  // --- Templates (Library) ---
  listTemplates(): Promise<Template[]>;
  createTemplate(t: Template): Promise<Template>;
  updateTemplate(t: Template): Promise<Template>;
  deleteTemplate(id: string): Promise<void>;
  validateTemplate(source: string): Promise<{ valid: boolean }>;

  // --- Workspaces (Library) ---
  listWorkspaces(): Promise<Workspace[]>;
  createWorkspace(w: Partial<Workspace>): Promise<Workspace>;
  updateWorkspace(w: Workspace): Promise<Workspace>;
  deleteWorkspace(id: string): Promise<void>;

  // --- Agents (Library) ---
  listAgents(): Promise<Agent[]>;
  createAgent(a: Partial<Agent>): Promise<Agent>;
  updateAgent(a: Agent): Promise<Agent>;
  deleteAgent(id: string): Promise<void>;
  generateAgent(body: GenerateAgentBody): Promise<{ job_id: string }>;
  describeAgent(prompt: string): Promise<{ result: string }>;
  suggestAgentCapabilities(prompt: string): Promise<AgentCapabilities>;
  refineAgent(prompt: string, instruction: string): Promise<{ result: string }>;

  // --- Capabilities (Library) ---
  listCapabilities(): Promise<Capability[]>;
  getCapability(id: string): Promise<Capability>;
  createCapability(c: Partial<Capability>): Promise<Capability>;
  updateCapability(c: Capability): Promise<Capability>;
  deleteCapability(id: string): Promise<void>;
  diagnoseCapability(id: string): Promise<DiagnoseResult>;

  // --- Tasks (session-agnostic) ---
  listAllTasks(status?: TaskStatus): Promise<Task[]>;

  // --- Retro ---
  listRetros(): Promise<Retro[]>;
  getRetro(id: string): Promise<Retro>;
  startRetro(
    sessionIds: string[],
    userObservations?: string,
  ): Promise<{ job_id: string; retro_id: string }>;
  acceptDpa(retroId: string, dpaId: string): Promise<void>;
  rejectDpa(retroId: string, dpaId: string): Promise<void>;
  createRetroFollowUpSession(
    retroId: string,
  ): Promise<{ session: Session; addr: string; tasks: Task[] }>;

  // --- Jobs ---
  cancelJob(jobId: string): Promise<void>;

  // --- Pipeline ---
  createPipelineRun(sessionId: string, templateId: string): Promise<{ run_id: string }>;
  cancelPipelineRun(runId: string): Promise<void>;
  retryTask(taskId: string): Promise<Task>;
}

/**
 * Thrown by task-mutating FetchRouterClient methods on a 400 response whose
 * body is a ValidationError ({"errors":[{"field","message"}]}) — lets the
 * bulk-import/edit UI show every field error at once (design rule 10.3)
 * instead of a single flat message. Narrowly scoped to the task-mutating
 * methods added in W6.4; nothing else in this client needed field-level
 * errors before now.
 */
export class RouterValidationError extends Error {
  constructor(public readonly errors: { field: string; message: string }[]) {
    super(errors.map((e) => `${e.field}: ${e.message}`).join("; "));
  }
}

// Thin fetch wrapper shared by every surface — no ad-hoc fetches in
// components. The Router's address is exposed via VITE_ROUTER_URL; the
// default matches the Router's own `-addr` flag default (localhost:8080).
export const ROUTER_BASE =
  (import.meta.env.VITE_ROUTER_URL as string | undefined) ?? "http://localhost:8080";

/**
 * On failure, a {errors:[{field,message}]} body throws RouterValidationError;
 * any other non-ok status throws a generic Error. 204 decodes to undefined.
 */
async function readJSON<T>(res: Response): Promise<T> {
  if (!res.ok) {
    const body = (await res
      .clone()
      .json()
      .catch(() => null)) as { errors?: { field: string; message: string }[] } | null;
    if (body?.errors) throw new RouterValidationError(body.errors);
    const text = await res.text();
    throw new Error(`router request failed: ${res.status} ${text}`);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

function buildUrl(
  baseUrl: string,
  path: string,
  params?: Record<string, string | undefined>,
): string {
  if (!params) return `${baseUrl}${path}`;
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined) qs.set(k, v);
  const s = qs.toString();
  return s ? `${baseUrl}${path}?${s}` : `${baseUrl}${path}`;
}

// Private transport helpers — every FetchRouterClient method is built on
// these. Not exported: callers use the typed RouterClient interface instead
// (see the `router` singleton at the bottom of this file).
async function routerGet<T>(
  baseUrl: string,
  path: string,
  params?: Record<string, string | undefined>,
): Promise<T> {
  const res = await fetch(buildUrl(baseUrl, path, params));
  return readJSON<T>(res);
}

async function routerPost<T>(baseUrl: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${baseUrl}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  return readJSON<T>(res);
}

async function routerPut<T>(baseUrl: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${baseUrl}${path}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  return readJSON<T>(res);
}

async function routerDelete<T>(baseUrl: string, path: string): Promise<T> {
  const res = await fetch(`${baseUrl}${path}`, { method: "DELETE" });
  return readJSON<T>(res);
}

/** fetch-backed RouterClient. Only exercised directly by client.test.ts. */
export class FetchRouterClient implements RouterClient {
  constructor(private readonly baseUrl: string = "") {}

  async getSession(id: string): Promise<Session> {
    return routerGet(this.baseUrl, `/sessions/${id}`);
  }

  async getTranscript(sessionId: string, page?: string): Promise<TranscriptMessage[]> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/transcript`, { page });
  }

  streamSession(sessionId: string): SessionEventSource {
    return new EventSource(`${this.baseUrl}/sessions/${sessionId}/stream`);
  }

  async sendMessage(
    sessionId: string,
    body: { text: string; mentions?: string[] },
  ): Promise<void> {
    return routerPost(this.baseUrl, `/sessions/${sessionId}/messages`, body);
  }

  async interrupt(sessionId: string): Promise<void> {
    return routerPost(this.baseUrl, `/sessions/${sessionId}/interrupt`);
  }

  async bounce(sessionId: string): Promise<void> {
    return routerPost(this.baseUrl, `/sessions/${sessionId}/bounce`);
  }

  async getMentionables(query: string): Promise<Mentionable[]> {
    return routerGet(this.baseUrl, `/mentionables?q=${encodeURIComponent(query)}`);
  }

  async getSkillCapabilities(agentId?: string): Promise<CapabilitySummary[]> {
    if (agentId) {
      return routerGet(this.baseUrl, `/capabilities?agent=${encodeURIComponent(agentId)}`);
    }
    return routerGet(this.baseUrl, "/capabilities");
  }

  // --- Tasks (W6.4, MADS-UX §3.3) ---
  async listTasks(sessionId: string, status?: TaskStatus): Promise<Task[]> {
    return routerGet(this.baseUrl, "/tasks", { session_id: sessionId, status });
  }

  async getTask(id: string): Promise<Task> {
    return routerGet(this.baseUrl, `/tasks/${id}`);
  }

  async createTask(task: Partial<Task>): Promise<Task> {
    return routerPost(this.baseUrl, "/tasks", task);
  }

  async updateTask(task: Task): Promise<Task> {
    return routerPut(this.baseUrl, `/tasks/${task.id}`, task);
  }

  async confirmTask(id: string): Promise<void> {
    return routerPost(this.baseUrl, `/tasks/${id}/confirm`);
  }

  async discardTask(id: string): Promise<void> {
    return routerPost(this.baseUrl, `/tasks/${id}/discard`);
  }

  async importTasks(tasks: Partial<Task>[]): Promise<Task[]> {
    const body = await routerPost<{ tasks: Task[] }>(this.baseUrl, "/tasks/import", { tasks });
    return body.tasks;
  }

  async taskCycles(sessionId: string): Promise<string[][]> {
    const body = await routerGet<{ cycles: string[][] }>(this.baseUrl, "/tasks/cycles", {
      session_id: sessionId,
    });
    return body.cycles;
  }

  async addNote(taskId: string, note: { type: NoteType; content: string }): Promise<Note> {
    return routerPost(this.baseUrl, `/tasks/${taskId}/notes`, note);
  }

  async confirmNote(taskId: string, noteId: number): Promise<void> {
    return routerPost(this.baseUrl, `/tasks/${taskId}/notes/${noteId}/confirm`);
  }

  async discardNote(taskId: string, noteId: number): Promise<void> {
    return routerPost(this.baseUrl, `/tasks/${taskId}/notes/${noteId}/discard`);
  }

  async getSessionChanges(sessionId: string): Promise<FileChange[]> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/files`);
  }

  streamTaskEvents(sessionId: string): SessionEventSource {
    const q = new URLSearchParams({ session_id: sessionId, types: "task.*,note.*" });
    return new EventSource(`${this.baseUrl}/events?${q.toString()}`);
  }

  // Files tab / merge-back flow (W6.5) — see ui/src/files/HANDOFF.md §1/§3
  // for which of these are real Router routes today (only
  // streamControlEvents is) vs. documented-but-unimplemented contracts this
  // client is built ahead of.
  async getFileChanges(sessionId: string, taskId?: string): Promise<FileChange[]> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/files`, { task_id: taskId });
  }

  async getCommits(sessionId: string): Promise<Commit[]> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/commits`);
  }

  // No diff-content endpoint exists anywhere yet (HANDOFF §1) — hits the
  // MADS-UX-named path so it's a rename, not a redesign, once one lands.
  async getFileDiff(sessionId: string, path: string, taskId?: string): Promise<FileDiff> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/changes`, { path, task_id: taskId });
  }

  // MADS-UX §3.3's open-in-editor action; no `/system/open-editor` route
  // exists anywhere in the codebase yet (HANDOFF §1).
  async openInEditor(path: string): Promise<void> {
    return routerPost(this.baseUrl, "/system/open-editor", { path });
  }

  // Only the per-session, unproxied `POST /merge` exists today (HANDOFF
  // §1); this hits the Router-proxied path MADS-UX §3.3's own "Actions"
  // line documents.
  async mergeSession(sessionId: string, targetBranch?: string): Promise<MergeResult> {
    return routerPost(this.baseUrl, `/sessions/${sessionId}/merge`, {
      target_branch: targetBranch,
    });
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

  // --- Sessions (session-agnostic) ---
  async listSessions(): Promise<Session[]> {
    return routerGet(this.baseUrl, "/sessions");
  }

  async createSession(
    payload: Partial<Session>,
  ): Promise<{ session: Session; addr: string }> {
    return routerPost(this.baseUrl, "/sessions", payload);
  }

  // --- Config ---
  async getConfig(): Promise<Config> {
    return routerGet(this.baseUrl, "/config");
  }

  async updateConfig(config: Config): Promise<Config> {
    return routerPut(this.baseUrl, "/config", config);
  }

  // --- Profiles ---
  async listProfiles(): Promise<Profile[]> {
    return routerGet(this.baseUrl, "/profiles");
  }

  async createProfile(profile: Profile): Promise<Profile> {
    return routerPost(this.baseUrl, "/profiles", profile);
  }

  async updateProfile(profile: Profile): Promise<Profile> {
    return routerPut(this.baseUrl, `/profiles/${profile.id}`, profile);
  }

  async deleteProfile(id: string): Promise<void> {
    return routerDelete(this.baseUrl, `/profiles/${id}`);
  }

  async activateProfile(id: string): Promise<Config> {
    return routerPost(this.baseUrl, `/profiles/${id}/activate`);
  }

  // --- Credentials ---
  async listCredentials(): Promise<string[]> {
    return routerGet(this.baseUrl, "/credentials");
  }

  async createCredential(name: string, value: string): Promise<{ name: string }> {
    return routerPost(this.baseUrl, "/credentials", { name, value });
  }

  async deleteCredential(name: string): Promise<void> {
    return routerDelete(this.baseUrl, `/credentials/${encodeURIComponent(name)}`);
  }

  async listCredentialEnvCandidates(): Promise<string[]> {
    return routerGet(this.baseUrl, "/credentials/env-candidates");
  }

  async importCredentials(names: string[]): Promise<{ imported: string[] }> {
    return routerPost(this.baseUrl, "/credentials/import", { names });
  }

  // --- Preflight ---
  async getPreflightChecks(): Promise<PreflightCheck[]> {
    return routerGet(this.baseUrl, "/preflight");
  }

  // --- Providers / behaviors / models ---
  async listProviders(): Promise<ProviderInfo[]> {
    return routerGet(this.baseUrl, "/providers");
  }

  async listProviderNames(): Promise<Provider[]> {
    // GET /providers returns ProviderInfo objects, not bare names — derive.
    // (Typing the raw response as string[] hid a live [object Object] URL
    // bug in the model pill; the wire shape is the objects.)
    const infos = await routerGet<ProviderInfo[]>(this.baseUrl, "/providers");
    return infos.map((p) => p.name);
  }

  async listProviderBehaviors(): Promise<BehaviorInfo[]> {
    return routerGet(this.baseUrl, "/providers/behaviors");
  }

  async listModels(provider: string): Promise<Model[]> {
    return routerGet(this.baseUrl, `/providers/${provider}/models`);
  }

  // --- Metrics (W6.9 Dashboard) ---
  async metricsCost(): Promise<CostMetrics> {
    return routerGet(this.baseUrl, "/metrics/cost");
  }

  async metricsAgentLeaderboard(): Promise<AgentLeaderboardEntry[]> {
    return routerGet(this.baseUrl, "/metrics/agents");
  }

  async metricsTaskOutcomes(): Promise<TaskOutcomes> {
    return routerGet(this.baseUrl, "/metrics/tasks");
  }

  async metricsCapabilityUsage(): Promise<CapabilityUsage[]> {
    return routerGet(this.baseUrl, "/metrics/capabilities");
  }

  async metricsPipelineRuns(): Promise<PipelineRun[]> {
    return routerGet(this.baseUrl, "/metrics/pipelines");
  }

  // --- Templates (Library) ---
  async listTemplates(): Promise<Template[]> {
    return routerGet(this.baseUrl, "/templates");
  }

  async createTemplate(t: Template): Promise<Template> {
    return routerPost(this.baseUrl, "/templates", t);
  }

  async updateTemplate(t: Template): Promise<Template> {
    return routerPut(this.baseUrl, `/templates/${t.id}`, t);
  }

  async deleteTemplate(id: string): Promise<void> {
    return routerDelete(this.baseUrl, `/templates/${id}`);
  }

  async validateTemplate(source: string): Promise<{ valid: boolean }> {
    return routerPost(this.baseUrl, "/templates/validate", { source });
  }

  // --- Workspaces (Library) ---
  async listWorkspaces(): Promise<Workspace[]> {
    return routerGet(this.baseUrl, "/workspaces");
  }

  async createWorkspace(w: Partial<Workspace>): Promise<Workspace> {
    return routerPost(this.baseUrl, "/workspaces", w);
  }

  async updateWorkspace(w: Workspace): Promise<Workspace> {
    return routerPut(this.baseUrl, `/workspaces/${w.id}`, w);
  }

  async deleteWorkspace(id: string): Promise<void> {
    return routerDelete(this.baseUrl, `/workspaces/${id}`);
  }

  // --- Agents (Library) ---
  async listAgents(): Promise<Agent[]> {
    return routerGet(this.baseUrl, "/agents");
  }

  async createAgent(a: Partial<Agent>): Promise<Agent> {
    return routerPost(this.baseUrl, "/agents", a);
  }

  async updateAgent(a: Agent): Promise<Agent> {
    return routerPut(this.baseUrl, `/agents/${a.id}`, a);
  }

  async deleteAgent(id: string): Promise<void> {
    return routerDelete(this.baseUrl, `/agents/${id}`);
  }

  async generateAgent(body: GenerateAgentBody): Promise<{ job_id: string }> {
    return routerPost(this.baseUrl, "/agents/generate", body);
  }

  async describeAgent(prompt: string): Promise<{ result: string }> {
    return routerPost(this.baseUrl, "/agents/describe", { prompt });
  }

  async suggestAgentCapabilities(prompt: string): Promise<AgentCapabilities> {
    return routerPost(this.baseUrl, "/agents/suggest-capabilities", { prompt });
  }

  async refineAgent(prompt: string, instruction: string): Promise<{ result: string }> {
    return routerPost(this.baseUrl, "/agents/refine", { prompt, instruction });
  }

  // --- Capabilities (Library) ---
  async listCapabilities(): Promise<Capability[]> {
    return routerGet(this.baseUrl, "/capabilities");
  }

  async getCapability(id: string): Promise<Capability> {
    return routerGet(this.baseUrl, `/capabilities/${id}`);
  }

  async createCapability(c: Partial<Capability>): Promise<Capability> {
    return routerPost(this.baseUrl, "/capabilities", c);
  }

  async updateCapability(c: Capability): Promise<Capability> {
    return routerPut(this.baseUrl, `/capabilities/${c.id}`, c);
  }

  async deleteCapability(id: string): Promise<void> {
    return routerDelete(this.baseUrl, `/capabilities/${id}`);
  }

  async diagnoseCapability(id: string): Promise<DiagnoseResult> {
    return routerPost(this.baseUrl, `/capabilities/${id}/diagnose`);
  }

  // --- Tasks (session-agnostic) ---
  async listAllTasks(status?: TaskStatus): Promise<Task[]> {
    return routerGet(this.baseUrl, "/tasks", { status });
  }

  // --- Retro ---
  async listRetros(): Promise<Retro[]> {
    return routerGet(this.baseUrl, "/retros");
  }

  async getRetro(id: string): Promise<Retro> {
    return routerGet(this.baseUrl, `/retros/${id}`);
  }

  async startRetro(
    sessionIds: string[],
    userObservations?: string,
  ): Promise<{ job_id: string; retro_id: string }> {
    return routerPost(this.baseUrl, "/retros", {
      session_ids: sessionIds,
      user_observations: userObservations,
    });
  }

  async acceptDpa(retroId: string, dpaId: string): Promise<void> {
    return routerPost(this.baseUrl, `/retros/${retroId}/dpas/${dpaId}/accept`);
  }

  async rejectDpa(retroId: string, dpaId: string): Promise<void> {
    return routerPost(this.baseUrl, `/retros/${retroId}/dpas/${dpaId}/reject`);
  }

  async createRetroFollowUpSession(
    retroId: string,
  ): Promise<{ session: Session; addr: string; tasks: Task[] }> {
    return routerPost(this.baseUrl, `/retros/${retroId}/follow-up-session`);
  }

  // --- Jobs ---
  async cancelJob(jobId: string): Promise<void> {
    return routerPost(this.baseUrl, `/jobs/${jobId}/cancel`);
  }

  // --- Pipeline ---
  async createPipelineRun(
    sessionId: string,
    templateId: string,
  ): Promise<{ run_id: string }> {
    return routerPost(this.baseUrl, "/pipelines", {
      session_id: sessionId,
      template_id: templateId,
    });
  }

  async cancelPipelineRun(runId: string): Promise<void> {
    return routerPost(this.baseUrl, `/pipelines/${runId}/cancel`);
  }

  async retryTask(taskId: string): Promise<Task> {
    return routerPost(this.baseUrl, `/tasks/${taskId}/retry`);
  }
}

/**
 * Shared singleton used by call sites that don't already receive a
 * `RouterClient` via props/constructor injection.
 */
export const router: RouterClient = new FetchRouterClient(ROUTER_BASE);
