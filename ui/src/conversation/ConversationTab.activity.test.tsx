// @vitest-environment jsdom
// Same jsdom rationale as ConversationTab.test.tsx: ChatRenderer renders
// markdown through DOMPurify, which doesn't sanitize under happy-dom.
//
// Integration coverage for the conversation-pane redesign wiring (T12–T17):
// subagent routing off the live stream, task-card lifecycle, the pinned
// substrip/bottom cluster, the V2 working-indicator ticker, and history
// parity from the control-plane event log.
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, act, within } from "@testing-library/react";
import { ConversationTab } from "./ConversationTab";
import { FakeRouterClient, fixtureSessions } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { Delegate, Envelope } from "../router/types";

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

  it("shows a running subagent once: its card while on screen, its strip once scrolled away", async () => {
    FakeIntersectionObserver.instances = [];
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
    const client = new FakeRouterClient();
    const source = await mountActive(client);

    act(() => {
      source.emit({
        kind: "tool_use",
        tool: { id: "tu1", name: "Task", kind: "agent", input: { description: "Color harmony" } },
      });
      source.emit({
        kind: "task",
        task: { phase: "started", task_id: "tk1", tool_use_id: "tu1", description: "Color harmony" },
      });
      // Claude Code narrates the current step in a progress line's description.
      source.emit({
        kind: "task",
        task: { phase: "progress", task_id: "tk1", description: "Searching for Ou Luo 2006", tool_uses: 3 },
      });
    });
    const card = screen.getByTestId("task-card");
    // The step ticks on the card, under the task's own name.
    expect(within(card).getByText("Color harmony")).toBeTruthy();
    expect(within(card).getByText("Searching for Ou Luo 2006")).toBeTruthy();

    setTaskCardOnScreen(card, true);
    expect(screen.queryByTestId("substrip")).toBeNull();

    setTaskCardOnScreen(card, false);
    const strip = screen.getByTestId("substrip");
    expect(within(strip).getByText("Color harmony")).toBeTruthy();
    expect(within(strip).getByText("Searching for Ou Luo 2006")).toBeTruthy();
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

  // A long session's log runs past GET /events' 500-envelope page: the replay
  // must read on, not stop at the oldest page.
  const longSession = (client: FakeRouterClient) => {
    vi.spyOn(client, "getTranscript").mockResolvedValue([
      {
        role: "tool",
        tool: { id: "t1", name: "Bash", kind: "execute", title: "go test ./ui/...", result: { text: "…" } },
        timestamp: "2026-09-09T10:00:01Z",
      },
      {
        role: "tool",
        tool: { id: "tu1", name: "Task", kind: "agent", input: { description: "Audit sweep idempotency" } },
        timestamp: "2026-09-09T10:00:02Z",
      },
    ]);
    // Another subagent's whole stream fills the first pages.
    for (let seq = 1; seq <= 1200; seq++) {
      client.seedEvent(
        env(seq, "session.subagent_event", {
          parent_tool_use_id: "tu_other",
          kind: "tool_use",
          tool: { id: `o${seq}`, name: "Read", kind: "read", title: `other ${seq}` },
        }),
      );
    }
    // And status changes for calls not on screen fill the replay's own first page.
    for (let seq = 1201; seq <= 1800; seq++) {
      client.seedEvent(env(seq, "session.tool_status_changed", { tool_use_id: `x${seq}`, status: "completed" }));
    }
    client.seedEvent(env(1801, "session.tool_status_changed", { tool_use_id: "t1", status: "failed" }));
    client.seedEvent(
      env(1802, "session.subagent_task", {
        phase: "started",
        task_id: "tk1",
        tool_use_id: "tu1",
        description: "Audit sweep idempotency",
        subagent_type: "general-purpose",
      }),
    );
    client.seedEvent(
      env(1803, "session.subagent_event", {
        parent_tool_use_id: "tu1",
        kind: "tool_use",
        tool: { id: "s1", name: "Bash", kind: "execute", title: "Recreate swept file" },
      }),
    );
  };

  it("replays a long session's log past its first page, without reading every subagent's stream", async () => {
    const client = new FakeRouterClient();
    longSession(client);
    client.delegateTranscripts.set("session-active/tk1", {
      available: true,
      messages: [
        { role: "user", text: "Check the sweep is idempotent.", timestamp: "2026-09-09T10:00:03Z" },
        { role: "agent", text: "From its own file.", timestamp: "2026-09-09T10:00:09Z" },
      ],
    });
    const getEvents = vi.spyOn(client, "getEvents");
    render(<ConversationTab sessionId="session-active" routerClient={client} />);

    // Status and lifecycle from past the first 500 envelopes both landed.
    await waitFor(() =>
      expect(screen.getAllByTestId("activity-group")[0].querySelector('[data-status="failed"]')).toBeTruthy(),
    );
    const card = await screen.findByTestId("task-card");
    expect(card.textContent).toContain("Audit sweep idempotency");

    // The dock reads the subagent's own file, named by its task id.
    fireEvent.click(card.querySelector("button")!);
    const dock = await screen.findByRole("dialog", { name: "Activity detail" });
    await waitFor(() => expect(dock.textContent).toContain("From its own file."));
    expect(dock.textContent).toContain("Check the sweep is idempotent.");
    expect(getEvents.mock.calls.some(([f]) => f.types?.includes("session.subagent_event"))).toBe(false);
  });

  it("with no file, reads every subagent stream back in full — once, when a dock needs it", async () => {
    const client = new FakeRouterClient();
    longSession(client);
    render(<ConversationTab sessionId="session-active" routerClient={client} />);
    const card = await screen.findByTestId("task-card");
    fireEvent.click(card.querySelector("button")!);
    const dock = await screen.findByRole("dialog", { name: "Activity detail" });
    await waitFor(() => expect(dock.textContent).toContain("Recreate swept file"));
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

describe("ConversationTab — permission cards on their calls", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders a live request under its tool row, with the harness's reason, and resolves from there", async () => {
    const client = new FakeRouterClient();
    const resolve = vi.spyOn(client, "resolvePermissionRequest");
    const source = await mountActive(client);

    act(() => {
      source.emit({ kind: "tool_use", tool: { id: "t1", name: "Read", kind: "read", title: "read /etc/hosts" } });
      source.emit({
        kind: "permission_request",
        permission: {
          id: "perm-1",
          tool: "Read",
          tool_use_id: "t1",
          status: "pending",
          reason: "Path is outside allowed working directories",
        },
      });
    });

    const row = screen.getByTestId("activity-row");
    const card = row.nextElementSibling as HTMLElement;
    expect(card.textContent).toContain("Path is outside allowed working directories");
    fireEvent.click(within(card).getByRole("button", { name: "Allow" }));
    expect(resolve).toHaveBeenCalledWith("session-active", "perm-1", "allow");
  });

  it("renders a subagent call's request inside its task card, and a cancelled one says so", async () => {
    const client = new FakeRouterClient();
    const source = await mountActive(client);

    act(() => {
      source.emit({
        kind: "tool_use",
        tool: { id: "tu1", name: "Task", kind: "agent", input: { description: "audit" } },
      });
      source.emit({
        kind: "tool_use",
        tool: { id: "sub1", name: "Bash", kind: "execute", title: "rm -rf build" },
        parent_tool_use_id: "tu1",
      });
      source.emit({
        kind: "permission_request",
        permission: { id: "perm-2", tool: "Bash", tool_use_id: "sub1", parent_tool_use_id: "tu1", status: "pending" },
      });
    });

    const taskCard = screen.getByTestId("task-card");
    expect(within(taskCard).getByRole("button", { name: "Deny" })).toBeTruthy();

    act(() => {
      source.emit({
        kind: "permission_request",
        permission: { id: "perm-2", tool: "Bash", tool_use_id: "sub1", parent_tool_use_id: "tu1", status: "cancelled" },
      });
    });
    expect(within(taskCard).getByText("cancelled — the agent stopped waiting")).toBeTruthy();
    expect(within(taskCard).queryByRole("button", { name: "Deny" })).toBeNull();
  });

  it("falls back to the trailing list for a request with no tool_use_id", async () => {
    const client = new FakeRouterClient();
    const source = await mountActive(client);

    act(() => {
      source.emit({ kind: "tool_use", tool: { id: "t1", name: "Read", kind: "read", title: "read a.go" } });
      source.emit({
        kind: "permission_request",
        permission: { id: "perm-3", tool: "WebFetch", status: "pending" },
      });
    });

    const allow = screen.getByRole("button", { name: "Allow" });
    expect(allow.closest('[data-testid="activity-group"]')).toBeNull();
    expect(screen.getByText("WebFetch")).toBeTruthy();
  });
});

// jsdom has no IntersectionObserver; this one only reports what a test says
// is on screen, so "off screen" is every card's starting state.
class FakeIntersectionObserver {
  static instances: FakeIntersectionObserver[] = [];
  readonly targets = new Set<Element>();
  constructor(readonly callback: IntersectionObserverCallback) {
    FakeIntersectionObserver.instances.push(this);
  }
  observe(el: Element) {
    this.targets.add(el);
  }
  unobserve(el: Element) {
    this.targets.delete(el);
  }
  disconnect() {
    this.targets.clear();
  }
  takeRecords() {
    return [];
  }
}

/** Reports every observed copy of a permission card as on/off screen. */
function setCardOnScreen(id: string, onScreen: boolean) {
  act(() => {
    for (const io of FakeIntersectionObserver.instances) {
      for (const target of io.targets) {
        if ((target as HTMLElement).dataset.permissionId !== id) continue;
        io.callback(
          [{ target, isIntersecting: onScreen } as IntersectionObserverEntry],
          io as unknown as IntersectionObserver,
        );
      }
    }
  });
}

/** Reports a task card as on/off screen to whichever observer watches it. */
function setTaskCardOnScreen(card: Element, onScreen: boolean) {
  act(() => {
    for (const io of FakeIntersectionObserver.instances) {
      if (!io.targets.has(card)) continue;
      io.callback(
        [{ target: card, isIntersecting: onScreen } as IntersectionObserverEntry],
        io as unknown as IntersectionObserver,
      );
    }
  });
}

const jumpButton = () =>
  screen.queryByRole("button", { name: /Approval needed|approvals waiting/ });

describe("ConversationTab — approval jump", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    FakeIntersectionObserver.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
    Element.prototype.scrollIntoView = vi.fn();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    delete (Element.prototype as Partial<Element>).scrollIntoView;
  });

  it("stays hidden with nothing pending, and while every pending card is on screen", async () => {
    const client = new FakeRouterClient();
    const source = await mountActive(client);
    act(() => {
      source.emit({ kind: "tool_use", tool: { id: "t1", name: "Read", kind: "read", title: "read hosts" } });
    });
    expect(jumpButton()).toBeNull();

    act(() => {
      source.emit({
        kind: "permission_request",
        permission: {
          id: "perm-1",
          tool: "Read",
          tool_use_id: "t1",
          input: { file_path: "/etc/hosts" },
          status: "pending",
        },
      });
    });
    // Off screen until the observer says otherwise.
    expect(jumpButton()?.textContent).toBe("Approval needed: Read hosts");

    setCardOnScreen("perm-1", true);
    expect(jumpButton()).toBeNull();
    setCardOnScreen("perm-1", false);
    expect(jumpButton()).toBeTruthy();

    act(() => {
      source.emit({
        kind: "permission_request",
        permission: { id: "perm-1", tool: "Read", tool_use_id: "t1", status: "allowed" },
      });
    });
    expect(jumpButton()).toBeNull();
  });

  it("names the subagent for one request, and counts several", async () => {
    const client = new FakeRouterClient();
    const source = await mountActive(client);
    act(() => {
      source.emit({
        kind: "tool_use",
        tool: { id: "tu1", name: "Task", kind: "agent", input: { description: "audit the parser" } },
      });
      source.emit({
        kind: "task",
        task: { phase: "started", task_id: "tk1", tool_use_id: "tu1", description: "audit the parser" },
      });
      source.emit({
        kind: "tool_use",
        tool: { id: "sub1", name: "Bash", kind: "execute", title: "clean" },
        parent_tool_use_id: "tu1",
      });
      source.emit({
        kind: "permission_request",
        permission: {
          id: "perm-1",
          tool: "Bash",
          tool_use_id: "sub1",
          parent_tool_use_id: "tu1",
          input: { command: "make clean\nmake all" },
          status: "pending",
        },
      });
    });
    expect(jumpButton()?.textContent).toBe("Approval needed: Bash make clean · audit the parser");
    // The announcement is its own live region, apart from the visibility-
    // gated button, so scrolling a card in and out never re-announces.
    const live = document.querySelector('[aria-live="polite"]')!;
    expect(live.textContent).toBe("Approval needed: Bash make clean · audit the parser");

    act(() => {
      source.emit({
        kind: "permission_request",
        permission: { id: "perm-2", tool: "WebFetch", status: "pending" },
      });
    });
    expect(jumpButton()?.textContent).toBe("2 approvals waiting");
    // Only the off-screen ones count toward the button.
    setCardOnScreen("perm-2", true);
    expect(jumpButton()?.textContent).toBe("Approval needed: Bash make clean · audit the parser");
  });

  it("jumps to the oldest pending card — reopening its collapsed group, flashing it, focusing Allow — and cycles", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValue([
      {
        role: "tool",
        tool: { id: "t1", name: "Edit", kind: "edit", title: "edit main.go" },
        timestamp: "2026-09-09T10:00:01Z",
      },
      { role: "agent", text: "now the tests", timestamp: "2026-09-09T10:00:02Z" },
      {
        role: "tool",
        tool: { id: "t2", name: "Bash", kind: "execute", title: "run tests" },
        timestamp: "2026-09-09T10:00:03Z",
      },
    ]);
    const source = await mountActive(client);
    await waitFor(() => expect(screen.getAllByTestId("activity-group")).toHaveLength(2));
    act(() => {
      source.emit({
        kind: "permission_request",
        permission: {
          id: "perm-1",
          tool: "Edit",
          tool_use_id: "t1",
          input: { file_path: "/repo/main.go" },
          status: "pending",
        },
      });
      source.emit({
        kind: "permission_request",
        permission: { id: "perm-2", tool: "Bash", tool_use_id: "t2", status: "pending" },
      });
    });

    // The user re-collapses the first group: its card leaves the DOM.
    const [first] = screen.getAllByTestId("activity-group");
    fireEvent.click(first.querySelector("button")!);
    expect(within(first).queryByRole("button", { name: "Allow" })).toBeNull();
    expect(jumpButton()?.textContent).toBe("2 approvals waiting");

    fireEvent.click(jumpButton()!);
    const card1 = document.querySelector<HTMLElement>('[data-permission-id="perm-1"]')!;
    expect(first.contains(card1)).toBe(true);
    expect(card1.scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "center" });
    expect(card1.hasAttribute("data-flash")).toBe(true);
    expect(document.activeElement).toBe(within(card1).getByRole("button", { name: "Allow" }));

    // Still off screen as far as the observer knows (mid-scroll): the next
    // click moves on rather than re-jumping…
    fireEvent.click(jumpButton()!);
    const card2 = document.querySelector<HTMLElement>('[data-permission-id="perm-2"]')!;
    expect(document.activeElement).toBe(within(card2).getByRole("button", { name: "Allow" }));
    // …and wraps around to the oldest.
    fireEvent.click(jumpButton()!);
    expect(document.activeElement).toBe(within(card1).getByRole("button", { name: "Allow" }));
  });

  it("shows for a background subagent's request after the turn ended, with no working indicator", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValue([
      {
        role: "tool",
        tool: { id: "tu1", name: "Task", kind: "agent", input: { description: "watch CI" } },
        timestamp: "2026-09-09T10:00:01Z",
      },
      { role: "agent", text: "CI watcher runs in the background", timestamp: "2026-09-09T10:00:02Z" },
    ]);
    const source = await mountActive(client);
    await screen.findByText("CI watcher runs in the background");
    act(() => {
      source.emit({
        kind: "permission_request",
        permission: {
          id: "perm-1",
          tool: "Bash",
          parent_tool_use_id: "tu1",
          input: { command: "gh run watch" },
          status: "pending",
        },
      });
    });
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByText(/blocked awaiting permission/)).toBeNull();
    expect(jumpButton()?.textContent).toBe("Approval needed: Bash gh run watch · subagent");

    fireEvent.click(jumpButton()!);
    const allow = within(screen.getByTestId("task-card")).getByRole("button", { name: "Allow" });
    expect(document.activeElement).toBe(allow);
  });

  it("counts a card shown in the open dock as on screen, and deciding from the main stream keeps the dock open", async () => {
    const client = new FakeRouterClient();
    const resolve = vi.spyOn(client, "resolvePermissionRequest");
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
      source.emit({
        kind: "tool_use",
        tool: { id: "sub1", name: "Bash", kind: "execute", title: "rm -rf build" },
        parent_tool_use_id: "tu1",
      });
      source.emit({
        kind: "permission_request",
        permission: { id: "perm-1", tool: "Bash", tool_use_id: "sub1", parent_tool_use_id: "tu1", status: "pending" },
      });
    });
    fireEvent.click(screen.getByTestId("task-card").querySelector("button")!);
    const dock = await screen.findByRole("dialog", { name: "Activity detail" });
    expect(within(dock).getByRole("button", { name: "Allow" })).toBeTruthy();

    // Either copy on screen is enough — here the dock's.
    expect(jumpButton()).toBeTruthy();
    const dockCard = dock.querySelector('[data-permission-id="perm-1"]')!;
    act(() => {
      for (const io of FakeIntersectionObserver.instances) {
        if (!io.targets.has(dockCard)) continue;
        io.callback(
          [{ target: dockCard, isIntersecting: true } as IntersectionObserverEntry],
          io as unknown as IntersectionObserver,
        );
      }
    });
    expect(jumpButton()).toBeNull();

    const mainAllow = within(screen.getByTestId("task-card")).getByRole("button", { name: "Allow" });
    fireEvent.mouseDown(mainAllow);
    fireEvent.click(mainAllow);
    expect(resolve).toHaveBeenCalledWith("session-active", "perm-1", "allow");
    expect(screen.getByRole("dialog", { name: "Activity detail" })).toBeTruthy();
  });
});

