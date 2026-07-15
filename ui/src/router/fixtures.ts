import type { RouterClient, SessionEventSource } from "./client";
import { RouterValidationError } from "./client";
import type {
  CapabilitySummary,
  Envelope,
  FileChange,
  Mentionable,
  Note,
  NoteType,
  Session,
  Task,
  TaskStatus,
  TranscriptMessage,
} from "./types";

export const fixtureSessions: Record<string, Session> = {
  "session-active": {
    id: "session-active",
    name: "Add retry backoff to the ingest worker",
    activity: "active",
    workspace: "mads",
    provider: "claudecode",
    model: "claude-sonnet-5",
    agent: "autonomous-executor",
    worktree_branch: "mads/tsk_retry-backoff",
  },
  "session-inactive": {
    id: "session-inactive",
    name: "Investigate flaky metrics query test",
    activity: "inactive",
    workspace: "mads",
    provider: "claudecode",
    model: "claude-sonnet-5",
    agent: "investigator",
    worktree_branch: "mads/tsk_flaky-metrics",
    summary:
      "## Accomplished\n\nReproduced the flake locally by pinning the test clock; " +
      "traced it to a race between the query view refresh and the event log " +
      "writer.\n\n## Decisions\n\nAdd a `FOR UPDATE` fence around the refresh " +
      "rather than serializing writers globally.\n\n## Remaining\n\nWrite a " +
      "regression test that exercises the race under `-race`.",
  },
};

export const fixtureTranscripts: Record<string, TranscriptMessage[]> = {
  "session-active": [
    { role: "user", text: "Add retry backoff to the ingest worker, please." },
    {
      role: "agent",
      text:
        "Sure — I'll look at `services/data/internal` for the ingest path " +
        "and add exponential backoff around the retry loop.",
    },
    {
      role: "tool",
      tool: {
        name: "Read",
        path: "services/data/internal/ingest.go",
        input: { file_path: "services/data/internal/ingest.go" },
      },
    },
    {
      role: "tool",
      tool: {
        name: "Edit",
        path: "services/data/internal/ingest.go",
        input: {
          file_path: "services/data/internal/ingest.go",
          old_string: "time.Sleep(retryDelay)",
          new_string: "time.Sleep(backoff.Next())",
        },
      },
    },
    {
      role: "agent",
      text:
        "Done — retries now back off exponentially with jitter, capped at " +
        "30s. Ran `go test ./services/data/...` and it's green.",
    },
  ],
  "session-inactive": [
    { role: "user", text: "The metrics query test flakes about 1 in 20 runs." },
    {
      role: "agent",
      text: "I'll reproduce it under `-race` with a pinned clock first.",
    },
  ],
};

export const fixtureMentionables: Mentionable[] = [
  {
    id: "skill:code-review",
    kind: "skill_doc",
    label: "code-review",
    detail: "Review the current diff for correctness bugs",
  },
  {
    id: "skill:deep-research",
    kind: "skill_doc",
    label: "deep-research",
    detail: "Multi-source, fact-checked research report",
  },
  {
    id: "session:session-inactive",
    kind: "session_summary",
    label: "Investigate flaky metrics query test",
    detail: "session summary",
  },
  {
    id: "steering:go-style",
    kind: "steering_file",
    label: "go-style.md",
    detail: "workspace steering file",
  },
];

export const fixtureCapabilities: CapabilitySummary[] = [
  {
    id: "cap-code-review",
    name: "code-review",
    type: "skill",
    description: "Review the current diff for bugs and cleanups",
    healthy: true,
  },
  {
    id: "cap-verify",
    name: "verify",
    type: "skill",
    description: "Exercise a change end-to-end and observe behavior",
    healthy: true,
  },
  {
    id: "cap-github",
    name: "github",
    type: "mcp",
    description: "Issue/PR read + comment access",
    healthy: false,
  },
  {
    id: "cap-security-review",
    name: "security-review",
    type: "skill",
    description: "Adversarial review for OWASP-class issues",
    healthy: false,
  },
];

