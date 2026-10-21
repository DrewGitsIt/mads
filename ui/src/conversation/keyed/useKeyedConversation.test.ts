import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { FakeRouterClient } from "../../router/fixtures";
import type { TranscriptMessage, TranscriptPage } from "../../router/types";
import { useKeyedConversation } from "./useKeyedConversation";

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

function setup(transcript: TranscriptMessage[] = []) {
  const client = new FakeRouterClient();
  client.transcripts["s1"] = transcript;
  const statuses: string[] = [];
  const renders: string[][] = [];
  const hook = renderHook(() => {
    const conv = useKeyedConversation(client, "s1", { stream: true, onStatus: (k) => statuses.push(k) });
    renders.push(conv.rows.map((r) => `${r.key}=${r.text ?? ""}`));
    return conv;
  });
  const src = () => client.v2Sources[client.v2Sources.length - 1].source;
  return { client, hook, src, statuses, renders };
}

const keys = (rows: { key: string }[]) => rows.map((r) => r.key);

describe("useKeyedConversation", () => {
  it("mounts with the last page and the stream, and merges by key", async () => {
    const { hook, src, client } = setup([{ role: "user", key: "u0", text: "hi" }]);
    await waitFor(() => expect(keys(hook.result.current.rows)).toEqual(["u0"]));
    expect(client.transcriptPageCalls[0]).toEqual({ sessionId: "s1", limit: 100 });
    expect(client.v2Sources[0].after).toBeUndefined();
    act(() => {
      src().emitFrame("hello", { epoch: "e", seq: 1, keyed: true, resume: "fresh", turn: { active: true }, queued: [] });
      src().emitFrame("item_start", { key: "a1", item: "text" }, "e:2");
      src().emitFrame("text_delta", { key: "a1", text: "streamed" }, "e:3");
      src().emitFrame("replay_end", {}, "e:3");
    });
    expect(keys(hook.result.current.rows)).toEqual(["u0", "a1"]);
    expect(hook.result.current.state.turn.active).toBe(true);
    expect(hook.result.current.state.lastId).toBe("e:3");
  });

  it("swaps a resync page in atomically: no blank render, frames after it held until the swap", async () => {
    const { hook, src, client, renders } = setup([{ role: "user", key: "u0", text: "hi" }]);
    await waitFor(() => expect(keys(hook.result.current.rows)).toEqual(["u0"]));
    act(() => {
      src().emitFrame("hello", { epoch: "e", seq: 1, keyed: true, resume: "fresh", turn: { active: true }, queued: [] });
      src().emitFrame("item_start", { key: "a1", item: "text" }, "e:2");
      src().emitFrame("text_delta", { key: "a1", text: "old" }, "e:3");
      src().emitFrame("replay_end", {}, "e:3");
    });
    const page = deferred<TranscriptPage>();
    client.getTranscriptPage = () => page.promise;
    renders.length = 0;
    act(() => {
      src().emitFrame("hello", { epoch: "e2", seq: 0, keyed: true, resume: "resync", turn: { active: true }, queued: [] });
      src().emitFrame("resync", { reason: "epoch" });
      src().emitFrame("item_start", { key: "a2", item: "text" }, "e2:1");
      src().emitFrame("text_delta", { key: "a2", text: "new" }, "e2:2");
      src().emitFrame("replay_end", {}, "e2:2");
    });
    // Until the page lands, the screen still shows what it had.
    expect(keys(hook.result.current.rows)).toEqual(["u0", "a1"]);
    await act(async () => {
      page.resolve({ rows: [{ role: "user", key: "u0", text: "hi" }, { role: "agent", key: "a1", text: "old, stored" }], has_more: false });
      await page.promise;
    });
    expect(hook.result.current.rows.map((r) => `${r.key}=${r.text}`)).toEqual(["u0=hi", "a1=old, stored", "a2=new"]);
    for (const r of renders) expect(r.length).toBeGreaterThanOrEqual(2); // never blank, never lost the user row
  });

  it("refreshes the last page once at turn end and reports status frames", async () => {
    const { hook, src, client, statuses } = setup();
    await waitFor(() => expect(client.transcriptPageCalls).toHaveLength(1));
    client.transcripts["s1"] = [{ role: "agent", key: "a1", text: "done", timestamp: "t" }];
    act(() => {
      src().emitFrame("hello", { epoch: "e", seq: 0, keyed: true, resume: "fresh", turn: { active: false }, queued: [] });
      src().emitFrame("replay_end", {}, "e:0");
      src().emitFrame("item_start", { key: "a1", item: "text" }, "e:1");
      src().emitFrame("text_delta", { key: "a1", text: "done" }, "e:2");
      src().emitFrame("turn_end", {}, "e:3");
    });
    await waitFor(() => expect(client.transcriptPageCalls).toHaveLength(2));
    await waitFor(() => expect(hook.result.current.rows[0].timestamp).toBe("t"));
    expect(statuses).toEqual(["hello", "turn_end"]);
  });

  it("reattach resumes after the last frame seen", async () => {
    const { hook, src, client } = setup();
    await waitFor(() => expect(client.transcriptPageCalls).toHaveLength(1));
    act(() => {
      src().emitFrame("hello", { epoch: "e", seq: 0, keyed: true, resume: "fresh", turn: { active: false }, queued: [] });
      src().emitFrame("replay_end", {}, "e:7");
    });
    const first = src();
    act(() => hook.result.current.reattach());
    expect(first.closed).toBe(true);
    expect(client.v2Sources[client.v2Sources.length - 1].after).toBe("e:7");
  });

  it("loads older pages before the cursor", async () => {
    const rows: TranscriptMessage[] = Array.from({ length: 150 }, (_, i) => ({ role: "agent", key: `k${i}`, text: `${i}` }));
    const { hook } = setup(rows);
    await waitFor(() => expect(hook.result.current.rows).toHaveLength(100));
    expect(hook.result.current.state.hasMore).toBe(true);
    await act(() => hook.result.current.loadOlder());
    expect(hook.result.current.rows).toHaveLength(150);
    expect(hook.result.current.rows[0].key).toBe("k0");
    expect(hook.result.current.state.hasMore).toBe(false);
  });

  it("a reopened stream that starts fresh merges the last page: what settled while it was down", async () => {
    const { hook, src, client } = setup([{ role: "user", key: "u0", text: "hi" }]);
    await waitFor(() => expect(keys(hook.result.current.rows)).toEqual(["u0"]));
    // The first connection was dead on arrival (a dormant session): no frames, no cursor.
    act(() => hook.result.current.reattach());
    client.transcripts["s1"] = [
      { role: "user", key: "u0", text: "hi" },
      { role: "user", key: "user:1", text: "wake up" },
      { role: "agent", key: "a1", text: "awake" },
    ];
    act(() => {
      src().emitFrame("hello", { epoch: "e", seq: 9, keyed: true, resume: "fresh", turn: { active: false }, queued: [] });
      src().emitFrame("replay_end", {}, "e:9");
    });
    await waitFor(() => expect(keys(hook.result.current.rows)).toEqual(["u0", "user:1", "a1"]));
    expect(client.transcriptPageCalls).toHaveLength(2);
    expect(hook.result.current.state.replaying).toBe(false);
  });

  it("the first connection's fresh hello fetches nothing more", async () => {
    const { src, client } = setup([{ role: "user", key: "u0", text: "hi" }]);
    await waitFor(() => expect(client.transcriptPageCalls).toHaveLength(1));
    act(() => {
      src().emitFrame("hello", { epoch: "e", seq: 0, keyed: true, resume: "fresh", turn: { active: false }, queued: [] });
      src().emitFrame("replay_end", {}, "e:0");
    });
    await new Promise((r) => setTimeout(r, 0));
    expect(client.transcriptPageCalls).toHaveLength(1);
  });
});