describe("ConversationTab — delegate records (docs/DELEGATES-DESIGN.md)", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const record = (state: Delegate["state"], extra: Partial<Delegate> = {}): Delegate => ({
    id: "dlg_1",
    session_id: "session-active",
    harness_id: "tk1",
    tool_use_id: "tu1",
    description: "audit",
    backgrounded: true,
    state,
    runs: 1,
    started_at: "2026-10-08T10:00:00Z",
    created_at: "2026-10-08T10:00:00Z",
    updated_at: "2026-10-08T10:00:00Z",
    ...extra,
  });

  function delegateChanged(d: Delegate) {
    const control = FakeEventSource.instances.find((s) => s.url.includes("session.delegate_changed"));
    expect(control).toBeTruthy();
    act(() => {
      control!.emit(
        "session.delegate_changed",
        JSON.stringify({ seq: 50, type: "session.delegate_changed", actor: "runner", session_id: "session-active", payload: d }),
      );
    });
  }

  async function spawnBackgrounded(source: Awaited<ReturnType<typeof mountActive>>) {
    act(() => {
      source.emit({
        kind: "tool_use",
        tool: { id: "tu1", name: "Agent", kind: "agent", input: { description: "audit" } },
      });
      source.emit({
        kind: "task",
        task: { phase: "started", task_id: "tk1", tool_use_id: "tu1", description: "audit", is_backgrounded: true },
      });
    });
  }

  it("keeps a backgrounded subagent running past its parent's turn until its record ends it", async () => {
    const client = new FakeRouterClient();
    client.delegates.push(record("running"));
    vi.spyOn(client, "getTranscript").mockResolvedValue([]);
    const source = await mountActive(client);
    await spawnBackgrounded(source);
    expect(screen.getByTestId("substrip")).toBeTruthy();

    await act(async () => {
      source.emit({ kind: "turn_end" });
      await Promise.resolve();
    });
    // The record still says running: the turn ending is not its ending.
    expect(screen.getByTestId("substrip")).toBeTruthy();

    delegateChanged(record("completed", { reason: "completed" }));
    await waitFor(() => expect(screen.queryByTestId("substrip")).toBeNull());
    const card = screen.getByTestId("task-card");
    expect(card.querySelector('[data-status="completed"]')).toBeTruthy();
  });

  it("renders a failed delegate as failed — not the green check any notification used to earn", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValue([]);
    const source = await mountActive(client);
    await spawnBackgrounded(source);
    act(() => {
      source.emit({ kind: "task", task: { phase: "notification", task_id: "tk1", status: "failed" } });
    });
    delegateChanged(record("failed", { reason: "failed" }));
    const card = await screen.findByTestId("task-card");
    await waitFor(() => expect(card.querySelector('[data-status="failed"]')).toBeTruthy());
    expect(card.querySelector('[data-status="completed"]')).toBeNull();
  });

  it("doesn't reopen the working indicator for a subagent's frames between turns", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getTranscript").mockResolvedValue([]);
    const source = await mountActive(client);
    await spawnBackgrounded(source);
    await act(async () => {
      source.emit({ kind: "turn_end" });
      await Promise.resolve();
    });
    await waitFor(() => expect(screen.queryByTestId("working-ticker")).toBeNull());

    act(() => {
      source.emit({ kind: "tool_use", tool: { id: "sub1", name: "Read" }, parent_tool_use_id: "tu1" });
      source.emit({ kind: "thinking", thinking_tokens: 300 });
      source.emit({ kind: "task", task: { phase: "progress", task_id: "tk1", tool_uses: 2 } });
    });
    expect(screen.queryByTestId("working-ticker")).toBeNull();
  });
});