// mirrors pkg/contracts/task.go, covering every case a reviewer needs to see
// without a real backend (ui/src/tasks/HANDOFF.md §3): a plain ready task, an
// in_progress task, a dependency-queued blocked task (dep not done — renders
// calm), a needs-human blocked task (no deps — renders red), a needs_review
// task, a done task, one draft task with a draft note, and a small 2-task
// dependency cycle (for graph cycle-highlighting review).
export const fixtureTasks: Task[] = [
  {
    id: "tsk_ready",
    session_id: "session-active",
    status: "ready",
    labels: ["backend"],
    retry_count: 0,
    created_at: "2026-07-01T10:00:00Z",
    updated_at: "2026-07-01T10:00:00Z",
    title: "Add retry backoff to the ingest worker",
    description: "Exponential backoff with jitter, capped at 30s.",
    source: "user",
  },
  {
    id: "tsk_in_progress",
    session_id: "session-active",
    status: "in_progress",
    retry_count: 0,
    created_at: "2026-07-01T10:05:00Z",
    updated_at: "2026-07-01T10:05:00Z",
    title: "Write regression test for the retry race",
    source: "user",
  },
  {
    id: "tsk_dep_not_done",
    session_id: "session-active",
    status: "ready",
    retry_count: 0,
    created_at: "2026-07-01T09:00:00Z",
    updated_at: "2026-07-01T09:00:00Z",
    title: "Design the retry policy",
    source: "user",
  },
  {
    id: "tsk_queued",
    session_id: "session-active",
    status: "blocked",
    dependencies: ["tsk_dep_not_done"],
    retry_count: 0,
    created_at: "2026-07-01T10:10:00Z",
    updated_at: "2026-07-01T10:10:00Z",
    title: "Roll retry backoff out to the export worker",
    description: "Waiting on the ingest worker's policy to be finalized.",
    source: "user",
  },
  {
    id: "tsk_needs_human",
    session_id: "session-active",
    status: "blocked",
    retry_count: 2,
    created_at: "2026-07-01T09:30:00Z",
    updated_at: "2026-07-01T09:45:00Z",
    title: "Rotate the staging database credentials",
    description: "Blocked: needs a human to approve the credential rotation window.",
    source: "user",
  },
  {
    id: "tsk_needs_review",
    session_id: "session-active",
    status: "needs_review",
    retry_count: 0,
    created_at: "2026-07-01T08:00:00Z",
    updated_at: "2026-07-01T11:00:00Z",
    title: "Add exponential backoff config to the ingest worker",
    source: "user",
  },
  {
    id: "tsk_done",
    session_id: "session-active",
    status: "done",
    retry_count: 0,
    created_at: "2026-06-30T12:00:00Z",
    updated_at: "2026-06-30T18:00:00Z",
    title: "Add retry metrics dashboard",
    source: "user",
  },
  {
    id: "tsk_draft",
    session_id: "session-active",
    status: "ready",
    retry_count: 0,
    created_at: "2026-07-01T11:30:00Z",
    updated_at: "2026-07-01T11:30:00Z",
    title: "Add circuit breaker around the ingest retry loop",
    draft: true,
    source: "synthesizer",
    notes: [
      {
        id: 1,
        type: "risk",
        content: "Might mask a persistent upstream outage if the breaker never resets.",
        source: "synthesizer",
        status: "draft",
        timestamp: "2026-07-01T11:30:00Z",
      },
    ],
  },
  {
    id: "tsk_cycle_a",
    session_id: "session-active",
    status: "blocked",
    dependencies: ["tsk_cycle_b"],
    retry_count: 0,
    created_at: "2026-07-01T12:00:00Z",
    updated_at: "2026-07-01T12:00:00Z",
    title: "Cycle A",
    source: "user",
  },
  {
    id: "tsk_cycle_b",
    session_id: "session-active",
    status: "blocked",
    dependencies: ["tsk_cycle_a"],
    retry_count: 0,
    created_at: "2026-07-01T12:00:00Z",
    updated_at: "2026-07-01T12:00:00Z",
    title: "Cycle B",
    source: "user",
  },
];

