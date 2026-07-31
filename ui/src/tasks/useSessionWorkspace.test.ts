import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { useSessionWorkspace } from "./useSessionWorkspace";
import { FakeRouterClient, fixtureWorkspaces } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { Session } from "../router/types";

describe("useSessionWorkspace", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("resolves the session's workspace by name", async () => {
    const client = new FakeRouterClient();
    const { result } = renderHook(() => useSessionWorkspace(client, "session-active"));

    await waitFor(() => expect(result.current).not.toBeNull());
    expect(result.current?.name).toBe(fixtureWorkspaces[0].name);
  });

  it("resolves to null when the session has no workspace", async () => {
    const session: Session = {
      id: "session-no-workspace",
      name: "No workspace",
      activity: "active",
      created_at: "t",
      updated_at: "t",
    };
    const client = new FakeRouterClient({ "session-no-workspace": session });
    const { result } = renderHook(() => useSessionWorkspace(client, "session-no-workspace"));

    await waitFor(() => expect(client.getSession("session-no-workspace")).resolves.toBeTruthy());
    expect(result.current).toBeNull();
  });
});
