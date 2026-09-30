// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: ChatRenderer renders
// markdown through DOMPurify, which doesn't sanitize under happy-dom.
//
// Integration coverage for the conversation-pane redesign wiring (T12–T17):
// subagent routing off the live stream, task-card lifecycle, the pinned
// substrip/bottom cluster, the V2 working-indicator ticker, and history
// parity from the control-plane event log.
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient, fixtureSessions } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { Envelope } from "../router/types";

vi.mock("@monaco-editor/react", () => ({
  Editor: () => <textarea data-testid="fake-monaco-editor" />,
}));

if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as unknown as MediaQueryList;
}

if (typeof window.ResizeObserver === "undefined") {
  window.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}

async function mountActive(client: FakeRouterClient) {
  render(<ConversationTab sessionId="session-active" routerClient={client} />);
  await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(0));
  await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
  return client.sessionEventSources.at(-1)!;
}

describe("ConversationTab — subagent presence (T15)", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("runs the full scripted lifecycle: card + substrip on task_started, ticking counters, inline summary on notification", async () => {
    const client = new FakeRouterClient();
    const source = await mountActive(client);

    // The spawning Agent tool_use anchors the in-stream card…
    act(() => {
      source.emit({
        kind: "tool_use",
        tool: {
          id: "tu1",
          name: "Task",
          kind: "agent",
          input: { description: "Verify classification end-to-end", subagent_type: "general-purpose" },
        },
      });
      source.emit({
        kind: "task",
        task: {
          phase: "started",
          task_id: "tk1",
          tool_use_id: "tu1",
          description: "Verify classification end-to-end",
          subagent_type: "general-purpose",
        },
      });
    });

    expect(screen.getByTestId("task-card")).toBeTruthy();
    // …and the pinned substrip appears while the task runs.
    expect(screen.getByTestId("substrip")).toBeTruthy();

    act(() => {
      source.emit({
        kind: "task",
        task: { phase: "progress", task_id: "tk1", total_tokens: 9800, tool_uses: 4, duration_ms: 27000 },
      });
    });
    expect(screen.getByTestId("task-counters").textContent).toBe("9.8k tok · 4 tools · 27.0s");

    act(() => {
      source.emit({
        kind: "task",
        task: {
          phase: "notification",
          task_id: "tk1",
          tool_use_id: "tu1",
          status: "completed",
          summary: "Classification verified end-to-end; one nit filed.",
        },
      });
    });
    // Completed variant: inline summary, substrip gone.
    expect(screen.getByText("Classification verified end-to-end; one nit filed.")).toBeTruthy();
    expect(screen.queryByTestId("substrip")).toBeNull();
  });

  it("routes parent-tagged events to the subagent's dock stream, never the main transcript", async () => {
    const client = new FakeRouterClient();
    const source = await mountActive(client);

    act(() => {
      source.emit({
        kind: "tool_use",
        tool: { id: "tu1", name: "Task", kind: "agent", input: { description: "audit" } },
      });
      source.emit({
        kind: "task",
        task: { phase: "started", task_id: "tk1", tool_use_id: "tu1", description: "audit" },
      });
      // Parent-tagged sidechain traffic — the interleave bug's trigger.
      source.emit({
        kind: "text",
        text: "SUBAGENT PROSE MUST NOT INTERLEAVE",
        parent_tool_use_id: "tu1",
        subagent_type: "general-purpose",
      });
      source.emit({
        kind: "tool_use",
        tool: { id: "sub1", name: "Bash", kind: "execute", title: "Drive create/edit/delete" },
        parent_tool_use_id: "tu1",
      });
    });

    // The main stream never shows the tagged events — the subagent's
    // current op is only allowed to surface on the substrip's ticker.
    expect(screen.queryByText("SUBAGENT PROSE MUST NOT INTERLEAVE")).toBeNull();
    const opSightings = screen.queryAllByText("Drive create/edit/delete");
    expect(opSightings.length).toBeGreaterThan(0);
    for (const el of opSightings) {
      expect(el.closest('[data-testid="substrip"]')).toBeTruthy();
    }

    // …but clicking the task card opens the subagent's full stream in the dock.
    fireEvent.click(screen.getByTestId("task-card").querySelector("button")!);
    const dock = await screen.findByRole("dialog", { name: "Activity detail" });
    expect(dock.textContent).toContain("SUBAGENT PROSE MUST NOT INTERLEAVE");
    expect(dock.textContent).toContain("Drive create/edit/delete");
  });

  it("marks a still-running task interrupted when the turn ends without a notification", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValue([]);
    const source = await mountActive(client);

    act(() => {
      source.emit({
        kind: "tool_use",
        tool: { id: "tu1", name: "Task", kind: "agent", input: { description: "audit" } },
      });
      source.emit({
        kind: "task",
        task: { phase: "started", task_id: "tk1", tool_use_id: "tu1", description: "audit" },
      });
    });
    expect(screen.getByTestId("substrip")).toBeTruthy();

    await act(async () => {
      source.emit({ kind: "turn_end" });
      await Promise.resolve();
    });
    await waitFor(() => expect(screen.queryByTestId("substrip")).toBeNull());
  });
});

