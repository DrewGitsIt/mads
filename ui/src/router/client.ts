import type {
  Attachment,
  Delegate,
  DelegateTranscript,
  DelegateState,
  AttachmentOwner,
  Agent,
  CapabilitySuggestion,
  AgentLeaderboardEntry,
  BehaviorInfo,
  Capability,
  CapabilitySummary,
  CapabilityUsage,
  Commit,
  CommitDraft,
  CommitError,
  CommitRequest,
  CommitResult,
  Config,
  CostMetrics,
  CreateSessionPayload,
  DiagnoseResult,
  ConversationImportResult,
  ConversationImportScan,
  EnvCandidates,
  Envelope,
  FastModelStatus,
  FileChange,
  FileDiff,
  GenerateAgentBody,
  Link,
  LinkRequest,
  LandResult,
  Mentionable,
  Model,
  ModelCatalog,
  Note,
  NoteType,
  PermissionRequest,
  PipelineRun,
  PreflightCheck,
  Profile,
  Proposal,
  Provider,
  ProviderInfo,
  RegistryEntry,
  RegistryItemType,
  RegistryPublishItem,
  Retro,
  Session,
  SessionDefaults,
  QueuedMessage,
  SendResult,
  SessionTrust,
  SetSessionModelResponse,
  Task,
  TaskOutcomes,
  TaskStatus,
  Template,
  TemplateValidationResult,
  ToolDiff,
  TranscriptMessage,
  UndoCommitResult,
  UpdateCheckResult,
  VersionInfo,
  Workspace,
  WorktreeStatus,
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
  /**
   * `queue: true` asks the session to hold a message sent mid-turn and
   * deliver it when the turn ends (resolving `{status: "queued"}`) rather
   * than refuse it with 409. The queue lives in the session process, so it
   * outlives this pane.
   */
  sendMessage(
    sessionId: string,
    body: { text: string; mentions?: string[]; queue?: boolean; attachments?: string[] },
  ): Promise<SendResult>;
  /**
   * Streams a pasted, dropped or picked file to the Router's attachment
   * store (services/router/internal/attachments). A message names it by id
   * (`sendMessage`'s `attachments`) and the agent is handed its path.
   */
  uploadAttachment(owner: AttachmentOwner, file: Blob, name: string): Promise<Attachment>;
  /** Drops an attachment that was staged but never sent. */
  deleteAttachment(owner: AttachmentOwner, attachmentId: string): Promise<void>;
  /** Where the original file, or its thumbnail, is served. */
  attachmentUrl(owner: AttachmentOwner, attachmentId: string, variant?: "thumbnail"): string;
  /** The session's queued messages, in delivery order. */
  listQueuedMessages(sessionId: string): Promise<QueuedMessage[]>;
  /** Drop one queued message before it's delivered. */
  cancelQueuedMessage(sessionId: string, queuedId: string): Promise<void>;
  interrupt(sessionId: string): Promise<void>;
  /**
   * Run a native context-compaction pass now (the "Compact now" banner
   * action). Resolves once the request is accepted; progress and outcome
   * arrive as `compact` stream events, same as a resume-triggered pass.
   */
  compactSession(sessionId: string): Promise<void>;
  bounce(sessionId: string): Promise<void>;

  // --- Chat/Terminal transport handoff (single provider conversation,
  // two transports, never both at once — see ui/src/conversation/HANDOFF.md) ---
  switchTransport(
    sessionId: string,
    mode: "chat" | "terminal",
  ): Promise<{ mode: string; ref: string }>;
  // The read side of the same handoff — the session's current live mode,
  // straight from the running child (single source of truth; the Router
  // proxy answers "chat" when there's no live child/transport history at
  // all). Used to initialize the display-mode toggle on mount, never as a
  // trigger for a switch.
  getTransportMode(sessionId: string): Promise<{ mode: "chat" | "terminal" }>;
  // Interactive permission prompts (Allow/Deny cards, chat transport only).
  // getPermissionRequests reconstructs any still-open card on transcript
  // refetch/mount (a resolved one is already known from the live stream).
  getPermissionRequests(sessionId: string): Promise<PermissionRequest[]>;
  resolvePermissionRequest(
    sessionId: string,
    requestId: string,
    decision: "allow" | "deny",
  ): Promise<void>;
  terminalWrite(sessionId: string, data: string): Promise<void>;
  terminalResize(sessionId: string, cols: number, rows: number): Promise<void>;
  closeSession(sessionId: string): Promise<void>;
  resumeSession(sessionId: string): Promise<{ addr: string }>;
  renameSession(id: string, name: string): Promise<Session>;
  /** Fold a session out of the rail's main list (or bring it back). Touches no child. */
  setSessionAside(id: string, aside: boolean): Promise<Session>;
  setSessionTrust(id: string, trust: SessionTrust): Promise<Session>;
  setSessionModel(id: string, model: string): Promise<SetSessionModelResponse>;
  deleteSession(id: string): Promise<void>;
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
  // Draft-chip source for the task editor (pkg/capinfer, call site A) — never
  // persists, caller must confirm into Task.required_capabilities via updateTask.
  suggestTaskCapabilities(title: string, description: string, workspace?: string): Promise<string[]>;
  addNote(taskId: string, note: { type: NoteType; content: string }): Promise<Note>;
  confirmNote(taskId: string, noteId: number): Promise<void>;
  discardNote(taskId: string, noteId: number): Promise<void>;
  getSessionChanges(sessionId: string): Promise<FileChange[]>;

  // --- Synthesis proposals (spec §4) ---
  listProposals(sessionId: string): Promise<Proposal[]>;
  approveProposal(id: string): Promise<Proposal>;
  dismissProposal(id: string): Promise<Proposal>;

  // Files tab (W6.5) — see ui/src/files/HANDOFF.md §1/§3
  // for which of these are real Router routes today vs.
  // documented-but-unimplemented contracts this client is built ahead of.
  getFileChanges(sessionId: string, taskId?: string): Promise<FileChange[]>;
  getCommits(sessionId: string): Promise<Commit[]>;
  getFileDiff(sessionId: string, path: string, taskId?: string): Promise<FileDiff>;

  getCommitFileDiff(sessionId: string, hash: string, path: string): Promise<FileDiff>;
  /**
   * One mutating tool call's historically-accurate before/after from the
   * shadow store (docs/SHADOW-GIT-DIFFS-ADR.md). Resolves null on 404 —
   * the documented "no capture recorded, keep your input-derived diff"
   * answer, expected for old sessions and non-captured calls.
   */
  getToolDiff(sessionId: string, toolUseId: string): Promise<ToolDiff | null>;
  openInEditor(path: string): Promise<void>;

  // Commit and Land (docs/SESSION-WORKTREES-DESIGN.md D5, D6).
  /** Runs git status server-side — call when a dialog opens or at confirm time, never per render. */
  getWorktreeStatus(sessionId: string): Promise<WorktreeStatus>;
  /** Throws CommitRefusedError for a structured refusal (invalid_paths, git_failed, …). */
  commitSession(sessionId: string, req: CommitRequest): Promise<CommitResult>;
  /** Resolves for a refusal too (undone: false, with its reason) — only an unexpected failure throws. */
  undoCommit(sessionId: string, hash: string): Promise<UndoCommitResult>;
  /** Throws CommitRefusedError (no_fast_model, draft_failed, …). */
  draftCommitMessage(sessionId: string, paths?: string[]): Promise<CommitDraft>;
  /** Landed, conflicted and refused all resolve — each is a card. */
  landSession(sessionId: string): Promise<LandResult>;

  // Batch (non-streaming) read of GET /events — a plain fetch (no explicit
  // Accept header) takes the Router's JSON branch instead of the SSE branch
  // (Pipeline tab HANDOFF.md §2/§3). Used to replay historical pipeline runs.
  getEvents(filter: { sessionId: string; since?: number; types?: string[] }): Promise<Envelope[]>;

  // --- Sessions (session-agnostic) ---
  listSessions(): Promise<Session[]>;
  createSession(
    payload: CreateSessionPayload,
  ): Promise<{ session: Session; addr: string; links?: Link[] }>;

  // --- Config ---
  getConfig(): Promise<Config>;
  updateConfig(config: Config): Promise<Config>;
  getFastModelStatus(): Promise<FastModelStatus>;

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
  listCredentialEnvCandidates(): Promise<EnvCandidates>;
  importCredentials(names: string[]): Promise<{ imported: string[] }>;

  // --- Conversation import ---
  scanConversationImports(): Promise<ConversationImportScan>;
  importConversations(): Promise<ConversationImportResult>;

  // --- Preflight ---
  getPreflightChecks(): Promise<PreflightCheck[]>;

  // --- Update (W7.5 §14.3) ---
  getVersion(): Promise<VersionInfo>;
  checkForUpdate(): Promise<UpdateCheckResult>;

  // --- Providers / behaviors / models ---
  listProviders(): Promise<ProviderInfo[]>;
  listProviderNames(): Promise<Provider[]>;
  listProviderBehaviors(): Promise<BehaviorInfo[]>;
  listModels(provider: string): Promise<Model[]>;
  // listModelCatalog is listModels plus the discovery state — what the
  // picker needs to distinguish "still discovering" from "couldn't ask"
  // from "genuinely no models".
  listModelCatalog(provider: string): Promise<ModelCatalog>;
  refreshModelCatalog(provider: string): Promise<ModelCatalog>;

  // --- Metrics (W6.9 Dashboard) ---
  metricsCost(): Promise<CostMetrics>;
  metricsAgentLeaderboard(): Promise<AgentLeaderboardEntry[]>;
  metricsTaskOutcomes(): Promise<TaskOutcomes>;
  metricsCapabilityUsage(): Promise<CapabilityUsage[]>;
  metricsPipelineRuns(sessionId?: string): Promise<PipelineRun[]>;

  // --- Templates (Library) ---
  listTemplates(): Promise<Template[]>;
  createTemplate(t: Template): Promise<Template>;
  updateTemplate(t: Template): Promise<Template>;
  deleteTemplate(id: string): Promise<void>;
  validateTemplate(source: string): Promise<{ valid: boolean }>;
  /**
   * POST /templates/validate-all — a one-shot sweep used to badge invalid
   * templates in the shelf. Callers must treat a rejection as "no badges
   * this session", not as a reason to hide the template list.
   */
  validateAllTemplates(): Promise<TemplateValidationResult[]>;

  // --- Workspaces (Library) ---
  listWorkspaces(): Promise<Workspace[]>;
  createWorkspace(w: Partial<Workspace>): Promise<Workspace>;
  updateWorkspace(w: Workspace): Promise<Workspace>;
  deleteWorkspace(id: string): Promise<void>;

  // --- Session defaults (Launchpad preview) ---
  getSessionDefaults(workspace?: string, dir?: string): Promise<SessionDefaults>;

  // --- Agents (Library) ---
  listAgents(): Promise<Agent[]>;
  createAgent(a: Partial<Agent>): Promise<Agent>;
  updateAgent(a: Agent): Promise<Agent>;
  deleteAgent(id: string): Promise<void>;
  generateAgent(body: GenerateAgentBody): Promise<{ job_id: string }>;
  describeAgent(prompt: string): Promise<{ result: string }>;
  suggestAgentCapabilities(prompt: string): Promise<CapabilitySuggestion>;
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

  // --- Registry (Library, flag-gated — W7.6) ---
  registryIndex(q?: string): Promise<RegistryEntry[]>;
  installRegistryItem(
    type: RegistryItemType,
    name: string,
    version?: string,
  ): Promise<{ job_id: string }>;
  updateRegistryItem(
    type: RegistryItemType,
    name: string,
    version?: string,
  ): Promise<{ job_id: string }>;
  publishRegistryItems(items: RegistryPublishItem[]): Promise<{ job_id: string }>;

  // --- Delegates (docs/DELEGATES-DESIGN.md) ---
  /** Every session's delegates, only those in `state` when given — the rail's cold read is "running". */
  listDelegates(state?: DelegateState): Promise<Delegate[]>;
  /** One session's delegates, oldest first. */
  getSessionDelegates(sessionId: string): Promise<Delegate[]>;
  /** One delegate's own transcript, read from its harness's store. */
  getDelegateTranscript(sessionId: string, harnessId: string): Promise<DelegateTranscript>;

  // --- Linked sessions (flag-gated, docs/LINKED-SESSIONS-DESIGN.md) ---
  getSessionLinks(sessionId: string): Promise<Link[]>;
  createLink(req: LinkRequest): Promise<Link>;
  updateLink(id: string, req: LinkRequest): Promise<Link>;
  deleteLink(id: string): Promise<void>;
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

/**
 * Thrown by `switchTransport` on a non-2xx from `POST /transport`, whose
 * body is `{error, code, retryable}` (services/session's `writeSwitchError`).
 * `code` is what lets `ConversationTab` distinguish "mid-turn, offer
 * interrupt-then-retry" (`busy`) from "old transport is dead, resume
 * failed" (`resume_failed`) without string-matching the message.
 */
export class TransportSwitchError extends Error {
  constructor(
    message: string,
    readonly code: "busy" | "resume_failed" | "not_ready",
    readonly retryable: boolean,
  ) {
    super(message);
  }
}

/**
 * Thrown by `sendMessage` on a non-2xx from `POST /messages` whose body
 * carries a `code` (services/router's wakeSession/writeWakeError) — i.e. the
 * session had no live child and the wake-then-deliver path failed before
 * ever reaching the child. Lets ConversationTab show a specific reason
 * ("provider_missing" vs "spawn_failed") instead of a bare error string.
 */
export class SessionWakeError extends Error {
  constructor(
    message: string,
    readonly code: string,
    readonly retryable: boolean,
  ) {
    super(message);
  }
}

/**
 * Thrown by `commitSession`/`draftCommitMessage` for a structured refusal
 * (pkg/contracts CommitError): the Commit dialog renders `body` in place —
 * git's verbatim output, per-path reasons — and keeps the selection.
 */
export class CommitRefusedError extends Error {
  constructor(
    readonly body: CommitError,
    readonly status: number,
  ) {
    super(body.error);
  }
}

/** A Commit route's answer: the body on 2xx, CommitRefusedError for a `{code, error}` refusal. */
async function readCommitJSON<T>(res: Response): Promise<T> {
  if (!res.ok) {
    const body = (await res
      .clone()
      .json()
      .catch(() => null)) as CommitError | null;
    if (body?.code) throw new CommitRefusedError(body, res.status);
  }
  return readJSON<T>(res);
}

// Thin fetch wrapper shared by every surface — no ad-hoc fetches in
// components. The Router's address is exposed via VITE_ROUTER_URL; the
// default matches the Router's own `-addr` flag default (localhost:17373).
export const ROUTER_BASE =
  (import.meta.env.VITE_ROUTER_URL as string | undefined) ?? "http://localhost:17373";

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

// ModelsResponseWire mirrors contracts.ModelsResponse. state/fetched_at are
// absent when the Router answered without its catalog cache, which is why
// both are optional here rather than defaulted.
interface ModelsResponseWire {
  models: Model[] | null;
  detail?: string;
  state?: ModelCatalog["state"];
  fetched_at?: string;
}

function toCatalog(res: ModelsResponseWire): ModelCatalog {
  return {
    models: res.models ?? [],
    state: res.state,
    detail: res.detail,
    fetchedAt: res.fetched_at,
  };
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

function attachmentsPath(owner: AttachmentOwner): string {
  return "session" in owner
    ? `/sessions/${encodeURIComponent(owner.session)}/attachments`
    : `/attachment-drafts/${encodeURIComponent(owner.draft)}/attachments`;
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

  // The child's live data-plane event feed (services/session's
  // GET /output, proxied byte-transparently by the Router) — this is also
  // the channel PTY-mode terminal output flows through post-switch, as its
  // own "pty_output" event kind (distinct from chat's "text"). A fresh
  // attach replays the session's bounded server-side PTY buffer as a
  // leading pty_output frame before live bytes (services/session/README.md),
  // so reattaching restores terminal scrollback.
  streamSession(sessionId: string): SessionEventSource {
    return new EventSource(`${this.baseUrl}/sessions/${sessionId}/output`);
  }

  async sendMessage(
    sessionId: string,
    body: { text: string; mentions?: string[]; queue?: boolean; attachments?: string[] },
  ): Promise<SendResult> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      // Parse the clone, like readJSON: the Router's standard error shape
      // ({error, path}) has no `code`, and falling through to res.text()
      // after res.json() consumed the body throws "body stream already
      // read" — masking the actual error.
      const errBody = (await res
        .clone()
        .json()
        .catch(() => null)) as {
        error?: string;
        code?: string;
        retryable?: boolean;
      } | null;
      if (errBody?.code) {
        throw new SessionWakeError(
          errBody.error ?? `router request failed: ${res.status}`,
          errBody.code,
          errBody.retryable ?? false,
        );
      }
      if (res.status === 409 && body.queue) {
        // Only a session process predating the queue refuses a queued
        // send mid-turn; it goes away once that session restarts.
        throw new Error(
          "The agent is mid-turn, and this session is running an older mads build that can't queue messages. Wait for the turn to finish, or restart the session.",
        );
      }
      const text = await res.text();
      throw new Error(`router request failed: ${res.status} ${text}`);
    }
    const result = await readJSON<Partial<SendResult> | null>(res);
    return result?.status === "queued" && "queued" in result
      ? (result as SendResult)
      : { status: "accepted" };
  }

  async uploadAttachment(owner: AttachmentOwner, file: Blob, name: string): Promise<Attachment> {
    // The raw file is the body — never a base64 or multipart envelope — so
    // the browser streams it from its own blob storage and the Router
    // writes it straight to disk.
    const res = await fetch(buildUrl(this.baseUrl, attachmentsPath(owner), { name }), {
      method: "POST",
      headers: { "Content-Type": "application/octet-stream" },
      body: file,
    });
    return readJSON<Attachment>(res);
  }

  async deleteAttachment(owner: AttachmentOwner, attachmentId: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}${attachmentsPath(owner)}/${attachmentId}`, {
      method: "DELETE",
    });
    if (!res.ok && res.status !== 404) {
      throw new Error(`router request failed: ${res.status} ${await res.text()}`);
    }
  }

  attachmentUrl(owner: AttachmentOwner, attachmentId: string, variant?: "thumbnail"): string {
    const base = `${this.baseUrl}${attachmentsPath(owner)}/${attachmentId}`;
    return variant ? `${base}/${variant}` : base;
  }

  async listQueuedMessages(sessionId: string): Promise<QueuedMessage[]> {
    const res = await routerGet<{ queued?: QueuedMessage[] }>(
      this.baseUrl,
      `/sessions/${sessionId}/queue`,
    );
    return res.queued ?? [];
  }

  async cancelQueuedMessage(sessionId: string, queuedId: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/queue/${queuedId}`, {
      method: "DELETE",
    });
    // 404: already delivered (or the session stopped) — nothing left to cancel.
    if (!res.ok && res.status !== 404) {
      throw new Error(`router request failed: ${res.status} ${await res.text()}`);
    }
  }

  async interrupt(sessionId: string): Promise<void> {
    return routerPost(this.baseUrl, `/sessions/${sessionId}/interrupt`);
  }

  async compactSession(sessionId: string): Promise<void> {
    return routerPost(this.baseUrl, `/sessions/${sessionId}/compact`);
  }

  async bounce(sessionId: string): Promise<void> {
    return routerPost(this.baseUrl, `/sessions/${sessionId}/bounce`);
  }

  async switchTransport(
    sessionId: string,
    mode: "chat" | "terminal",
  ): Promise<{ mode: string; ref: string }> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/transport`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode }),
    });
    if (!res.ok) {
      const body = (await res.json().catch(() => null)) as {
        error?: string;
        code?: "busy" | "resume_failed" | "not_ready";
        retryable?: boolean;
      } | null;
      throw new TransportSwitchError(
        body?.error ?? `router request failed: ${res.status}`,
        body?.code ?? "resume_failed",
        body?.retryable ?? false,
      );
    }
    return (await res.json()) as { mode: string; ref: string };
  }

  async getTransportMode(sessionId: string): Promise<{ mode: "chat" | "terminal" }> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/transport`);
  }

  async getPermissionRequests(sessionId: string): Promise<PermissionRequest[]> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/permission-requests`);
  }

  async resolvePermissionRequest(
    sessionId: string,
    requestId: string,
    decision: "allow" | "deny",
  ): Promise<void> {
    await routerPost(
      this.baseUrl,
      `/sessions/${sessionId}/permission-requests/${requestId}/resolve`,
      { decision },
    );
  }

  // Raw bytes, not JSON-wrapped — the child's POST /input reads the
  // request body directly and writes it to the pty.
  async terminalWrite(sessionId: string, data: string): Promise<void> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/input`, {
      method: "POST",
      body: data,
    });
    await readJSON(res);
  }

  async terminalResize(sessionId: string, cols: number, rows: number): Promise<void> {
    return routerPost(this.baseUrl, `/sessions/${sessionId}/resize`, { cols, rows });
  }

  async closeSession(sessionId: string): Promise<void> {
    return routerPost(this.baseUrl, `/sessions/${sessionId}/terminate`);
  }

  async resumeSession(sessionId: string): Promise<{ addr: string }> {
    return routerPost(this.baseUrl, `/sessions/${sessionId}/resume`);
  }

  async renameSession(id: string, name: string): Promise<Session> {
    return routerPost(this.baseUrl, `/sessions/${id}/rename`, { name });
  }

  // Records the user's judgement about a session, not anything the session
  // did — so it persists on the record rather than in this browser, and
  // deliberately leaves the running child alone.
  async setSessionAside(id: string, aside: boolean): Promise<Session> {
    return routerPost(this.baseUrl, `/sessions/${id}/aside`, { aside });
  }

  async setSessionTrust(id: string, trust: SessionTrust): Promise<Session> {
    // Trust is spawn-time-only in the runner, so the Router bounces a live
    // child as part of this call — an in-flight turn is interrupted.
    return routerPost(this.baseUrl, `/sessions/${id}/trust`, { trust });
  }

  async setSessionModel(id: string, model: string): Promise<SetSessionModelResponse> {
    // Unlike trust this never bounces the session: the Router persists the
    // model, then the child cycles its provider process in place (same ref,
    // conversation intact) — or queues the cycle behind an in-flight turn and
    // reports queued: true.
    return routerPost(this.baseUrl, `/sessions/${id}/model`, { model });
  }

  async deleteSession(id: string): Promise<void> {
    return routerDelete(this.baseUrl, `/sessions/${id}`);
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

  async suggestTaskCapabilities(
    title: string,
    description: string,
    workspace?: string,
  ): Promise<string[]> {
    const body = await routerPost<{ capabilities: string[] }>(
      this.baseUrl,
      "/tasks/suggest-capabilities",
      { title, description, workspace },
    );
    return body.capabilities;
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

  // --- Synthesis proposals (spec §4) ---
  async listProposals(sessionId: string): Promise<Proposal[]> {
    return routerGet(this.baseUrl, "/proposals", { session_id: sessionId });
  }

  async approveProposal(id: string): Promise<Proposal> {
    return routerPost(this.baseUrl, `/proposals/${id}/approve`);
  }

  async dismissProposal(id: string): Promise<Proposal> {
    return routerPost(this.baseUrl, `/proposals/${id}/dismiss`);
  }

  // Files tab (W6.5) — see ui/src/files/HANDOFF.md §1/§3
  // for which of these are real Router routes today vs.
  // documented-but-unimplemented contracts this client is built ahead of.
  async getFileChanges(sessionId: string, taskId?: string): Promise<FileChange[]> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/files`, { task_id: taskId });
  }

  async getCommits(sessionId: string): Promise<Commit[]> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/commits`);
  }

  // Working-change diff (services/router/internal/api/working_diff.go):
  // HEAD-vs-disk for a file in the session's file-change ledger.
  async getFileDiff(sessionId: string, path: string, taskId?: string): Promise<FileDiff> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/changes`, { path, task_id: taskId });
  }

  async getCommitFileDiff(sessionId: string, hash: string, path: string): Promise<FileDiff> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/commits/${hash}/diff`, { path });
  }

  async getToolDiff(sessionId: string, toolUseId: string): Promise<ToolDiff | null> {
    const res = await fetch(
      buildUrl(this.baseUrl, `/sessions/${sessionId}/tool-diff`, { tool_use_id: toolUseId }),
    );
    // 404 is the route's documented degrade signal (no shadow store / no
    // capture for this call) — "keep the input-derived diff", not an error.
    if (res.status === 404) return null;
    return readJSON<ToolDiff>(res);
  }

  // MADS-UX §3.3's open-in-editor action; no `/system/open-editor` route
  // exists anywhere in the codebase yet (HANDOFF §1).
  async openInEditor(path: string): Promise<void> {
    return routerPost(this.baseUrl, "/system/open-editor", { path });
  }

  async getWorktreeStatus(sessionId: string): Promise<WorktreeStatus> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/worktree`);
  }

  async commitSession(sessionId: string, req: CommitRequest): Promise<CommitResult> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/commit`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(req),
    });
    return readCommitJSON<CommitResult>(res);
  }

  async undoCommit(sessionId: string, hash: string): Promise<UndoCommitResult> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/commit/undo`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ hash }),
    });
    // A refusal is a 409 carrying the same shape, undone: false.
    if (res.status === 409) {
      const body = (await res
        .clone()
        .json()
        .catch(() => null)) as UndoCommitResult | null;
      if (body && body.undone === false) return body;
    }
    return readJSON<UndoCommitResult>(res);
  }

  async draftCommitMessage(sessionId: string, paths?: string[]): Promise<CommitDraft> {
    const res = await fetch(`${this.baseUrl}/sessions/${sessionId}/commit/draft`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(paths?.length ? { paths } : {}),
    });
    return readCommitJSON<CommitDraft>(res);
  }

  async landSession(sessionId: string): Promise<LandResult> {
    return routerPost(this.baseUrl, `/sessions/${sessionId}/land`, {});
  }

  async getEvents(filter: { sessionId: string; since?: number; types?: string[] }): Promise<Envelope[]> {
    return routerGet(this.baseUrl, "/events", {
      session_id: filter.sessionId,
      since: filter.since !== undefined ? String(filter.since) : undefined,
      types: filter.types?.length ? filter.types.join(",") : undefined,
    });
  }

  // --- Sessions (session-agnostic) ---
  async listSessions(): Promise<Session[]> {
    return routerGet(this.baseUrl, "/sessions");
  }

  async createSession(
    payload: CreateSessionPayload,
  ): Promise<{ session: Session; addr: string; links?: Link[] }> {
    return routerPost(this.baseUrl, "/sessions", payload);
  }

  // --- Config ---
  async getConfig(): Promise<Config> {
    return routerGet(this.baseUrl, "/config");
  }

  async updateConfig(config: Config): Promise<Config> {
    return routerPut(this.baseUrl, "/config", config);
  }

  async getFastModelStatus(): Promise<FastModelStatus> {
    return routerGet(this.baseUrl, "/fast-model/status");
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

  async listCredentialEnvCandidates(): Promise<EnvCandidates> {
    return routerGet(this.baseUrl, "/credentials/env-candidates");
  }

  async scanConversationImports(): Promise<ConversationImportScan> {
    return routerGet(this.baseUrl, "/imports/conversations");
  }

  async importConversations(): Promise<ConversationImportResult> {
    return routerPost(this.baseUrl, "/imports/conversations", {});
  }

  async importCredentials(names: string[]): Promise<{ imported: string[] }> {
    return routerPost(this.baseUrl, "/credentials/import", { names });
  }

  // --- Preflight ---
  async getPreflightChecks(): Promise<PreflightCheck[]> {
    return routerGet(this.baseUrl, "/preflight");
  }

  // --- Update (W7.5 §14.3) ---
  async getVersion(): Promise<VersionInfo> {
    return routerGet(this.baseUrl, "/version");
  }

  async checkForUpdate(): Promise<UpdateCheckResult> {
    return routerPost(this.baseUrl, "/update/check");
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
    return (await this.listModelCatalog(provider)).models;
  }

  async listModelCatalog(provider: string): Promise<ModelCatalog> {
    return toCatalog(
      await routerGet<ModelsResponseWire>(this.baseUrl, `/providers/${provider}/models`),
    );
  }

  async refreshModelCatalog(provider: string): Promise<ModelCatalog> {
    return toCatalog(
      await routerPost<ModelsResponseWire>(
        this.baseUrl,
        `/providers/${provider}/models/refresh`,
        {},
      ),
    );
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

  async metricsPipelineRuns(sessionId?: string): Promise<PipelineRun[]> {
    return routerGet(this.baseUrl, "/metrics/pipelines", { session_id: sessionId });
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

  async validateAllTemplates(): Promise<TemplateValidationResult[]> {
    const res = await routerPost<{ results: TemplateValidationResult[] }>(
      this.baseUrl,
      "/templates/validate-all",
    );
    return res.results;
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

  // --- Session defaults (Launchpad preview) ---
  async getSessionDefaults(workspace?: string, dir?: string): Promise<SessionDefaults> {
    return routerGet(this.baseUrl, "/sessions/defaults", { workspace, dir });
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

  async suggestAgentCapabilities(prompt: string): Promise<CapabilitySuggestion> {
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

  // --- Registry (Library, flag-gated — W7.6) ---
  async registryIndex(q?: string): Promise<RegistryEntry[]> {
    return routerGet(this.baseUrl, "/registry/index", { q });
  }

  async installRegistryItem(
    type: RegistryItemType,
    name: string,
    version?: string,
  ): Promise<{ job_id: string }> {
    return routerPost(this.baseUrl, "/registry/install", { type, name, version });
  }

  async updateRegistryItem(
    type: RegistryItemType,
    name: string,
    version?: string,
  ): Promise<{ job_id: string }> {
    return routerPost(this.baseUrl, "/registry/update", { type, name, version });
  }

  async publishRegistryItems(items: RegistryPublishItem[]): Promise<{ job_id: string }> {
    return routerPost(this.baseUrl, "/registry/publish", { items });
  }

  async getSessionDelegates(sessionId: string): Promise<Delegate[]> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/delegates`);
  }

  async listDelegates(state?: DelegateState): Promise<Delegate[]> {
    return routerGet(this.baseUrl, state ? `/delegates?state=${encodeURIComponent(state)}` : "/delegates");
  }

  async getDelegateTranscript(sessionId: string, harnessId: string): Promise<DelegateTranscript> {
    return routerGet(
      this.baseUrl,
      `/sessions/${sessionId}/delegates/${encodeURIComponent(harnessId)}/transcript`,
    );
  }

  async getSessionLinks(sessionId: string): Promise<Link[]> {
    return routerGet(this.baseUrl, `/sessions/${sessionId}/links`);
  }

  async createLink(req: LinkRequest): Promise<Link> {
    return routerPost(this.baseUrl, "/links", req);
  }

  async updateLink(id: string, req: LinkRequest): Promise<Link> {
    return routerPut(this.baseUrl, `/links/${id}`, req);
  }

  async deleteLink(id: string): Promise<void> {
    return routerDelete(this.baseUrl, `/links/${id}`);
  }
}

/**
 * Shared singleton used by call sites that don't already receive a
 * `RouterClient` via props/constructor injection.
 */
export const router: RouterClient = new FetchRouterClient(ROUTER_BASE);
