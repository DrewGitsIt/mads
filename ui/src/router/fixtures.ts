import type { RouterClient, SessionEventSource } from "./client";
import type {
  CapabilitySummary,
  Commit,
  ControlEvent,
  FileChange,
  FileDiff,
  Mentionable,
  MergeResult,
  Session,
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
  // Files tab demo sessions (W6.5) — see fixtureFileChanges/fixtureCommits
  // below for their change/commit data.
  "session-files-clean": {
    id: "session-files-clean",
    name: "Add retry backoff to the ingest worker",
    activity: "inactive",
    workspace: "mads",
    worktree_branch: "mads/tsk_retry-backoff",
    worktree_dir: "/home/drew/.mads/worktrees/ses_x/tsk_retry-backoff",
  },
  "session-files-observed": {
    id: "session-files-observed",
    name: "Investigate flaky metrics query test",
    activity: "inactive",
    workspace: "mads",
    worktree_branch: "mads/tsk_flaky-metrics",
    worktree_dir: "/home/drew/.mads/worktrees/ses_x/tsk_flaky-metrics",
  },
  "session-files-conflict": {
    id: "session-files-conflict",
    name: "Rename Session.Trust to Session.TrustLevel",
    activity: "inactive",
    workspace: "mads",
    worktree_branch: "mads/tsk_contract-rename",
    worktree_dir: "/home/drew/.mads/worktrees/ses_x/tsk_contract-rename",
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

/** No-op stream source — the fake has no real transport, only static fixtures. */
class FakeSessionEventSource implements SessionEventSource {
  addEventListener(): void {}
  removeEventListener(): void {}
  close(): void {}
}

// Files tab fixtures (W6.5) — three session ids a demo/test can pick between,
// per ui/src/files/HANDOFF.md §8: a clean-changes session, an
// unattributed-observation session, and a conflicted-merge session. Every
// AttributionKind (ui/src/files/attribution.ts) has at least one fixture row
// across the two sessions below, per HANDOFF §4.
export const fixtureFileChanges: Record<string, FileChange[]> = {
  "session-files-clean": [
    {
      id: 1,
      session_id: "session-files-clean",
      path: "services/data/internal/ingest.go",
      change: "modified",
      origin: "tool_use",
      attributed: true,
      actor: "autonomous-executor",
      timestamp: "2026-07-14T10:02:00Z",
    },
    {
      id: 2,
      session_id: "session-files-clean",
      path: "services/data/internal/ingest_test.go",
      change: "added",
      origin: "tool_use",
      attributed: true,
      actor: "autonomous-executor",
      timestamp: "2026-07-14T10:03:00Z",
    },
    {
      id: 3,
      session_id: "session-files-clean",
      path: "services/data/internal/legacy_retry.go",
      change: "deleted",
      origin: "commit",
      attributed: true,
      actor: "autonomous-executor",
      timestamp: "2026-07-14T10:05:00Z",
    },
  ],
  "session-files-observed": [
    {
      id: 4,
      session_id: "session-files-observed",
      path: "services/router/internal/api/events.go",
      change: "modified",
      origin: "tool_use",
      attributed: true,
      actor: "investigator",
      timestamp: "2026-07-14T11:00:00Z",
    },
    {
      id: 5,
      session_id: "session-files-observed",
      path: "ui/src/App.module.css",
      change: "modified",
      origin: "observed",
      attributed: false,
      timestamp: "2026-07-14T11:04:00Z",
    },
  ],
  "session-files-conflict": [
    {
      id: 6,
      session_id: "session-files-conflict",
      path: "pkg/contracts/session.go",
      change: "modified",
      origin: "tool_use",
      attributed: true,
      actor: "autonomous-executor",
      timestamp: "2026-07-14T12:00:00Z",
    },
  ],
};

export const fixtureCommits: Record<string, Commit[]> = {
  "session-files-clean": [
    {
      hash: "a1b2c3d",
      session_id: "session-files-clean",
      task_id: "tsk_retry-backoff",
      message: "ingest: add retry backoff with jitter\n\nTask: tsk_retry-backoff",
      author: "autonomous-executor",
      linked_via: "trailer",
      timestamp: "2026-07-14T10:06:00Z",
    },
  ],
  "session-files-observed": [
    {
      hash: "e4f5a6b",
      session_id: "session-files-observed",
      task_id: "tsk_flaky-metrics",
      message: "router: filter control events by session_id",
      author: "investigator",
      linked_via: "session_context",
      timestamp: "2026-07-14T11:05:00Z",
    },
    {
      hash: "c7d8e9f",
      session_id: "session-files-observed",
      task_id: "tsk_flaky-metrics",
      message: "router: candidate fix for overlapping file touches",
      author: "investigator",
      linked_via: "heuristic",
      timestamp: "2026-07-14T11:08:00Z",
    },
    {
      hash: "b0c1d2e",
      session_id: "session-files-observed",
      message: "chore: bump go.sum",
      author: "drew",
      orphan: true,
      timestamp: "2026-07-14T11:10:00Z",
    },
  ],
  "session-files-conflict": [
    {
      hash: "f1a2b3c",
      session_id: "session-files-conflict",
      task_id: "tsk_contract-rename",
      message: "contracts: rename Session.Trust to Session.TrustLevel\n\nTask: tsk_contract-rename",
      author: "autonomous-executor",
      linked_via: "trailer",
      timestamp: "2026-07-14T12:01:00Z",
    },
  ],
};

export const fixtureFileDiffs: Record<string, FileDiff> = {
  "services/data/internal/ingest.go": {
    path: "services/data/internal/ingest.go",
    language: "go",
    original: "func ingest() {\n\ttime.Sleep(retryDelay)\n}\n",
    modified: "func ingest() {\n\ttime.Sleep(backoff.Next())\n}\n",
  },
};

export const fixtureCleanMerge: MergeResult = {
  conflicted: false,
  branch: "mads/tsk_retry-backoff",
};

export const fixtureConflictedMerge: MergeResult = {
  conflicted: true,
  branch: "mads/tsk_contract-rename",
  files: ["pkg/contracts/session.go", "services/data/internal/store/sessions.go"],
};

// A conflict-marker sample for the seed-only MergeEditor stub (HANDOFF §5) —
// no real conflicted worktree content exists to point one at, so this is the
// only content the stub is ever tested against.
export const fixtureConflictMarkers = `<<<<<<< HEAD
	TrustLevel string \`json:"trust_level"\`
=======
	Trust string \`json:"trust"\`
>>>>>>> mads/tsk_contract-rename
`;

/**
 * Driveable fake for the Router's control-plane SSE stream
 * (GET /events?session_id=&types=). Unlike FakeSessionEventSource this one
 * has a real (in-memory) transport: `emit` dispatches a named-event
 * MessageEvent to every listener registered for that type, with `ev.data`
 * JSON-holding a full envelope (mirroring the real `id:/event:/data:` wire
 * format, where `data` is the whole `contracts.Envelope`, not just its
 * payload — see services/router/internal/api/events.go), so
 * MergeBackFlow.test.tsx can simulate merge.attempted/conflicted/completed
 * arriving over the wire (HANDOFF §7).
 */
export class FakeControlEventSource implements SessionEventSource {
  private listeners = new Map<string, Set<(ev: MessageEvent<string>) => void>>();

  addEventListener(type: string, listener: (ev: MessageEvent<string>) => void): void {
    const set = this.listeners.get(type) ?? new Set();
    set.add(listener);
    this.listeners.set(type, set);
  }

  removeEventListener(type: string, listener: (ev: MessageEvent<string>) => void): void {
    this.listeners.get(type)?.delete(listener);
  }

  close(): void {
    this.listeners.clear();
  }

  emit(type: string, envelope: Partial<ControlEvent> = {}): void {
    const full: ControlEvent = { seq: 0, timestamp: new Date().toISOString(), ...envelope, type };
    const data = JSON.stringify(full);
    const event = { data, type } as MessageEvent<string>;
    this.listeners.get(type)?.forEach((listener) => listener(event));
  }
}

export function createFakeControlEventSource(): {
  source: FakeControlEventSource;
  emit: (type: string, envelope?: Partial<ControlEvent>) => void;
} {
  const source = new FakeControlEventSource();
  return { source, emit: (type, envelope) => source.emit(type, envelope) };
}

/**
 * In-memory RouterClient for dev-mode demo mounting and component tests.
 * Every test outside client.test.ts should inject this rather than mocking
 * fetch — see ui/src/conversation/HANDOFF.md §3.
 */
export class FakeRouterClient implements RouterClient {
  /** Every control-event source this client has handed out, in creation order — tests grab the latest and call `.emit(...)` on it (HANDOFF §7). */
  readonly controlEventSources: FakeControlEventSource[] = [];
  /** Paths passed to openInEditor, in call order — lets tests assert the button fired without a real editor to open. */
  readonly openedPaths: string[] = [];

  constructor(
    private readonly sessions = fixtureSessions,
    private readonly transcripts = fixtureTranscripts,
    private readonly mentionables = fixtureMentionables,
    private readonly capabilities = fixtureCapabilities,
    private readonly fileChanges = fixtureFileChanges,
    private readonly commits = fixtureCommits,
    private readonly fileDiffs = fixtureFileDiffs,
    private readonly mergeResults: Record<string, MergeResult> = {
      "session-files-conflict": fixtureConflictedMerge,
    },
  ) {}

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

  async getFileChanges(sessionId: string, taskId?: string): Promise<FileChange[]> {
    const changes = this.fileChanges[sessionId] ?? [];
    return taskId ? changes.filter((c) => c.task_id === taskId) : changes;
  }

  async getCommits(sessionId: string): Promise<Commit[]> {
    return this.commits[sessionId] ?? [];
  }

  async getFileDiff(_sessionId: string, path: string): Promise<FileDiff> {
    const diff = this.fileDiffs[path];
    if (!diff) throw new Error(`fixture diff not found: ${path}`);
    return diff;
  }

  async openInEditor(path: string): Promise<void> {
    this.openedPaths.push(path);
  }

  async mergeSession(sessionId: string): Promise<MergeResult> {
    return this.mergeResults[sessionId] ?? fixtureCleanMerge;
  }

  streamControlEvents(): SessionEventSource {
    const source = new FakeControlEventSource();
    this.controlEventSources.push(source);
    return source;
  }
}