describe("ConversationTab — V2 working indicator (T16)", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("streams the current-activity ticker: latest tool title, then Thinking from thinking_tokens ticks", async () => {
    const client = new FakeRouterClient();
    const source = await mountActive(client);

    act(() => {
      source.emit({
        kind: "tool_use",
        tool: { id: "t1", name: "Grep", kind: "search", title: "sweepDeletedFiles call sites" },
      });
    });
    expect(screen.getByTestId("working-ticker").textContent).toBe("sweepDeletedFiles call sites");
    // Precedence intact: the generic label still carries the pending tool.
    expect(screen.getByRole("status", { name: "Using Grep…" })).toBeTruthy();

    act(() => {
      source.emit({ kind: "tool_result", tool: { id: "t1", name: "" }, text: "3 hits" });
      source.emit({ kind: "thinking", thinking_tokens: 1200 });
    });
    expect(screen.getByTestId("working-ticker").textContent).toBe("Thinking · 1.2k tokens");
  });

  it("keeps substrip + indicator + composer together in the pinned bottom cluster", async () => {
    const client = new FakeRouterClient();
    const source = await mountActive(client);

    act(() => {
      source.emit({ kind: "tool_use", tool: { id: "t1", name: "Read", title: "runner.go" } });
      source.emit({
        kind: "task",
        task: { phase: "started", task_id: "tk1", tool_use_id: "tu1", description: "audit" },
      });
    });

    const pinned = screen.getByTestId("pinned-cluster");
    expect(pinned.querySelector('[data-testid="substrip"]')).toBeTruthy();
    expect(pinned.querySelector('[role="status"]')).toBeTruthy();
    expect(pinned.querySelector("textarea")).toBeTruthy();
  });

  it("a whole thinking block renders as a think row in the live group", async () => {
    const client = new FakeRouterClient();
    const source = await mountActive(client);

    act(() => {
      source.emit({ kind: "tool_use", tool: { id: "t1", name: "Read", title: "runner.go" } });
      source.emit({ kind: "thinking", text: "the sweep must also run on the PTY pump" });
    });
    expect(screen.getByText(/Thought · the sweep must also run/)).toBeTruthy();
  });
});

