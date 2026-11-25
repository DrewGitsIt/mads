import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { ProvisioningStrip } from "./ProvisioningStrip";
import { FakeEventSource } from "../router/testFakeEventSource";
import { resetEventsBusForTests } from "../router/events";
import type { Envelope, Session } from "../router/types";

function emitStep(source: FakeEventSource, seq: number, step: string) {
  act(() => {
    source.emit(
      "session.provisioning",
      JSON.stringify({
        seq,
        type: "session.provisioning",
        actor: "system",
        session_id: "s1",
        payload: { step },
      }),
    );
  });
}

// A client whose record reads `activity`, resolved when the test says so.
function recordClient(activity: Session["activity"]) {
  let resolve!: () => void;
  const ready = new Promise<void>((r) => (resolve = r));
  const getSession = vi.fn(async () => {
    await ready;
    return { id: "s1", activity } as Session;
  });
  return { client: { getSession, getEvents: vi.fn(async (): Promise<Envelope[]> => []) }, resolve };
}

describe("ProvisioningStrip", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("streams provisioning steps in as session.provisioning events arrive", () => {
    render(<ProvisioningStrip sessionId="s1" />);
    const source = FakeEventSource.instances[0];

    expect(screen.getByText("Setting up worktree").className).toMatch(/active/);
    expect(screen.getByText("Ready").className).toMatch(/pending/);

    emitStep(source, 1, "worktree");
    emitStep(source, 2, "hook");

    expect(screen.getByText("Setting up worktree").className).toMatch(/done/);
    expect(screen.getByText("Installing commit hook").className).toMatch(/done/);
    expect(screen.getByText("Connecting capabilities").className).toMatch(/active/);
  });

  it("marks only the step in progress with the moving spinner", () => {
    const { container } = render(<ProvisioningStrip sessionId="s1" />);
    emitStep(FakeEventSource.instances[0], 1, "worktree");

    const spinners = container.querySelectorAll('[class*="statusMarkSpin"]');
    expect(spinners).toHaveLength(1);
    expect(screen.getByText("Installing commit hook").contains(spinners[0])).toBe(true);
  });

  it("unmounts on provider_ready, the runner's last step", () => {
    const onDone = vi.fn();
    render(<ProvisioningStrip sessionId="s1" onDone={onDone} />);

    emitStep(FakeEventSource.instances[0], 1, "provider_ready");

    expect(screen.queryByRole("status", { name: "Session provisioning" })).toBeNull();
    expect(onDone).toHaveBeenCalledOnce();
  });

  it("unmounts once session.activity_changed fires and calls onDone", () => {
    const onDone = vi.fn();
    render(<ProvisioningStrip sessionId="s1" onDone={onDone} />);
    const source = FakeEventSource.instances[0];

    act(() => {
      source.emit(
        "session.activity_changed",
        JSON.stringify({
          seq: 2,
          type: "session.activity_changed",
          actor: "system",
          session_id: "s1",
        }),
      );
    });

    expect(screen.queryByRole("status", { name: "Session provisioning" })).toBeNull();
    expect(onDone).toHaveBeenCalledOnce();
  });

  it("unmounts when the record already reads active — a start that finished before it subscribed", async () => {
    const onDone = vi.fn();
    const { client, resolve } = recordClient("active");
    render(<ProvisioningStrip sessionId="s1" routerClient={client} onDone={onDone} />);
    expect(screen.getByRole("status", { name: "Session provisioning" })).toBeTruthy();

    resolve();

    await waitFor(() => expect(onDone).toHaveBeenCalledOnce());
    expect(screen.queryByRole("status", { name: "Session provisioning" })).toBeNull();
    expect(client.getSession).toHaveBeenCalledWith("s1");
  });

  it("stays up while the record reads inactive", async () => {
    const onDone = vi.fn();
    const { client, resolve } = recordClient("inactive");
    render(<ProvisioningStrip sessionId="s1" routerClient={client} onDone={onDone} />);

    resolve();
    await act(async () => {});

    expect(screen.getByRole("status", { name: "Session provisioning" })).toBeTruthy();
    expect(onDone).not.toHaveBeenCalled();
  });
  describe("the history seed (the bus never replays provisioning)", () => {
    const step = (seq: number, name: string): Envelope => ({
      seq,
      timestamp: "2026-11-20T00:00:00Z",
      type: "session.provisioning",
      actor: "system",
      session_id: "s1",
      payload: { step: name },
    });
    const activity = (seq: number): Envelope => ({
      seq,
      timestamp: "2026-11-20T00:00:00Z",
      type: "session.activity_changed",
      actor: "system",
      session_id: "s1",
      payload: { activity: "active" },
    });
    const client = (history: Envelope[] | Promise<Envelope[]>) => ({
      getSession: vi.fn(async () => ({ id: "s1", activity: "inactive" }) as Session),
      getEvents: vi.fn(() => Promise.resolve(history)),
    });

    it("paints steps that happened before it subscribed, from one scoped read", async () => {
      const c = client([step(1, "worktree"), step(2, "hook")]);
      render(<ProvisioningStrip sessionId="s1" routerClient={c} />);
      await waitFor(() => expect(screen.getByText("Connecting capabilities").className).toMatch(/active/));
      expect(c.getEvents).toHaveBeenCalledWith(
        expect.objectContaining({ sessionId: "s1", types: ["session.provisioning", "session.activity_changed"] }),
      );
    });

    it("merges history and live steps by seq, history in front of what arrived live", async () => {
      let answer!: (v: Envelope[]) => void;
      const c = client(new Promise<Envelope[]>((r) => (answer = r)));
      render(<ProvisioningStrip sessionId="s1" routerClient={c} />);
      emitStep(FakeEventSource.instances[0], 3, "mcp_up");
      await act(async () => answer([step(1, "worktree"), step(2, "hook"), step(3, "mcp_up")]));
      expect(screen.getByText("Connecting capabilities").className).toMatch(/done/);
      expect(screen.getByText("Starting agent").className).toMatch(/active/);
    });

    it("hides when the history already holds provider_ready", async () => {
      const onDone = vi.fn();
      render(<ProvisioningStrip sessionId="s1" routerClient={client([step(1, "worktree"), step(2, "provider_ready")])} onDone={onDone} />);
      await waitFor(() => expect(onDone).toHaveBeenCalledOnce());
      expect(screen.queryByRole("status", { name: "Session provisioning" })).toBeNull();
    });

    it("hides on an activity change after the latest start's last step, not on one from an earlier life", async () => {
      const onDone = vi.fn();
      const { unmount } = render(
        <ProvisioningStrip sessionId="s1" routerClient={client([activity(1), step(2, "worktree")])} onDone={onDone} />,
      );
      await waitFor(() => expect(screen.getByText("Installing commit hook").className).toMatch(/active/));
      expect(onDone).not.toHaveBeenCalled();
      unmount();

      render(<ProvisioningStrip sessionId="s1" routerClient={client([step(1, "worktree"), activity(2)])} onDone={onDone} />);
      await waitFor(() => expect(onDone).toHaveBeenCalledOnce());
    });

    // The ready barrier: nothing is read until the bus knows its cursor, so
    // a step landing between the subscribe and the read arrives live (it is
    // after the cursor) and merges with the read by seq.
    it("reads only after the head, and a step between subscribe and snapshot is not lost", async () => {
      resetEventsBusForTests();
      let head!: (r: Response) => void;
      vi.stubGlobal(
        "fetch",
        vi.fn((url: string) =>
          new URL(url).pathname === "/events/head" ? new Promise<Response>((r) => (head = r)) : Promise.resolve(new Response("[]")),
        ),
      );
      let answer!: (v: Envelope[]) => void;
      const c = client(new Promise<Envelope[]>((r) => (answer = r)));
      render(<ProvisioningStrip sessionId="s1" routerClient={c} />);
      await act(async () => {});
      expect(c.getEvents).not.toHaveBeenCalled();
      expect(c.getSession).not.toHaveBeenCalled();

      await act(async () => head(new Response('{"seq":1}')));
      await waitFor(() => expect(c.getEvents).toHaveBeenCalled());
      expect(new URL(FakeEventSource.instances[0].url).searchParams.get("since")).toBe("1");

      emitStep(FakeEventSource.instances[0], 2, "hook");
      await act(async () => answer([step(1, "worktree"), step(2, "hook")]));
      expect(screen.getByText("Setting up worktree").className).toMatch(/done/);
      expect(screen.getByText("Installing commit hook").className).toMatch(/done/);
      expect(screen.getByText("Connecting capabilities").className).toMatch(/active/);
    });
  });
});
