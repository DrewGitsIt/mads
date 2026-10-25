import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { ProvisioningStrip } from "./ProvisioningStrip";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { Session } from "../router/types";

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
  return { client: { getSession }, resolve };
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
});