describe("ConversationTab — history parity (T17)", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const env = (seq: number, type: string, payload: unknown): Envelope => ({
    seq,
    timestamp: `2026-09-09T10:00:00Z`,
    type,
    session_id: "session-active",
    actor: "system",
    payload,
  });

  it("merges the event log by tool_use id: interrupted glyph on a resultless tool, thinking rows, interrupted-shape task, subagent dock stream", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValue([
      { role: "user", text: "root-cause the Files tab bug", timestamp: "2026-09-09T09:59:00Z" },
      {
        role: "tool",
        tool: { id: "t1", name: "Bash", kind: "execute", title: "go test ./ui/..." },
        timestamp: "2026-09-09T10:00:01Z",
      },
      {
        role: "tool",
        tool: { id: "tu1", name: "Task", kind: "agent", input: { description: "Audit sweep idempotency" } },
        timestamp: "2026-09-09T10:00:02Z",
      },
    ]);
    client.seedEvent(env(1, "session.tool_status_changed", { tool_use_id: "t1", status: "interrupted" }));
    client.seedEvent(env(2, "session.thinking", { text: "the ledger never distinguishes created vs edited" }));
    client.seedEvent(
      env(3, "session.subagent_task", {
        phase: "started",
        task_id: "tk1",
        tool_use_id: "tu1",
        description: "Audit sweep idempotency",
        subagent_type: "general-purpose",
      }),
    );
    client.seedEvent(
      env(4, "session.subagent_event", {
        parent_tool_use_id: "tu1",
        kind: "tool_use",
        tool: { id: "s1", name: "Bash", kind: "execute", title: "Recreate swept file" },
      }),
    );

    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(screen.getByText("root-cause the Files tab bug")).toBeTruthy());

    // The resultless Bash renders ⊘ on its (collapsed) group header.
    const group = await screen.findByTestId("activity-group");
    expect(group.querySelector('[data-status="interrupted"]')).toBeTruthy();

    // The historical thinking block joined the same group as a think row.
    fireEvent.click(group.querySelector("button")!);
    expect(screen.getByText(/Thought · the ledger never distinguishes/)).toBeTruthy();

    // The task card is interrupted-shaped (no notification, no live stream).
    const card = screen.getByTestId("task-card");
    expect(card.querySelector('[data-status="interrupted"]')).toBeTruthy();
    expect(card.querySelector('[data-status="in_progress"]')).toBeNull();

    // Its subagent stream replays into the dock from session.subagent_event.
    fireEvent.click(card.querySelector("button")!);
    const dock = await screen.findByRole("dialog", { name: "Activity detail" });
    expect(dock.textContent).toContain("Recreate swept file");
  });

  it("applies a live session.tool_status_changed envelope to the rendered row", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValue([
      {
        role: "tool",
        tool: { id: "t1", name: "Bash", kind: "execute", title: "long-running build", result: { text: "…" } },
        timestamp: "2026-09-09T10:00:01Z",
      },
    ]);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(screen.getByTestId("activity-group")).toBeTruthy());

    const controlSource = FakeEventSource.instances.find((s) =>
      s.url.includes("session.tool_status_changed"),
    );
    expect(controlSource).toBeTruthy();
    act(() => {
      controlSource!.emit(
        "session.tool_status_changed",
        JSON.stringify({
          seq: 10,
          type: "session.tool_status_changed",
          actor: "system",
          session_id: "session-active",
          payload: { tool_use_id: "t1", status: "failed" },
        }),
      );
    });
    const group = screen.getByTestId("activity-group");
    expect(group.querySelector('[data-status="failed"]')).toBeTruthy();
  });
});

describe("ConversationTab — bottom dock wiring (T13)", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("row click opens the dock; clicking another row retargets it; Esc closes", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValue([
      {
        role: "tool",
        tool: { id: "t1", name: "Bash", kind: "execute", title: "run the suite", result: { text: "ok" } },
        timestamp: "2026-09-09T10:00:01Z",
      },
      {
        role: "tool",
        tool: { id: "t2", name: "Read", kind: "read", title: "read runner.go", result: { text: "…" } },
        timestamp: "2026-09-09T10:00:02Z",
      },
    ]);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    const group = await screen.findByTestId("activity-group");
    fireEvent.click(group.querySelector("button")!); // expand

    fireEvent.click(screen.getByText("run the suite"));
    let dock = await screen.findByRole("dialog", { name: "Activity detail" });
    expect(dock.textContent).toContain("run the suite");

    // Retarget without closing.
    fireEvent.click(screen.getAllByText("read runner.go")[0]);
    dock = screen.getByRole("dialog", { name: "Activity detail" });
    expect(dock.textContent).toContain("read runner.go");

    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Activity detail" })).toBeNull();
  });
});

