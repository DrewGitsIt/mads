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

  function emitStep(source: FakeEventSource, seq: number, payload: Record<string, unknown>) {
    act(() => {
      source.emit(
        "session.provisioning",
        JSON.stringify({
          seq,
          type: "session.provisioning",
          actor: "system",
          session_id: "s1",
          payload,
        }),
      );
    });
  }

  it("says the session uses the current directory when the worktree step ran in place", () => {
    render(<ProvisioningStrip sessionId="s1" />);
    const source = FakeEventSource.instances[0];

    expect(screen.getByText("Setting up worktree").className).toMatch(/active/);
    emitStep(source, 1, { step: "worktree", mode: "in_place" });

    expect(screen.getByText("Using current directory").className).toMatch(/done/);
    expect(screen.queryByText("Setting up worktree")).toBeNull();
  });

  it("keeps the worktree wording for mode worktree and for old rows without a mode", () => {
    render(<ProvisioningStrip sessionId="s1" />);
    const source = FakeEventSource.instances[0];

    emitStep(source, 1, { step: "worktree", mode: "worktree" });
    expect(screen.getByText("Setting up worktree").className).toMatch(/done/);

    emitStep(source, 2, { step: "worktree" });
    expect(screen.getByText("Setting up worktree").className).toMatch(/done/);
    expect(screen.queryByText("Using current directory")).toBeNull();
  });

  it("hides the hook step once it reports skipped, and keeps it when installed or an old row", () => {
    render(<ProvisioningStrip sessionId="s1" />);
    const source = FakeEventSource.instances[0];

    emitStep(source, 1, { step: "worktree", mode: "in_place" });
    expect(screen.getByText("Installing commit hook").className).toMatch(/active/);

    emitStep(source, 2, { step: "hook" });
    expect(screen.getByText("Installing commit hook").className).toMatch(/done/);

    emitStep(source, 3, { step: "hook", skipped: true });
    expect(screen.queryByText("Installing commit hook")).toBeNull();
    expect(screen.getByText("Connecting capabilities").className).toMatch(/active/);
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
