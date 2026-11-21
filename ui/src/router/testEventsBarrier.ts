// Drives the event bus's ready barrier (router/events.ts eventsReady) in a
// consumer test. setupTests primes every test's bus at cursor 0, so the
// barrier is already open and a consumer that reads its REST snapshot
// before the barrier would pass anyway. This puts the bus back on the
// production first connect, with the head read held until the test says.
import { act } from "@testing-library/react";
import { vi } from "vitest";
import { resetEventsBusForTests } from "./events";

export interface HeldEventsHead {
  /** Answers the held GET /events/head with `seq` and lets the barrier open. */
  release(seq: number): Promise<void>;
  /** How many GET /events/head reads went out. */
  headReads(): number;
}

/**
 * Resets the bus to "head unknown" and holds its GET /events/head. Call it
 * after the test's own fetch stub (if any): every other request goes to
 * that fetch. The typical test:
 *
 *   const head = holdEventsHead();
 *   render(...)                         // subscribes; the head read is held
 *   await act(async () => {});
 *   expect(snapshotRead).not.toHaveBeenCalled();   // nothing before ready
 *   ...change what the snapshot will say (an event before the head)...
 *   await head.release(7);              // the stream starts after seq 7
 *   ...emit a live event (seq 8) before the snapshot answers...
 *   ...assert the consumer shows both...
 */
export function holdEventsHead(): HeldEventsHead {
  resetEventsBusForTests();
  const inner = globalThis.fetch;
  let answer!: (r: Response) => void;
  const held = new Promise<Response>((r) => (answer = r));
  let reads = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (new URL(url, "http://localhost").pathname === "/events/head") {
        reads++;
        return held;
      }
      return inner(input, init);
    }),
  );
  return {
    async release(seq: number) {
      await act(async () => {
        answer(new Response(JSON.stringify({ seq }), { status: 200 }));
      });
      await act(async () => {});
    },
    headReads: () => reads,
  };
}