describe("ConversationTab — durable turn_ended reconciliation", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("finalizes a turn from session.turn_ended when the stream's own turn_end frame was lost", async () => {
    const client = new FakeRouterClient();
    const getTranscript = vi.spyOn(client, "getTranscript").mockResolvedValue([]);
    const source = await mountActive(client);

    // A live turn streams in… and then the end-of-turn burst overruns the
    // hub: the final text and turn_end frames never arrive on /output.
    act(() => {
      source.emit({ kind: "text", text: "working on it" });
      source.emit({
        kind: "tool_use",
        tool: { id: "t1", name: "Bash", kind: "execute", title: "make check" },
      });
      source.emit({ kind: "tool_result", tool: { id: "t1", name: "" }, text: "ok" });
    });

    // The durable control-plane envelope still fires (runner pushTurnEnded
    // appends it to the event log independently of the SSE stream)…
    getTranscript.mockResolvedValue([
      { role: "user", text: "prompt", timestamp: "2026-09-09T21:00:00Z" },
      { role: "agent", text: "the final answer", timestamp: "2026-09-09T21:05:00Z" },
    ]);
    const controlSource = FakeEventSource.instances.find((s) =>
      s.url.includes("session.turn_ended"),
    );
    expect(controlSource).toBeTruthy();
    act(() => {
      controlSource!.emit(
        "session.turn_ended",
        JSON.stringify({
          seq: 5,
          type: "session.turn_ended",
          actor: "system",
          session_id: "session-active",
          payload: { delta: "…" },
        }),
      );
    });

    // …and reconciles the pane: transcript refetched, final text visible.
    await waitFor(() => expect(screen.getByText("the final answer")).toBeTruthy());
  });
});

