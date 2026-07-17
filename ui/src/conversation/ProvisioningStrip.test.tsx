import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { ProvisioningStrip } from "./ProvisioningStrip";
import { FakeEventSource } from "../router/testFakeEventSource";

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

    expect(screen.getByText("Ready").className).toMatch(/pending/);

    act(() => {
      source.emit(
        "session.provisioning",
        JSON.stringify({
          seq: 1,
          type: "session.provisioning",
          actor: "system",
          session_id: "s1",
          payload: { step: "provider_ready" },
        }),
      );
    });

    expect(screen.getByText("Ready").className).toMatch(/done/);
    expect(screen.getByText("Setting up worktree").className).toMatch(/done/);
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
});
