import { describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { useCheckoutBranch } from "./useCheckoutBranch";
import { FakeRouterClient, fixtureIsolatedSession, fixtureSessionDefaults } from "../../router/fixtures";
import type { Session } from "../../router/types";

const IN_PLACE: Session = {
  id: "s1",
  name: "in place",
  activity: "active",
  created_at: "",
  updated_at: "",
  dir: "/home/user/repo/notes",
};

function clientWithBranch(branch?: string) {
  const client = new FakeRouterClient();
  const spy = vi
    .spyOn(client, "getSessionDefaults")
    .mockResolvedValue({ ...fixtureSessionDefaults, dir_branch: branch });
  return { client, spy };
}

describe("useCheckoutBranch", () => {
  it("reads the branch of an in-place session's folder from dir_branch", async () => {
    const { client, spy } = clientWithBranch("feat/header");
    const { result } = renderHook(() => useCheckoutBranch(client, IN_PLACE));
    await waitFor(() => expect(result.current).toBe("feat/header"));
    expect(spy).toHaveBeenCalledWith(undefined, "/home/user/repo/notes");
  });

  it("re-reads after a turn, since the agent may have switched branches", async () => {
    const { client, spy } = clientWithBranch("main");
    const { rerender } = renderHook(({ s }) => useCheckoutBranch(client, s), {
      initialProps: { s: IN_PLACE },
    });
    await waitFor(() => expect(spy).toHaveBeenCalledTimes(1));
    rerender({ s: { ...IN_PLACE, last_turn_at: "2026-10-05T00:00:00Z" } });
    await waitFor(() => expect(spy).toHaveBeenCalledTimes(2));
  });

  // An isolated session's branch is on its record; nothing to ask.
  it("asks nothing for an isolated session or one with no folder", () => {
    const { client, spy } = clientWithBranch("main");
    renderHook(() => useCheckoutBranch(client, fixtureIsolatedSession));
    renderHook(() => useCheckoutBranch(client, { ...IN_PLACE, dir: undefined, cwd: "." }));
    expect(spy).not.toHaveBeenCalled();
  });
});