export const fixtureFileChanges: FileChange[] = [
  {
    id: 1,
    session_id: "session-active",
    task_id: "tsk_ready",
    path: "services/data/internal/ingest.go",
    change: "modified",
    origin: "tool_use",
    attributed: true,
    actor: "autonomous-executor",
    timestamp: "2026-07-01T10:15:00Z",
  },
  {
    id: 2,
    session_id: "session-active",
    path: "services/data/internal/ingest_test.go",
    change: "added",
    origin: "tool_use",
    attributed: true,
    actor: "autonomous-executor",
    timestamp: "2026-07-01T10:16:00Z",
  },
];

/**
 * Dispatch-capable stream source for component tests (ui/src/tasks/HANDOFF.md
 * §3) — unlike the Conversation tab's streamSession (still a total no-op,
 * nothing consumes it yet), this task's acceptance criteria requires a fake
 * that can push a server-emitted envelope so a test can assert on the
 * resulting re-render.
 */
export class FakeSessionEventSource implements SessionEventSource {
  private listeners: ((ev: MessageEvent<string>) => void)[] = [];

  addEventListener(_type: "message", fn: (ev: MessageEvent<string>) => void): void {
    this.listeners.push(fn);
  }

  removeEventListener(_type: "message", fn: (ev: MessageEvent<string>) => void): void {
    this.listeners = this.listeners.filter((l) => l !== fn);
  }

  close(): void {}

  /** Test-only: simulate a server-pushed envelope, wire-shaped like a real SSE `data:` line. */
  dispatch(envelope: Partial<Envelope> & { type: string }): void {
    const ev = { data: JSON.stringify(envelope) } as MessageEvent<string>;
    this.listeners.forEach((fn) => fn(ev));
  }
}

/**
 * In-memory RouterClient for dev-mode demo mounting and component tests.
 * Every test outside client.test.ts should inject this rather than mocking
 * fetch — see ui/src/conversation/HANDOFF.md §3.
 */
export class FakeRouterClient implements RouterClient {
  private tasks: Map<string, Task>;
  private fileChanges: FileChange[];
  private nextTaskSeq = 1000;
  private nextNoteSeq = 1000;
  /** Test-only: set to return a specific instance from streamTaskEvents so a test can hold a reference to dispatch on. */
  taskEventSource: FakeSessionEventSource;

  constructor(
    private readonly sessions = fixtureSessions,
    private readonly transcripts = fixtureTranscripts,
    private readonly mentionables = fixtureMentionables,
    private readonly capabilities = fixtureCapabilities,
    tasks: Task[] = fixtureTasks,
    fileChanges: FileChange[] = fixtureFileChanges,
  ) {
    this.tasks = new Map(tasks.map((t) => [t.id, structuredClone(t)]));
    this.fileChanges = structuredClone(fileChanges);
    this.taskEventSource = new FakeSessionEventSource();
  }

  async getSession(id: string): Promise<Session> {
    const session = this.sessions[id];
    if (!session) throw new Error(`fixture session not found: ${id}`);
    return session;
  }

  async getTranscript(sessionId: string): Promise<TranscriptMessage[]> {
    return this.transcripts[sessionId] ?? [];
  }

  streamSession(): SessionEventSource {
    return new FakeSessionEventSource();
  }

  async sendMessage(): Promise<void> {}

  async interrupt(): Promise<void> {}

  async bounce(): Promise<void> {}

  async getMentionables(query: string): Promise<Mentionable[]> {
    const q = query.toLowerCase();
    if (!q) return this.mentionables;
    return this.mentionables.filter(
      (m) =>
        m.label.toLowerCase().includes(q) ||
        m.detail?.toLowerCase().includes(q),
    );
  }

  async getSkillCapabilities(): Promise<CapabilitySummary[]> {
    return this.capabilities;
  }

