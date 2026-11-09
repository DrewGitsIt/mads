import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { aroundOpen, useOpenDelegateRecords } from "./useOpenDelegateRecords";
import { FakeRouterClient } from "../../../router/fixtures";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import type { Delegate } from "../../../router/types";

function rec(harness_id: string, extra: Partial<Delegate> = {}): Delegate {
  return {
    id: `dlg_${harness_id}`,
    session_id: "s1",
    harness_id,
    state: "completed",
    ended_at: "2026-01-01T00:00:00Z",
    runs: 1,
    started_at: "2026-01-01T00:00:00Z",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...extra,
  };
}

const ids = (list?: Delegate[]) => list?.map((d) => d.harness_id).sort();

describe("aroundOpen", () => {
  it("takes the open delegate, its ancestors and its direct children — not its grandchildren or siblings", () => {
    const list = [
      rec("g"),
      rec("m", { parent_harness_id: "g" }),
      rec("sib", { parent_harness_id: "g" }),
      rec("kid", { parent_harness_id: "m" }),
      rec("grandkid", { parent_harness_id: "kid" }),
    ];
    expect(ids(aroundOpen(new Map(list.map((d) => [d.harness_id, d])), "m"))).toEqual(["g", "kid", "m"]);
  });
});

describe("useOpenDelegateRecords", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("reads the open delegate's session once, follows its records live, and lets go when it closes", async () => {
    const client = new FakeRouterClient();
    client.delegates.push(rec("m"), rec("kid", { parent_harness_id: "m", state: "running", ended_at: undefined }));
    const read = vi.spyOn(client, "getSessionDelegates");
    const { result, rerender } = renderHook(({ open }) => useOpenDelegateRecords(client, open), {
      initialProps: { open: { sessionId: "s1", harnessId: "m" } as { sessionId: string; harnessId: string } | undefined },
    });
    await waitFor(() => expect(ids(result.current)).toEqual(["kid", "m"]));
    expect(read).toHaveBeenCalledTimes(1);

    // A child that finishes after the read doesn't keep spinning from it.
    const source = FakeEventSource.instances.find((s) => s.url.includes("session.delegate_changed"))!;
    act(() => {
      source.emit(
        "session.delegate_changed",
        JSON.stringify({
          seq: 1,
          timestamp: "2026-01-01T00:01:00Z",
          type: "session.delegate_changed",
          actor: "runner",
          session_id: "s1",
          payload: rec("kid", { parent_harness_id: "m" }),
        }),
      );
    });
    await waitFor(() => expect(result.current?.find((d) => d.harness_id === "kid")?.state).toBe("completed"));

    rerender({ open: undefined });
    expect(result.current).toBeUndefined();
    expect(read).toHaveBeenCalledTimes(1);
  });
});
