import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { DelegateView } from "./DelegateView";
import { FakeRouterClient } from "../../../router/fixtures";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import type { Delegate, Envelope } from "../../../router/types";

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
    await waitFor(() => expect(screen.getByText("swallowed errors")).toBeTruthy());
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
