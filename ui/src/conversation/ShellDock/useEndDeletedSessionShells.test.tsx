import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useEndDeletedSessionShells } from "./useEndDeletedSessionShells";
import { makeFakeShellHost } from "./fakeShellHost";
import { FakeEventSource } from "../../router/testFakeEventSource";

const info = (sessionId: string) => ({
  sessionId,
  shellId: `shell-${sessionId}`,
  cwd: "/",
  pid: 1,
  startedAt: 0,
  attached: false,
});

const deleted = (sessionId: string) =>
  JSON.stringify({ seq: 1, type: "session.deleted", actor: "user", session_id: sessionId });

describe("useEndDeletedSessionShells", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    delete (window as { mads?: unknown }).mads;
  });

  it("ends the shell of a session reported deleted", async () => {
    const fake = makeFakeShellHost();
    fake.setShells([info("ses_a"), info("ses_b")]);
    window.mads = { shell: fake.host };
    renderHook(() => useEndDeletedSessionShells());
    await waitFor(() => expect(fake.host.list).toHaveBeenCalled());
    await act(async () => {});

    act(() => FakeEventSource.instances[0].emit("session.deleted", deleted("ses_a")));

    expect(fake.host.endSession).toHaveBeenCalledTimes(1);
    expect(fake.host.endSession).toHaveBeenCalledWith("ses_a");
  });

  it("leaves the host alone for a session with no live shell", async () => {
    const fake = makeFakeShellHost();
    fake.setShells([info("ses_a")]);
    window.mads = { shell: fake.host };
    renderHook(() => useEndDeletedSessionShells());
    await act(async () => {});

    act(() => FakeEventSource.instances[0].emit("session.deleted", deleted("ses_other")));

    expect(fake.host.endSession).not.toHaveBeenCalled();
  });

  it("follows shells that start after it mounted", async () => {
    const fake = makeFakeShellHost();
    window.mads = { shell: fake.host };
    renderHook(() => useEndDeletedSessionShells());
    await act(async () => {});
    act(() => fake.setShells([info("ses_late")]));

    act(() => FakeEventSource.instances[0].emit("session.deleted", deleted("ses_late")));

    expect(fake.host.endSession).toHaveBeenCalledWith("ses_late");
  });

  it("subscribes to nothing outside Electron", () => {
    renderHook(() => useEndDeletedSessionShells());
    expect(FakeEventSource.instances).toHaveLength(0);
  });
});