// Everything the pinned bottom cluster shows is fed by live /output frames,
// which the stream never replays — so a conversation reopened (navigating
// away and back) mid-turn, mid-compaction or mid-subagent has to rebuild it
// from the session record and the control-plane event log instead.
describe("ConversationTab — reopened mid-turn", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    localStorage.clear();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString();
  const env = (seq: number, timestamp: string, type: string, payload: unknown): Envelope => ({
    seq,
    timestamp,
    type,
    session_id: "session-active",
    actor: "system",
    payload,
  });
  const working = (client: FakeRouterClient, since: string, label?: string) =>
    vi.spyOn(client, "getSession").mockResolvedValue({
      ...fixtureSessions["session-active"],
      agent_state: { state: "working", label, since },
    });
  let nextSeq = 50;
  const controlEnvelope = (type: string, payload: unknown) => {
    const source = FakeEventSource.instances.find((s) => s.url.includes(type));
    expect(source).toBeTruthy();
    act(() => {
      source!.emit(type, JSON.stringify({ seq: nextSeq++, type, actor: "system", session_id: "session-active", payload }));
    });
  };

  it("shows the working indicator from the record's agent state, counting from the turn's real start", async () => {
    const client = new FakeRouterClient();
    working(client, iso(42_000), "Running bash · make check");
    await mountActive(client);

    const indicator = await screen.findByRole("status", { name: "Working…" });
    expect(screen.getByTestId("working-ticker").textContent).toBe("Running bash · make check");
    expect(indicator.textContent).toMatch(/4[2-3]s/);
  });

  it("ignores a stale working mirror on an inactive session", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getSession").mockResolvedValue({
      ...fixtureSessions["session-inactive"],
      agent_state: { state: "working", label: "Running bash · make check", since: iso(60_000) },
    });
    render(<ConversationTab sessionId="session-inactive" routerClient={client} />);
    await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
    expect(screen.queryByRole("status", { name: "Working…" })).toBeNull();
  });

  it("revives a subagent spawned inside the in-flight turn, but not an earlier turn's abandoned one", async () => {
    const client = new FakeRouterClient();
    working(client, iso(30_000));
    client.seedEvent(
      env(1, iso(600_000), "session.subagent_task", {
        phase: "started",
        task_id: "old",
        tool_use_id: "tu-old",
        description: "Abandoned audit",
      }),
    );
    client.seedEvent(
      env(2, iso(10_000), "session.subagent_task", {
        phase: "started",
        task_id: "now",
        tool_use_id: "tu-now",
        description: "Verify retry backoff",
      }),
    );
    await mountActive(client);

    const strip = await screen.findByTestId("substrip");
    expect(strip.textContent).toContain("Verify retry backoff");
    expect(strip.textContent).not.toContain("Abandoned audit");
  });

  it("follows session.agent_state live: working raises the indicator, idle closes the turn", async () => {
    const client = new FakeRouterClient();
    await mountActive(client);
    expect(screen.queryByRole("status", { name: "Working…" })).toBeNull();

    controlEnvelope("session.agent_state", { state: "working", label: "Reading runner.go", since: iso(5_000) });
    expect(await screen.findByRole("status", { name: "Working…" })).toBeTruthy();
    expect(screen.getByTestId("working-ticker").textContent).toBe("Reading runner.go");

    controlEnvelope("session.agent_state", { state: "idle", since: iso(0) });
    await waitFor(() => expect(screen.queryByRole("status", { name: "Working…" })).toBeNull());
  });

  it("restores a compaction still running on an active session", async () => {
    const client = new FakeRouterClient();
    client.seedEvent(env(1, iso(3_000), "session.compact", { phase: "start" }));
    await mountActive(client);
    expect(await screen.findByText("Compacting conversation…")).toBeTruthy();
  });

  it("restores an unacknowledged compaction failure, and keeps a dismissed one dismissed", async () => {
    const client = new FakeRouterClient();
    client.seedEvent(env(1, iso(90_000), "session.compact", { phase: "start" }));
    client.seedEvent(
      env(2, iso(60_000), "session.compact", { phase: "done", error: "Not enough messages to compact." }),
    );
    const { unmount } = render(<ConversationTab sessionId="session-active" routerClient={client} />);
    expect(await screen.findByText("Compaction failed: Not enough messages to compact.")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    unmount();

    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
    await waitFor(() => expect(client.sessionEventSources.length).toBeGreaterThan(1));
    expect(screen.queryByText(/Compaction failed/)).toBeNull();
  });

  it("never replays a successful compaction's transient confirmation", async () => {
    const client = new FakeRouterClient();
    client.seedEvent(env(1, iso(60_000), "session.compact", { phase: "done", success: true }));
    await mountActive(client);
    await waitFor(() => expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy());
    expect(screen.queryByText("Conversation compacted.")).toBeNull();
  });

  // The screenshot bug: a message queued behind a /compact, then the user
  // navigated away. The queue now lives in the session process, so the
  // reopened pane shows it, and when it goes out it lands as a user bubble.
  it("shows the session's queued messages on reopen, and a delivered one as the user's bubble", async () => {
    const client = new FakeRouterClient();
    working(client, iso(30_000), "Compacting…");
    const queued = { id: "q_1", text: "now wire the tasks count", queued_at: iso(10_000) };
    client.seedQueuedMessages("session-active", [queued]);
    await mountActive(client);

    expect(await screen.findByText("now wire the tasks count")).toBeTruthy();
    expect(screen.getByText("Queued")).toBeTruthy();

    controlEnvelope("session.queue_changed", { queued: [], delivered: queued });
    await waitFor(() => expect(screen.queryByText("Queued")).toBeNull());
    const bubble = screen.getByText("now wire the tasks count");
    expect(bubble.closest("[data-user-msg]")).toBeTruthy();
  });

  it("removing a queued message cancels it in the session", async () => {
    const client = new FakeRouterClient();
    working(client, iso(30_000));
    client.seedQueuedMessages("session-active", [
      { id: "q_1", text: "never mind this", queued_at: iso(5_000) },
    ]);
    const cancel = vi.spyOn(client, "cancelQueuedMessage");
    await mountActive(client);

    fireEvent.click(await screen.findByLabelText("Remove queued message"));
    expect(cancel).toHaveBeenCalledWith("session-active", "q_1");
    expect(screen.queryByText("never mind this")).toBeNull();
  });
});