  async listTasks(sessionId: string, status?: TaskStatus): Promise<Task[]> {
    return [...this.tasks.values()].filter(
      (t) => t.session_id === sessionId && (!status || t.status === status),
    );
  }

  async getTask(id: string): Promise<Task> {
    const t = this.tasks.get(id);
    if (!t) throw new Error(`fixture task not found: ${id}`);
    return structuredClone(t);
  }

  async createTask(task: Partial<Task>): Promise<Task> {
    if (!task.title || !task.session_id) {
      throw new RouterValidationError(
        [
          !task.title && { field: "title", message: "required" },
          !task.session_id && { field: "session_id", message: "required" },
        ].filter((e): e is { field: string; message: string } => Boolean(e)),
      );
    }
    const now = new Date().toISOString();
    const created: Task = {
      status: "ready",
      retry_count: 0,
      source: "user",
      created_at: now,
      updated_at: now,
      ...task,
      id: `tsk_${this.nextTaskSeq++}`,
    } as Task;
    this.tasks.set(created.id, created);
    return structuredClone(created);
  }

  async updateTask(task: Task): Promise<Task> {
    if (!this.tasks.has(task.id)) throw new Error(`fixture task not found: ${task.id}`);
    const updated = { ...task, updated_at: new Date().toISOString() };
    this.tasks.set(task.id, updated);
    return structuredClone(updated);
  }

  async confirmTask(id: string): Promise<void> {
    const t = this.tasks.get(id);
    if (!t) throw new Error(`fixture task not found: ${id}`);
    t.draft = false;
  }

  async discardTask(id: string): Promise<void> {
    this.tasks.delete(id);
  }

  async importTasks(tasks: Partial<Task>[]): Promise<Task[]> {
    return Promise.all(tasks.map((t) => this.createTask(t)));
  }

  async taskCycles(sessionId: string): Promise<string[][]> {
    const bySession = [...this.tasks.values()].filter((t) => t.session_id === sessionId);
    const ids = new Set(bySession.map((t) => t.id));
    const cycles: string[][] = [];
    const visited = new Set<string>();
    for (const t of bySession) {
      if (visited.has(t.id)) continue;
      const path: string[] = [];
      let cur: Task | undefined = t;
      const onPath = new Map<string, number>();
      while (cur && !visited.has(cur.id)) {
        onPath.set(cur.id, path.length);
        path.push(cur.id);
        const depID: string | undefined = cur.dependencies?.find((d) => ids.has(d));
        const next: Task | undefined = depID ? this.tasks.get(depID) : undefined;
        cur = next;
        if (next && onPath.has(next.id)) {
          cycles.push(path.slice(onPath.get(next.id)));
          break;
        }
      }
      path.forEach((id) => visited.add(id));
    }
    return cycles;
  }

  async addNote(taskId: string, note: { type: NoteType; content: string }): Promise<Note> {
    const t = this.tasks.get(taskId);
    if (!t) throw new Error(`fixture task not found: ${taskId}`);
    const n: Note = {
      id: this.nextNoteSeq++,
      type: note.type,
      content: note.content,
      source: "user",
      status: "confirmed",
      timestamp: new Date().toISOString(),
    };
    t.notes = [...(t.notes ?? []), n];
    return n;
  }

  async confirmNote(taskId: string, noteId: number): Promise<void> {
    const t = this.tasks.get(taskId);
    const n = t?.notes?.find((n) => n.id === noteId);
    if (n) n.status = "confirmed";
  }

  async discardNote(taskId: string, noteId: number): Promise<void> {
    const t = this.tasks.get(taskId);
    if (t) t.notes = t.notes?.filter((n) => n.id !== noteId);
  }

  async getSessionChanges(sessionId: string): Promise<FileChange[]> {
    return this.fileChanges.filter((c) => c.session_id === sessionId);
  }

  streamTaskEvents(): SessionEventSource {
    return this.taskEventSource;
  }
}
