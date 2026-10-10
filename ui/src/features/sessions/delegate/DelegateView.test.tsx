import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { DelegateView } from "./DelegateView";
import { FakeRouterClient } from "../../../router/fixtures";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import type { Delegate, DelegateTranscript, Envelope } from "../../../router/types";

const record: Delegate = {
  id: "dlg_1",
  session_id: "s1",
  harness_id: "a1",
  tool_use_id: "toolu_spawn",
  description: "Audit error handling",
  type: "Explore",
  backgrounded: true,
  state: "running",
  runs: 1,
  started_at: "2026-10-08T10:00:00Z",
  created_at: "2026-10-08T10:00:00Z",
  updated_at: "2026-10-08T10:00:00Z",
};

/** The stream row titled `title` — the working indicator's ticker repeats an in-flight one's title. */
function row(title: string): HTMLElement | undefined {
  return screen.queryAllByTestId("activity-row").find((r) => r.textContent?.includes(title));
}

function env(seq: number, type: string, payload: unknown): Envelope {
  return { seq, timestamp: "2026-10-08T10:00:01Z", type, actor: "runner", session_id: "s1", payload } as Envelope;
}

describe("DelegateView", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("shows the delegate's record and its own stream, and follows it live", async () => {
    const client = new FakeRouterClient();
    client.delegates.push(record);
    vi.spyOn(client, "getEvents").mockResolvedValue([
      env(1, "session.subagent_event", {
        parent_tool_use_id: "toolu_spawn",
        kind: "tool_use",
        tool: { id: "t1", name: "Grep", kind: "search", title: "swallowed errors" },
      }),
      // Another delegate's event never leaks in.
      env(2, "session.subagent_event", { parent_tool_use_id: "toolu_other", kind: "text", text: "not mine" }),
    ]);
    const onBack = vi.fn();
    render(<DelegateView sessionId="s1" harnessId="a1" parentName="Parent" routerClient={client} onBack={onBack} />);

    expect(await screen.findByRole("heading", { name: "Audit error handling" })).toBeTruthy();
    expect(screen.getByText("Explore")).toBeTruthy();
    await waitFor(() => expect(row("swallowed errors")).toBeTruthy());
    expect(screen.queryByText("not mine")).toBeNull();
    expect(screen.getByTestId("delegate-view").querySelector('[data-status="in_progress"]')).toBeTruthy();

    // The record ends live, on the shared control-plane stream.
    const control = FakeEventSource.instances.find((s) => s.url.includes("session.delegate_changed"));
    act(() => {
      control!.emit(
        "session.delegate_changed",
        JSON.stringify(env(9, "session.delegate_changed", { ...record, state: "failed", reason: "failed" })),
      );
    });
    await waitFor(() =>
      expect(screen.getByTestId("delegate-view").querySelector('[data-status="failed"]')).toBeTruthy(),
    );
    expect(screen.getByText(/Failed/)).toBeTruthy();

    fireEvent.click(screen.getByText("← Parent"));
    expect(onBack).toHaveBeenCalled();
  });

  it("says so when the delegate has no record", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "getEvents").mockResolvedValue([]);
    render(<DelegateView sessionId="s1" harnessId="gone" routerClient={client} onBack={() => {}} />);
    expect(await screen.findByText(/record isn't available/)).toBeTruthy();
  });
});

describe("DelegateView — capabilities", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("states what the delegate supports as resolved for it, without offering controls", async () => {
    const client = new FakeRouterClient();
    client.delegates.push({
      ...record,
      worktree_path: "/wt/a1",
      capabilities: { transcript: true, continuable: true, steer: "relay", stop: false, stops_with_parent: true },
    });
    vi.spyOn(client, "getEvents").mockResolvedValue([]);
    render(<DelegateView sessionId="s1" harnessId="a1" routerClient={client} onBack={() => {}} />);
    const caps = await screen.findByTestId("delegate-capabilities");
    expect(caps.textContent).toContain("Own transcript on disk");
    expect(caps.textContent).toContain("Reachable through its parent");
    expect(caps.textContent).toContain("Stops with its parent");
    expect(caps.textContent).toContain("Could continue as a session");
    expect(caps.textContent).toContain("Worktree /wt/a1");
    expect(caps.querySelector("button")).toBeNull();
  });

  it("reads watch-only for a delegate nothing can reach", async () => {
    const client = new FakeRouterClient();
    client.delegates.push({
      ...record,
      capabilities: { transcript: false, continuable: false, steer: "none", stop: false, stops_with_parent: true },
    });
    vi.spyOn(client, "getEvents").mockResolvedValue([]);
    render(<DelegateView sessionId="s1" harnessId="a1" routerClient={client} onBack={() => {}} />);
    const caps = await screen.findByTestId("delegate-capabilities");
    expect(caps.textContent).toContain("Watch only");
    expect(caps.textContent).toContain("No transcript of its own");
    expect(caps.textContent).not.toContain("continue");
  });
});

