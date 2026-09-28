import type { RouterClient, SessionEventSource } from "./client";
import { RouterValidationError } from "./client";
import type {
  Agent,
  CapabilitySuggestion,
  AgentLeaderboardEntry,
  BehaviorInfo,
  Capability,
  CapabilitySummary,
  CapabilityUsage,
  Commit,
  Config,
  CostMetrics,
  DiagnoseResult,
  Envelope,
  FastModelStatus,
  FileChange,
  FileDiff,
  GenerateAgentBody,
  Link,
  LinkRequest,
  Mentionable,
  MergeResult,
  Model,
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
  Retro,
  Session,
  SessionDefaults,
  SessionTrust,
  SetSessionModelResponse,
  StreamEvent,
  Task,
  TaskOutcomes,
  TaskStatus,
  Template,
  TemplateValidationResult,
  ToolDiff,
  TranscriptMessage,
  UpdateCheckResult,
  VersionInfo,
  Workspace,
} from "./types";
import { PRESET_GRANTS } from "./types";

export const fixtureLinks: Link[] = [];

export const fixtureSessions: Record<string, Session> = {
  "session-active": {
    id: "session-active",
    created_at: "2026-07-14T00:00:00Z",
    updated_at: "2026-07-14T00:00:00Z",
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
    created_at: "2026-07-14T00:00:00Z",
    updated_at: "2026-07-14T00:00:00Z",
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
    created_at: "2026-07-14T00:00:00Z",
    updated_at: "2026-07-14T00:00:00Z",
    name: "Add retry backoff to the ingest worker",
    activity: "inactive",
    workspace: "mads",
    worktree_branch: "mads/tsk_retry-backoff",
    worktree_dir: "/home/drew/.mads/worktrees/ses_x/tsk_retry-backoff",
  },
  "session-files-observed": {
    id: "session-files-observed",
    created_at: "2026-07-14T00:00:00Z",
    updated_at: "2026-07-14T00:00:00Z",
    name: "Investigate flaky metrics query test",
    activity: "inactive",
    workspace: "mads",
    worktree_branch: "mads/tsk_flaky-metrics",
    worktree_dir: "/home/drew/.mads/worktrees/ses_x/tsk_flaky-metrics",
  },
  "session-files-conflict": {
    id: "session-files-conflict",
    created_at: "2026-07-14T00:00:00Z",
    updated_at: "2026-07-14T00:00:00Z",
    name: "Rename Session.Trust to Session.TrustLevel",
    activity: "inactive",
    workspace: "mads",
    worktree_branch: "mads/tsk_contract-rename",
    worktree_dir: "/home/drew/.mads/worktrees/ses_x/tsk_contract-rename",
  },
  // Double-empty-state fixture: no file changes, no commits (ui/src/files/
  // HANDOFF.md §7).
  "session-files-empty": {
    id: "session-files-empty",
    created_at: "2026-07-14T00:00:00Z",
    updated_at: "2026-07-14T00:00:00Z",
    name: "Nothing recorded yet",
    activity: "inactive",
    workspace: "mads",
  },
  // Pipeline-only fixture: commits recorded, no observed working changes —
  // the case this task's root cause was about (ui/src/files/HANDOFF.md §7/§10).
  "session-files-commits-only": {
    id: "session-files-commits-only",
    created_at: "2026-07-14T00:00:00Z",
    updated_at: "2026-07-14T00:00:00Z",
    name: "Pipeline-only maintenance run",
    activity: "inactive",
    workspace: "mads",
  },
  // Retro surface (W6.7) — a retro-generated follow-up session, so the
  // picker's "Retro-generated" kind filter has a fixture to match on.
  "session-retro-generated": {
    id: "session-retro-generated",
    created_at: "2026-07-14T00:00:00Z",
    updated_at: "2026-07-14T00:00:00Z",
    name: "Follow-up: retry backoff DPAs",
    activity: "inactive",
    workspace: "mads",
    tags: ["retro"],
    retro_id: "retro-1",
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

// Full Capability shape (Library editor / CapabilityPickerField registry) —
// same names as fixtureCapabilities above, which is the CapabilitySummary
// subset used by the / command palette.
export const fixtureCapabilityRegistry: Capability[] = fixtureCapabilities.map((c) => ({
  id: c.id,
  name: c.name,
  description: c.description,
  type: c.type,
  config: c.type === "mcp" ? { command: c.name } : { path: `skills/${c.name}` },
  health: { healthy: c.healthy },
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
}));

export const fixtureWorkspaces: Workspace[] = [
  {
    id: "ws-mads",
    name: "mads",
    description: "The mads monorepo",
    defaults: {},
    capabilities: ["code-review"],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
];

// Retro surface (W6.7) fixture — three DPAs deliberately span 3 different
// remediation_layer values and both accepted/rejected statuses, so a single
// fixture exercises DPACard's kind-agnostic rendering and gives the
// chip-toggle test a pre-rejected DPA to flip back to accepted, not just an
// accepted-to-rejected path.
export const fixtureRetro: Retro = {
  id: "retro-1",
  session_ids: ["session-active", "session-inactive"],
  user_observations:
    "Retries kept looping without backoff; flaky test masked a real race.",
  dpas: [
    {
      id: "dpa_1",
      problem: "Ingest worker retried without backoff, hammering the queue.",
      evidence: "session-active transcript, tool_use Edit at ingest.go:42",
      root_cause: "No backoff policy configured on the retry loop.",
      remediation_layer: "tool_config",
      suggested_action: "Default all retry loops to exponential backoff with jitter.",
      status: "accepted",
    },
    {
      id: "dpa_2",
      problem: "Flaky test masked a real refresh/writer race.",
      evidence: "session-inactive transcript, pinned-clock repro",
      root_cause: "No FOR UPDATE fence around the metrics view refresh.",
      remediation_layer: "prompt_engineering",
      suggested_action:
        "Steer investigator agents to add a regression test under -race before closing.",
      status: "rejected",
    },
    {
      id: "dpa_3",
      problem: "No workspace convention for retry policies.",
      evidence: "Both DPAs above stem from ad hoc retry code.",
      root_cause: "Missing workspace-level steering file for retry/backoff conventions.",
      remediation_layer: "human_process",
      suggested_action: "Add a retry-policy steering file and reference it from agent prompts.",
      status: "accepted",
    },
  ],
  created_at: "2026-07-14T12:00:00Z",
  updated_at: "2026-07-14T12:05:00Z",
};

export const fixtureRetroList: Retro[] = [fixtureRetro];

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
  // Files tab commit-chip fixtures (ui/src/files/HANDOFF.md §6) — matches
  // fixtureCommits' task_ids below, so the Files tab's task-title lookup
  // has something real to resolve.
  {
    id: "tsk_retry-backoff",
    session_id: "session-files-clean",
    status: "done",
    retry_count: 0,
    created_at: "2026-07-14T09:00:00Z",
    updated_at: "2026-07-14T10:06:00Z",
    title: "Ingest worker resilience",
    source: "user",
  },
  {
    id: "tsk_flaky-metrics",
    session_id: "session-files-observed",
    status: "in_progress",
    retry_count: 0,
    created_at: "2026-07-14T10:30:00Z",
    updated_at: "2026-07-14T11:08:00Z",
    title: "Investigate flaky metrics filtering",
    source: "user",
  },
  {
    id: "tsk_contract-rename",
    session_id: "session-files-conflict",
    status: "needs_review",
    retry_count: 0,
    created_at: "2026-07-14T11:45:00Z",
    updated_at: "2026-07-14T12:01:00Z",
    title: "Rename Session.Trust contract field",
    source: "user",
  },
];

// Synthesis proposal cards (Context Rail) — one of each ProposalType, all
// "proposed" (spec §4's default pending-review state).
export const fixtureProposals: Proposal[] = [
  {
    id: "prop_new_task",
    session_id: "session-active",
    type: "create_task",
    status: "proposed",
    task: {
      title: "Add a metrics counter for retry exhaustion",
      description: "The ingest worker gives up silently after max retries — nothing pages on it.",
    },
    source: "synthesizer",
    created_at: "2026-07-14T12:00:00Z",
    updated_at: "2026-07-14T12:00:00Z",
  },
  {
    id: "prop_note",
    session_id: "session-active",
    type: "add_note",
    status: "proposed",
    task_id: "tsk_in_progress",
    note: { type: "risk", content: "The retry race repro relies on a pinned test clock — flaky under -race." },
    source: "synthesizer",
    created_at: "2026-07-14T12:01:00Z",
    updated_at: "2026-07-14T12:01:00Z",
  },
  {
    id: "prop_update",
    session_id: "session-active",
    type: "update_task",
    status: "proposed",
    task_id: "tsk_ready",
    update: { acceptance_criteria: "Backoff caps at 30s; jitter is +/-20%; covered by a race-detector test." },
    source: "synthesizer",
    created_at: "2026-07-14T12:02:00Z",
    updated_at: "2026-07-14T12:02:00Z",
  },
];

// Tasks tab / Context Rail change rows (W6.4) — folded into the record below.
const taskTabFileChanges: FileChange[] = [
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
 * §3) — also backs the Conversation tab's streamSession.
 */
export class FakeSessionEventSource implements SessionEventSource {
  private listeners = new Map<string, Set<(ev: MessageEvent<string>) => void>>();

  addEventListener(type: string, listener: (ev: MessageEvent<string>) => void): void {
    const set = this.listeners.get(type) ?? new Set();
    set.add(listener);
    this.listeners.set(type, set);
  }

  removeEventListener(type: string, listener: (ev: MessageEvent<string>) => void): void {
    this.listeners.get(type)?.delete(listener);
  }

  close(): void {}

  /** Test-only: simulate one normalized StreamEvent (providers/provider/event.go), SSE-encoded with event: <kind>. */
  emit(event: StreamEvent): void {
    const data = { data: JSON.stringify(event) } as MessageEvent<string>;
    this.listeners.get(event.kind)?.forEach((listener) => listener(data));
  }

  /** Test-only: simulate the browser's native EventSource reconnecting after
   * a mid-stream drop — a plain (non-StreamEvent) "open" DOM event, distinct
   * from `emit`'s provider-frame shape above. */
  emitOpen(): void {
    const ev = {} as MessageEvent<string>;
    this.listeners.get("open")?.forEach((listener) => listener(ev));
  }
}

// Files tab fixtures (W6.5) — three session ids a demo/test can pick between,
// per ui/src/files/HANDOFF.md §8: a clean-changes session, an
// unattributed-observation session, and a conflicted-merge session. Every
// AttributionKind (ui/src/files/attribution.ts) has at least one fixture row
// across the two sessions below, per HANDOFF §4.
export const fixtureFileChanges: Record<string, FileChange[]> = {
  "session-active": taskTabFileChanges,
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
      files: [
        { path: "services/data/internal/backoff.go", change: "added" },
        { path: "services/data/internal/backoff_test.go", change: "added" },
        { path: "docs/RETRY-BACKOFF.md", change: "modified" },
      ],
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
  "session-files-commits-only": [
    {
      hash: "d1e2f3a",
      session_id: "session-files-commits-only",
      task_id: "tsk_contract-rename",
      message: "chore: pipeline-only maintenance commit",
      author: "autonomous-executor",
      linked_via: "session_context",
      timestamp: "2026-07-14T13:00:00Z",
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

// Keyed by `${hash}:${path}` — a commit's file diffs, distinct from
// fixtureFileDiffs (working-change diffs, keyed by path alone).
export const fixtureCommitFileDiffs: Record<string, FileDiff> = {
  "a1b2c3d:services/data/internal/backoff.go": {
    path: "services/data/internal/backoff.go",
    language: "go",
    original: "",
    modified: "func backoff() time.Duration {\n\treturn baseDelay\n}\n",
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
 * In-memory RouterClient for dev-mode demo mounting and component tests.
 * Every test outside client.test.ts should inject this rather than mocking
 * fetch — see ui/src/conversation/HANDOFF.md §3.
 */
export class FakeRouterClient implements RouterClient {
  private sessions: Map<string, Session>;
  private tasks: Map<string, Task>;
  private links: Map<string, Link>;
  private proposals: Map<string, Proposal>;
  private nextTaskSeq = 1000;
  private nextNoteSeq = 1000;
  private nextLinkSeq = 1000;
  /** Every data-plane stream source this client has handed out via streamSession, in creation order — tests grab the latest and call `.emit(...)` on it. */
  readonly sessionEventSources: FakeSessionEventSource[] = [];
  /** Paths passed to openInEditor, in call order — lets tests assert the button fired without a real editor to open. */
  readonly openedPaths: string[] = [];
  /** When true, setSessionModel answers queued: true — the "a turn was in flight, the switch lands at turn end" branch. */
  queueModelSwitches = false;
  /** Per-session live transport mode, mutated by switchTransport and read back by getTransportMode — mirrors the real child's runner-held mode being the single source of truth (no live child = no entry = defaults "chat"). */
  private readonly transportModes = new Map<string, "chat" | "terminal">();

  constructor(
    sessions: Record<string, Session> = fixtureSessions,
    private readonly transcripts = fixtureTranscripts,
    private readonly mentionables = fixtureMentionables,
    private readonly capabilities = fixtureCapabilities,
    tasks: Task[] = fixtureTasks,
    private readonly fileChanges = fixtureFileChanges,
    private readonly commits = fixtureCommits,
    private readonly fileDiffs = fixtureFileDiffs,
    private readonly commitFileDiffs = fixtureCommitFileDiffs,
    private readonly mergeResults: Record<string, MergeResult> = {
      "session-files-conflict": fixtureConflictedMerge,
    },
    private readonly sessionDefaults: SessionDefaults = fixtureSessionDefaults,
    links: Link[] = fixtureLinks,
    proposals: Proposal[] = fixtureProposals,
  ) {
    this.sessions = new Map(
      Object.entries(sessions).map(([id, s]) => [id, structuredClone(s)]),
    );
    this.tasks = new Map(tasks.map((t) => [t.id, structuredClone(t)]));
    this.links = new Map(links.map((l) => [l.id, structuredClone(l)]));
    this.proposals = new Map(proposals.map((p) => [p.id, structuredClone(p)]));
  }

  async getSession(id: string): Promise<Session> {
    const session = this.sessions.get(id);
    if (!session) throw new Error(`fixture session not found: ${id}`);
    return session;
  }

  async getTranscript(sessionId: string): Promise<TranscriptMessage[]> {
    return this.transcripts[sessionId] ?? [];
  }

  streamSession(): SessionEventSource {
    const source = new FakeSessionEventSource();
    this.sessionEventSources.push(source);
    return source;
  }

  async sendMessage(): Promise<void> {}

  async interrupt(): Promise<void> {}

  async compactSession(): Promise<void> {}

  async bounce(): Promise<void> {}

  // Fixture-backed no-ops, same convention as sendMessage/interrupt/bounce
  // above — tests override via vi.spyOn when they need to exercise the
  // switching flow (ui/src/conversation/HANDOFF.md). Does record the mode
  // into transportModes though, so a plain (non-spied) switch followed by
  // a remount/getTransportMode read sees it stuck, the same as the real
  // Router/session-service round trip.
  async switchTransport(
    sessionId: string,
    mode: "chat" | "terminal",
  ): Promise<{ mode: string; ref: string }> {
    this.transportModes.set(sessionId, mode);
    return { mode, ref: "fake-ref" };
  }

  async getTransportMode(sessionId: string): Promise<{ mode: "chat" | "terminal" }> {
    return { mode: this.transportModes.get(sessionId) ?? "chat" };
  }

  /** Per-session pending permission requests — mutated by resolvePermissionRequest and seedPermissionRequest below, mirroring the real Runner's in-memory registry (services/session/internal/runner/permissions.go). */
  private readonly permissionRequests = new Map<string, PermissionRequest[]>();

  async getPermissionRequests(sessionId: string): Promise<PermissionRequest[]> {
    return this.permissionRequests.get(sessionId) ?? [];
  }

  async resolvePermissionRequest(sessionId: string, requestId: string): Promise<void> {
    const list = this.permissionRequests.get(sessionId);
    if (!list) return;
    this.permissionRequests.set(
      sessionId,
      list.filter((p) => p.id !== requestId),
    );
  }

  /** Test hook: seeds a pending request as if the hub had broadcast one, so a component test can assert the card renders without a real hook round trip. */
  seedPermissionRequest(sessionId: string, req: PermissionRequest): void {
    const list = this.permissionRequests.get(sessionId) ?? [];
    list.push(req);
    this.permissionRequests.set(sessionId, list);
  }

  async terminalWrite(): Promise<void> {}

  async terminalResize(): Promise<void> {}

  async closeSession(sessionId: string): Promise<void> {
    const session = this.sessions.get(sessionId);
    if (!session) throw new Error(`fixture session not found: ${sessionId}`);
    session.activity = "inactive";
  }

  async resumeSession(sessionId: string): Promise<{ addr: string }> {
    const session = this.sessions.get(sessionId);
    if (!session) throw new Error(`fixture session not found: ${sessionId}`);
    session.activity = "active";
    return { addr: "localhost:0" };
  }

  async renameSession(id: string, name: string): Promise<Session> {
    const session = this.sessions.get(id);
    if (!session) throw new Error(`fixture session not found: ${id}`);
    session.name = name;
    session.name_source = "user";
    return session;
  }

  async setSessionTrust(id: string, trust: SessionTrust): Promise<Session> {
    const session = this.sessions.get(id);
    if (!session) throw new Error(`fixture session not found: ${id}`);
    session.trust = trust;
    return session;
  }

  /**
   * Mirrors the real endpoint: the record always takes the new model, and
   * `queued` reflects whether a turn was in flight. Tests drive that with
   * `queueModelSwitches` rather than a live turn, since the fake has no
   * provider process to be busy.
   */
  async setSessionModel(id: string, model: string): Promise<SetSessionModelResponse> {
    const session = this.sessions.get(id);
    if (!session) throw new Error(`fixture session not found: ${id}`);
    session.model = model;
    return { session, queued: this.queueModelSwitches };
  }

  async deleteSession(id: string): Promise<void> {
    if (!this.sessions.has(id)) throw new Error(`fixture session not found: ${id}`);
    this.sessions.delete(id);
  }

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

  /** Tests set this to control what the next suggestTaskCapabilities call returns; defaults to none suggested. */
  suggestedCapabilities: string[] = [];

  async suggestTaskCapabilities(): Promise<string[]> {
    return this.suggestedCapabilities;
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
    return (this.fileChanges[sessionId] ?? []).filter(
      (c) => c.session_id === sessionId,
    );
  }

  // --- Synthesis proposals (spec §4) ---
  async listProposals(sessionId: string): Promise<Proposal[]> {
    return [...this.proposals.values()]
      .filter((p) => p.session_id === sessionId)
      .map((p) => structuredClone(p));
  }

  async approveProposal(id: string): Promise<Proposal> {
    const p = this.proposals.get(id);
    if (!p) throw new Error(`fixture proposal not found: ${id}`);
    const now = new Date().toISOString();
    if (p.type === "create_task" && p.task) {
      const created = await this.createTask({
        session_id: p.session_id,
        title: p.task.title,
        description: p.task.description,
        acceptance_criteria: p.task.acceptance_criteria,
        scope: p.task.scope,
        source: "synthesizer",
      });
      p.applied_task_id = created.id;
    } else if (p.type === "add_note" && p.note && p.task_id) {
      await this.addNote(p.task_id, p.note);
    } else if (p.type === "update_task" && p.update && p.task_id) {
      const t = this.tasks.get(p.task_id);
      if (t) {
        Object.assign(t, {
          ...(p.update.title && { title: p.update.title }),
          ...(p.update.description && { description: p.update.description }),
          ...(p.update.acceptance_criteria && {
            acceptance_criteria: p.update.acceptance_criteria,
          }),
          ...(p.update.scope && { scope: p.update.scope }),
          updated_at: now,
        });
      }
    }
    p.status = "approved";
    p.updated_at = now;
    return structuredClone(p);
  }

  async dismissProposal(id: string): Promise<Proposal> {
    const p = this.proposals.get(id);
    if (!p) throw new Error(`fixture proposal not found: ${id}`);
    p.status = "dismissed";
    p.updated_at = new Date().toISOString();
    return structuredClone(p);
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

  async getCommitFileDiff(_sessionId: string, hash: string, path: string): Promise<FileDiff> {
    const diff = this.commitFileDiffs[`${hash}:${path}`];
    if (!diff) throw new Error(`no fixture diff for commit ${hash} path ${path}`);
    return diff;
  }

  async openInEditor(path: string): Promise<void> {
    this.openedPaths.push(path);
  }

  async mergeSession(sessionId: string): Promise<MergeResult> {
    return this.mergeResults[sessionId] ?? fixtureCleanMerge;
  }

  /** Seeded control-plane event log — empty by default so the Conversation tab's history-merge fetch (T17) is a no-op unless a test seeds envelopes via seedEvent. */
  private readonly eventLog: Envelope[] = [];

  /** Test hook: seeds one persisted envelope for getEvents to replay. */
  seedEvent(envelope: Envelope): void {
    this.eventLog.push(envelope);
  }

  async getEvents(filter: {
    sessionId: string;
    since?: number;
    types?: string[];
  }): Promise<Envelope[]> {
    return this.eventLog.filter(
      (e) =>
        (!filter.sessionId || e.session_id === filter.sessionId) &&
        (filter.since === undefined || e.seq > filter.since) &&
        (!filter.types?.length || filter.types.includes(e.type)),
    );
  }

  /** Seeded tool-diff captures by tool_use_id — absent means the real route's 404 ("keep the input-derived diff"). */
  private readonly toolDiffs = new Map<string, ToolDiff>();

  /** Test hook: seeds a shadow-store capture for getToolDiff. */
  seedToolDiff(toolUseId: string, diff: ToolDiff): void {
    this.toolDiffs.set(toolUseId, diff);
  }

  async getToolDiff(_sessionId: string, toolUseId: string): Promise<ToolDiff | null> {
    return this.toolDiffs.get(toolUseId) ?? null;
  }

  // --- Sessions (session-agnostic) ---
  async listSessions(): Promise<Session[]> {
    return Array.from(this.sessions.values());
  }

  async createSession(): Promise<{ session: Session; addr: string }> {
    throw new Error("not fixtured: createSession");
  }

  // --- Config ---
  async getConfig(): Promise<Config> {
    return fixtureConfig;
  }

  async updateConfig(): Promise<Config> {
    throw new Error("not fixtured: updateConfig");
  }

  async getFastModelStatus(): Promise<FastModelStatus> {
    return fixtureFastModelStatus;
  }

  // --- Profiles ---
  async listProfiles(): Promise<Profile[]> {
    return fixtureProfiles;
  }

  async createProfile(): Promise<Profile> {
    throw new Error("not fixtured: createProfile");
  }

  async updateProfile(): Promise<Profile> {
    throw new Error("not fixtured: updateProfile");
  }

  async deleteProfile(): Promise<void> {
    throw new Error("not fixtured: deleteProfile");
  }

  async activateProfile(): Promise<Config> {
    throw new Error("not fixtured: activateProfile");
  }

  // --- Credentials ---
  async listCredentials(): Promise<string[]> {
    throw new Error("not fixtured: listCredentials");
  }

  async createCredential(): Promise<{ name: string }> {
    throw new Error("not fixtured: createCredential");
  }

  async deleteCredential(): Promise<void> {
    throw new Error("not fixtured: deleteCredential");
  }

  async listCredentialEnvCandidates(): Promise<string[]> {
    throw new Error("not fixtured: listCredentialEnvCandidates");
  }

  async importCredentials(): Promise<{ imported: string[] }> {
    throw new Error("not fixtured: importCredentials");
  }

  // --- Preflight ---
  async getPreflightChecks(): Promise<PreflightCheck[]> {
    return fixturePreflightChecks;
  }

  // --- Update (W7.5 §14.3) ---
  async getVersion(): Promise<VersionInfo> {
    return fixtureVersionInfo;
  }

  async checkForUpdate(): Promise<UpdateCheckResult> {
    return fixtureUpdateCheckResult;
  }

  // --- Providers / behaviors / models ---
  async listProviders(): Promise<ProviderInfo[]> {
    return fixtureProviders;
  }

  async listProviderNames(): Promise<Provider[]> {
    throw new Error("not fixtured: listProviderNames");
  }

  async listProviderBehaviors(): Promise<BehaviorInfo[]> {
    return fixtureProviderBehaviors;
  }

  async listModels(): Promise<Model[]> {
    throw new Error("not fixtured: listModels");
  }

  // --- Metrics ---
  async metricsCost(): Promise<CostMetrics> {
    throw new Error("not fixtured: metricsCost");
  }

  async metricsAgentLeaderboard(): Promise<AgentLeaderboardEntry[]> {
    throw new Error("not fixtured: metricsAgentLeaderboard");
  }

  async metricsTaskOutcomes(): Promise<TaskOutcomes> {
    throw new Error("not fixtured: metricsTaskOutcomes");
  }

  async metricsCapabilityUsage(): Promise<CapabilityUsage[]> {
    throw new Error("not fixtured: metricsCapabilityUsage");
  }

  async metricsPipelineRuns(_sessionId?: string): Promise<PipelineRun[]> {
    throw new Error("not fixtured: metricsPipelineRuns");
  }

  // --- Templates ---
  async listTemplates(): Promise<Template[]> {
    throw new Error("not fixtured: listTemplates");
  }

  async createTemplate(): Promise<Template> {
    throw new Error("not fixtured: createTemplate");
  }

  async updateTemplate(): Promise<Template> {
    throw new Error("not fixtured: updateTemplate");
  }

  async deleteTemplate(): Promise<void> {
    throw new Error("not fixtured: deleteTemplate");
  }

  async validateTemplate(): Promise<{ valid: boolean }> {
    throw new Error("not fixtured: validateTemplate");
  }

  async validateAllTemplates(): Promise<TemplateValidationResult[]> {
    throw new Error("not fixtured: validateAllTemplates");
  }

  // --- Workspaces ---
  async listWorkspaces(): Promise<Workspace[]> {
    return fixtureWorkspaces;
  }

  // --- Session defaults (Launchpad preview) ---
  async getSessionDefaults(): Promise<SessionDefaults> {
    return this.sessionDefaults;
  }

  async createWorkspace(): Promise<Workspace> {
    throw new Error("not fixtured: createWorkspace");
  }

  async updateWorkspace(): Promise<Workspace> {
    throw new Error("not fixtured: updateWorkspace");
  }

  async deleteWorkspace(): Promise<void> {
    throw new Error("not fixtured: deleteWorkspace");
  }

  // --- Agents (Library) ---
  async listAgents(): Promise<Agent[]> {
    throw new Error("not fixtured: listAgents");
  }

  async createAgent(): Promise<Agent> {
    throw new Error("not fixtured: createAgent");
  }

  async updateAgent(): Promise<Agent> {
    throw new Error("not fixtured: updateAgent");
  }

  async deleteAgent(): Promise<void> {
    throw new Error("not fixtured: deleteAgent");
  }

  async generateAgent(_body: GenerateAgentBody): Promise<{ job_id: string }> {
    throw new Error("not fixtured: generateAgent");
  }

  async describeAgent(): Promise<{ result: string }> {
    throw new Error("not fixtured: describeAgent");
  }

  async suggestAgentCapabilities(): Promise<CapabilitySuggestion> {
    throw new Error("not fixtured: suggestAgentCapabilities");
  }

  async refineAgent(): Promise<{ result: string }> {
    throw new Error("not fixtured: refineAgent");
  }

  // --- Capabilities (Library) ---
  async listCapabilities(): Promise<Capability[]> {
    return fixtureCapabilityRegistry;
  }

  async getCapability(): Promise<Capability> {
    throw new Error("not fixtured: getCapability");
  }

  async createCapability(): Promise<Capability> {
    throw new Error("not fixtured: createCapability");
  }

  async updateCapability(): Promise<Capability> {
    throw new Error("not fixtured: updateCapability");
  }

  async deleteCapability(): Promise<void> {
    throw new Error("not fixtured: deleteCapability");
  }

  async diagnoseCapability(): Promise<DiagnoseResult> {
    throw new Error("not fixtured: diagnoseCapability");
  }

  // --- Tasks (session-agnostic) ---
  async listAllTasks(status?: TaskStatus): Promise<Task[]> {
    return [...this.tasks.values()].filter((t) => !status || t.status === status);
  }

  // --- Retro ---
  async listRetros(): Promise<Retro[]> {
    return fixtureRetroList;
  }

  async getRetro(id: string): Promise<Retro> {
    if (id !== fixtureRetro.id) throw new Error(`fixture retro not found: ${id}`);
    return fixtureRetro;
  }

  async startRetro(): Promise<{ job_id: string; retro_id: string }> {
    throw new Error("not fixtured: startRetro");
  }

  async acceptDpa(): Promise<void> {
    throw new Error("not fixtured: acceptDpa");
  }

  async rejectDpa(): Promise<void> {
    throw new Error("not fixtured: rejectDpa");
  }

  async createRetroFollowUpSession(): Promise<{ session: Session; addr: string; tasks: Task[] }> {
    throw new Error("not fixtured: createRetroFollowUpSession");
  }

  // --- Jobs ---
  async cancelJob(): Promise<void> {
    throw new Error("not fixtured: cancelJob");
  }

  // --- Pipeline ---
  async createPipelineRun(): Promise<{ run_id: string }> {
    throw new Error("not fixtured: createPipelineRun");
  }

  async cancelPipelineRun(): Promise<void> {
    throw new Error("not fixtured: cancelPipelineRun");
  }

  async retryTask(): Promise<Task> {
    throw new Error("not fixtured: retryTask");
  }

  // --- Registry (Library, flag-gated — W7.6) ---
  async registryIndex(): Promise<RegistryEntry[]> {
    throw new Error("not fixtured: registryIndex");
  }

  async installRegistryItem(): Promise<{ job_id: string }> {
    throw new Error("not fixtured: installRegistryItem");
  }

  async updateRegistryItem(): Promise<{ job_id: string }> {
    throw new Error("not fixtured: updateRegistryItem");
  }

  async publishRegistryItems(): Promise<{ job_id: string }> {
    throw new Error("not fixtured: publishRegistryItems");
  }

  async getSessionLinks(sessionId: string): Promise<Link[]> {
    return [...this.links.values()].filter(
      (l) => l.granting_session_id === sessionId || l.subject_session_id === sessionId,
    );
  }

  async createLink(req: LinkRequest): Promise<Link> {
    if (req.granting_session_id === req.subject_session_id) {
      throw new RouterValidationError([
        { field: "subject_session_id", message: "a session cannot link to itself" },
      ]);
    }
    const reverse = [...this.links.values()].find(
      (l) =>
        l.granting_session_id === req.subject_session_id &&
        l.subject_session_id === req.granting_session_id,
    );
    if (reverse) {
      throw new RouterValidationError([
        {
          field: "subject_session_id",
          message: "a link already exists in the opposite direction",
        },
      ]);
    }
    const now = new Date().toISOString();
    const link: Link = {
      id: `link-${this.nextLinkSeq++}`,
      granting_session_id: req.granting_session_id,
      subject_session_id: req.subject_session_id,
      grants: req.grants ?? (req.preset ? PRESET_GRANTS[req.preset] : []),
      cadence: req.cadence ?? "on_signal",
      created_at: now,
      updated_at: now,
    };
    this.links.set(link.id, link);
    return link;
  }

  async updateLink(id: string, req: LinkRequest): Promise<Link> {
    const cur = this.links.get(id);
    const grants = req.grants ?? (req.preset ? PRESET_GRANTS[req.preset] : []);
    if (grants.length === 0) {
      throw new RouterValidationError([{ field: "grants", message: "must not be empty" }]);
    }
    const now = new Date().toISOString();
    const link: Link = {
      id,
      granting_session_id: cur?.granting_session_id ?? req.granting_session_id,
      subject_session_id: cur?.subject_session_id ?? req.subject_session_id,
      created_at: cur?.created_at ?? now,
      orchestrate_status: cur?.orchestrate_status,
      ...cur,
      grants,
      cadence: req.cadence ?? cur?.cadence ?? "on_signal",
      updated_at: now,
    };
    this.links.set(id, link);
    return link;
  }

  async deleteLink(id: string): Promise<void> {
    this.links.delete(id);
  }
}

// Settings & first-run (W6.10) fixtures.
export const fixtureConfig: Config = {
  synthesis_mode: "light",
  synthesis_auto_approve: false,
  agent_selection_mode: "full",
  default_model_tier: "balanced",
  pipeline_stage_timeout_minutes: 30,
  default_trust: "limited",
  display_mode: "comfortable",
  keybindings: "default",
  theme: "dark",
  palette: "clay",
  stream_speed: "off",
  concurrency_limit: 3,
  synthesis_debounce_ms: 2000,
  log_retention_days: 30,
  feature_flags: { multi_repo: false, linked_sessions: false, remote_registry: false },
  onboarding_complete: true,
};

export const fixtureFastModelStatus: FastModelStatus = {
  mode: "provider",
  provider_name: "claude-code",
  provider_completer: true,
};

export const fixtureSessionDefaults: SessionDefaults = {
  provider: { value: "claudecode", source: "config" },
  model: { value: "claude-sonnet-5", source: "provider_default" },
  agent: {},
  isolation: { value: "worktree", source: "builtin" },
  trust: { value: "limited", source: "config" },
};

export const fixtureProfile: Profile = {
  id: "prf_focus",
  name: "focus-mode",
  description: "Fewer distractions, cheaper model tier",
  snapshot: { default_model_tier: "cheapest", concurrency_limit: 1 },
  created_at: "2026-07-14T00:00:00Z",
  updated_at: "2026-07-14T00:00:00Z",
};

export const fixtureProfiles: Profile[] = [fixtureProfile];

// Spans native/emulated/unsupported across two providers so ProvidersPanel's
// table has real styling variance to assert against.
export const fixtureProviders: ProviderInfo[] = [
  {
    name: "claude-code",
    matrix: {
      interactive: "native",
      interrupt: "native",
      one_shot: "native",
      resume: "native",
      transcript: "native",
      model_discovery: "emulated",
      cost_reporting: "native",
      acp: "native",
      pty: "unsupported",
      branching: "unsupported",
      cwd_reroot: "unsupported",
    },
    intents: {
      deny_commands: "native",
      path_scope: "unsupported",
      read_only: "native",
      require_approval: "native",
    },
  },
  {
    name: "fake",
    matrix: {
      interactive: "native",
      interrupt: "native",
      one_shot: "native",
      resume: "unsupported",
      transcript: "emulated",
      model_discovery: "unsupported",
      cost_reporting: "emulated",
      acp: "unsupported",
      pty: "native",
      branching: "unsupported",
      cwd_reroot: "unsupported",
    },
  },
];

export const fixtureProviderBehaviors: BehaviorInfo[] = [
  { behavior: "interactive", level: "required" },
  { behavior: "interrupt", level: "required" },
  { behavior: "one_shot", level: "required" },
  { behavior: "resume", level: "expected" },
  { behavior: "transcript", level: "expected" },
  { behavior: "model_discovery", level: "expected" },
  { behavior: "cost_reporting", level: "expected" },
  { behavior: "acp", level: "expected" },
  { behavior: "pty", level: "expected" },
  { behavior: "branching", level: "optional" },
  { behavior: "cwd_reroot", level: "optional" },
];

export const fixturePreflightChecks: PreflightCheck[] = [
  { name: "git", healthy: true, detail: "git version 2.43.0" },
  { name: "data_service", healthy: true },
  {
    name: "provider:claude-code",
    healthy: false,
    detail: "not found on PATH",
    fix_hint: "install Claude Code and ensure `claude` is on PATH",
  },
];

export const fixtureVersionInfo: VersionInfo = { version: "0.1.0-dev" };

export const fixtureUpdateCheckResult: UpdateCheckResult = {
  current_version: "0.1.0-dev",
  update_available: false,
};
