import { describe, expect, it, vi } from "vitest";
import { EVENTS_BATCH_LIMIT, getAllEvents } from "./eventPages";
import { FakeRouterClient } from "./fixtures";
import type { Envelope } from "./types";

const env = (seq: number, type = "session.thinking"): Envelope =>
  ({ seq, timestamp: "2026-10-09T10:00:00Z", type, actor: "runner", session_id: "s1", payload: {} }) as Envelope;

describe("getAllEvents", () => {
  it("reads page after page from the last seq until a short page", async () => {
    const client = new FakeRouterClient();
    for (let seq = 1; seq <= EVENTS_BATCH_LIMIT * 2 + 7; seq++) client.seedEvent(env(seq));
    const read = vi.spyOn(client, "getEvents");
    const all = await getAllEvents(client, { sessionId: "s1", types: ["session.thinking"] });
    expect(all.map((e) => e.seq)).toEqual(Array.from({ length: EVENTS_BATCH_LIMIT * 2 + 7 }, (_, i) => i + 1));
    expect(read.mock.calls.map(([f]) => f.since)).toEqual([undefined, EVENTS_BATCH_LIMIT, EVENTS_BATCH_LIMIT * 2]);
  });

  it("keeps the caller's starting cursor and filters", async () => {
    const client = new FakeRouterClient();
    client.seedEvent(env(1));
    client.seedEvent(env(2, "session.compact"));
    client.seedEvent(env(3));
    const all = await getAllEvents(client, { sessionId: "s1", since: 1, types: ["session.thinking"] });
    expect(all.map((e) => e.seq)).toEqual([3]);
  });
});