describe("DelegateView — the delegate's own transcript", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("reads the stream from the delegate's own file over the live record", async () => {
    const client = new FakeRouterClient();
    client.delegates.push(record);
    client.delegateTranscripts.set("s1/a1", {
      available: true,
      messages: [
        { role: "user", text: "Audit the error handling in api/" },
        { role: "tool", tool: { id: "t1", name: "Grep", kind: "search", title: "swallowed errors" } },
        { role: "agent", text: "Three handlers drop the error." },
      ],
    });
    vi.spyOn(client, "getEvents").mockResolvedValue([
      env(1, "session.subagent_event", {
        parent_tool_use_id: "toolu_spawn",
        kind: "tool_use",
        tool: { id: "t9", name: "Read", kind: "read", title: "only in the event log" },
      }),
    ]);
    render(<DelegateView sessionId="s1" harnessId="a1" routerClient={client} onBack={() => {}} />);

    const prompt = await screen.findByTestId("subagent-message");
    expect(prompt.textContent).toContain("Prompt");
    expect(prompt.textContent).toContain("Audit the error handling in api/");
    expect(screen.getByText("Three handlers drop the error.")).toBeTruthy();
    expect(row("swallowed errors")).toBeTruthy();
    expect(screen.queryByText("only in the event log")).toBeNull();
    expect(screen.getByText(/From its own transcript/)).toBeTruthy();
  });

  it("falls back to what mads recorded live when there is no file", async () => {
    const client = new FakeRouterClient();
    client.delegates.push(record);
    vi.spyOn(client, "getEvents").mockResolvedValue([
      env(1, "session.subagent_event", {
        parent_tool_use_id: "toolu_spawn",
        kind: "tool_use",
        tool: { id: "t1", name: "Grep", kind: "search", title: "swallowed errors" },
      }),
    ]);
    render(<DelegateView sessionId="s1" harnessId="a1" routerClient={client} onBack={() => {}} />);
    await waitFor(() => expect(row("swallowed errors")).toBeTruthy());
    expect(await screen.findByText(/As mads recorded it live/)).toBeTruthy();
  });

  it("re-reads the file once live activity settles", async () => {
    const client = new FakeRouterClient();
    client.delegates.push(record);
    vi.spyOn(client, "getEvents").mockResolvedValue([]);
    const read = vi.spyOn(client, "getDelegateTranscript");
    render(<DelegateView sessionId="s1" harnessId="a1" routerClient={client} onBack={() => {}} />);
    await screen.findByText(/As mads recorded it live/);
    const before = read.mock.calls.length;

    // The harness wrote more; a live event for the delegate is the cue.
    client.delegateTranscripts.set("s1/a1", {
      available: true,
      messages: [{ role: "agent", text: "Written since the first read." }],
    });
    const stream = FakeEventSource.instances.find((s) => s.url.includes("session.subagent_event"));
    act(() => {
      stream!.emit(
        "session.subagent_event",
        JSON.stringify(env(5, "session.subagent_event", { parent_tool_use_id: "toolu_spawn", kind: "tool_use", tool: { id: "t2", name: "Read" } })),
      );
    });
    expect(await screen.findByText("Written since the first read.", undefined, { timeout: 3000 })).toBeTruthy();
    expect(read.mock.calls.length).toBe(before + 1);
  });
});

