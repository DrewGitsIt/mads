import type { RouterClient, SessionEventSource } from "./client";
import type {
  CapabilitySummary,
  Mentionable,
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

/**
 * In-memory RouterClient for dev-mode demo mounting and component tests.
 * Every test outside client.test.ts should inject this rather than mocking
 * fetch — see ui/src/conversation/HANDOFF.md §3.
 */
export class FakeRouterClient implements RouterClient {
  constructor(
    private readonly sessions = fixtureSessions,
    private readonly transcripts = fixtureTranscripts,
    private readonly mentionables = fixtureMentionables,
    private readonly capabilities = fixtureCapabilities,
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
}
