import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { useWorktreeCards } from "./useWorktreeCards";
import { FakeRouterClient } from "../../router/fixtures";
import { FakeEventSource } from "../../router/testFakeEventSource";
import type { Envelope } from "../../router/types";

const env = (seq: number, type: string, payload: unknown): Envelope =>
  ({ seq, type, timestamp: "2026-10-09T10:00:00Z", actor: "system", session_id: "ses", payload }) as Envelope;

describe("useWorktreeCards", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("reads the whole log back, not just its first page", async () => {
    const client = new FakeRouterClient();
    // The agent's own commits fill the first page; they draw no card.
    for (let seq = 1; seq <= 600; seq++) client.seedEvent(env(seq, "commit.created", { hash: `agent${seq}` }));
    client.seedEvent(env(601, "commit.created", { hash: "mine", source: "commit", message: "Late commit", drafted: true }));
    const { result } = renderHook(() => useWorktreeCards(client, "ses"));
    await waitFor(() => expect(result.current.cards).toHaveLength(1));
    expect(result.current.cards[0]).toMatchObject({ kind: "committed", hash: "mine", tip: true });
  });
});