describe("DelegateView — following it as it runs", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => vi.unstubAllGlobals());

  const inFlight = {
    available: true,
    messages: [
      { role: "user", text: "Find the swallowed errors." },
      { role: "tool", tool: { id: "t1", name: "Grep", kind: "search", title: "done already" } },
      { role: "tool_result", tool: { id: "t1", name: "Grep" }, text: "3 matches" },
      { role: "tool", tool: { id: "t2", name: "Bash", kind: "execute", title: "still going" } },
    ],
  } as DelegateTranscript;

  function mount() {
    const client = new FakeRouterClient();
    client.delegates.push(record);
    vi.spyOn(client, "getEvents").mockResolvedValue([]);
    client.delegateTranscripts.set("s1/a1", inFlight);
    render(<DelegateView sessionId="s1" harnessId="a1" routerClient={client} onBack={() => {}} />);
    return client;
  }

  function end() {
    const control = FakeEventSource.instances.find((s) => s.url.includes("session.delegate_changed"));
    act(() => {
      control!.emit(
        "session.delegate_changed",
        JSON.stringify(env(9, "session.delegate_changed", { ...record, state: "completed", reason: "completed" })),
      );
    });
  }

  function rowStatus(title: string) {
    return row(title)?.querySelector("[data-status]")?.getAttribute("data-status");
  }

  // The group's own status — a group collapses once when it stops being live, as in the main stream.
  function groupStatus() {
    const groups = screen.getAllByTestId("activity-group");
    return groups[groups.length - 1].querySelector("button [data-status]")?.getAttribute("data-status");
  }

  it("spins a call still in flight and names it in the working indicator, until the delegate ends", async () => {
    mount();
    await waitFor(() => expect(row("still going")).toBeTruthy());
    expect(rowStatus("done already")).toBe("completed");
    expect(rowStatus("still going")).toBe("in_progress");
    expect(groupStatus()).toBe("in_progress");
    const indicator = screen.getByRole("status", { name: "Using Bash…" });
    expect(indicator.textContent).toContain("still going");

    // Ended with no result: that call was cut off, and nothing is working.
    end();
    await waitFor(() => expect(screen.queryByRole("status", { name: /Using|Working/ })).toBeNull());
    expect(groupStatus()).toBe("interrupted");
  });

  it("opens a call's full detail in the dock, and closes it on Escape", async () => {
    mount();
    fireEvent.click(await screen.findByText("done already"));
    const dock = screen.getByRole("dialog", { name: "Activity detail" });
    expect(dock.textContent).toContain("3 matches");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Activity detail" })).toBeNull();
  });

  it("opens at the tail and follows it, but lets go once the user scrolls up", async () => {
    mount();
    const stream = screen.getByTestId("delegate-stream");
    let top = 0;
    Object.defineProperty(stream, "scrollHeight", { configurable: true, get: () => 2000 });
    Object.defineProperty(stream, "clientHeight", { configurable: true, get: () => 500 });
    Object.defineProperty(stream, "scrollTop", {
      configurable: true,
      get: () => top,
      set: (v: number) => (top = v),
    });
    await waitFor(() => expect(row("still going")).toBeTruthy());
    expect(top).toBe(2000);

    top = 200;
    fireEvent.scroll(stream);
    end();
    await waitFor(() => expect(groupStatus()).toBe("interrupted"));
    expect(top).toBe(200);
  });
});

describe("DelegateView — a long session's log", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("reads its lifecycle and, with no file, its stream from past the log's first page", async () => {
    const client = new FakeRouterClient();
    client.delegates.push(record);
    // Other subagents' streams fill the first pages.
    for (let seq = 1; seq <= 1200; seq++) {
      client.seedEvent(
        env(seq, "session.subagent_event", {
          parent_tool_use_id: "toolu_other",
          kind: "tool_use",
          tool: { id: `o${seq}`, name: "Read", kind: "read", title: `other ${seq}` },
        }),
      );
    }
    client.seedEvent(
      env(1201, "session.subagent_task", {
        phase: "progress",
        task_id: "a1",
        tool_use_id: "toolu_spawn",
        total_tokens: 4200,
        tool_uses: 7,
      }),
    );
    client.seedEvent(
      env(1202, "session.subagent_event", {
        parent_tool_use_id: "toolu_spawn",
        kind: "tool_use",
        tool: { id: "t1", name: "Grep", kind: "search", title: "late in the log" },
      }),
    );
    render(<DelegateView sessionId="s1" harnessId="a1" routerClient={client} onBack={() => {}} />);
    await waitFor(() => expect(row("late in the log")).toBeTruthy());
    expect(screen.getByTestId("delegate-counters").textContent).toContain("7 tools");
  });
});
