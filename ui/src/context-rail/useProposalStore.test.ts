import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useProposalStore } from "./useProposalStore";
import { FakeRouterClient } from "../router/fixtures";
import { FakeEventSource } from "../router/testFakeEventSource";
import type { Envelope, Proposal } from "../router/types";

let nextSeq = 1;
function emit(type: string) {
  const env: Envelope = {
    seq: nextSeq++,
    timestamp: new Date().toISOString(),
    actor: "test",
    type,
    session_id: "session-active",
  } as Envelope;
  FakeEventSource.instances[0].emit(type, JSON.stringify(env));
}

describe("useProposalStore", () => {
  beforeEach(() => {
    nextSeq = 1;
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("never lets an older list response land after a newer one", async () => {
    const client = new FakeRouterClient();
    const pending: ((p: Proposal[]) => void)[] = [];
    const listProposals = vi
      .spyOn(client, "listProposals")
      .mockImplementation(() => new Promise<Proposal[]>((answer) => pending.push(answer)));
    const { result } = renderHook(() => useProposalStore(client, "session-active"));
    await waitFor(() => expect(pending).toHaveLength(1));

    const older = [{ id: "prop_old" }] as Proposal[];
    const newer = [{ id: "prop_old" }, { id: "prop_new" }] as Proposal[];

    // An event while the mount read is in flight: no second read in
    // parallel, one follow-up after the first settles.
    emit("synthesis.proposal_created");
    emit("synthesis.proposal_created");
    await new Promise((r) => setTimeout(r, 80));
    expect(listProposals).toHaveBeenCalledTimes(1);

    await act(async () => pending[0](older));
    expect(result.current.map((p) => p.id)).toEqual(["prop_old"]);
    await waitFor(() => expect(pending).toHaveLength(2));
    await act(async () => pending[1](newer));
    expect(result.current.map((p) => p.id)).toEqual(["prop_old", "prop_new"]);
    expect(listProposals).toHaveBeenCalledTimes(2);
  });
});
