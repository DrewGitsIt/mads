import { describe, expect, it } from "vitest";
import type { TranscriptMessage } from "../../router/types";
import {
  conversationReducer,
  emptyConversation,
  orderedRows,
  subagentRows,
  type ConvAction,
  type ConvState,
  type Frame,
} from "./conversation";

const f = (kind: string, data: Record<string, unknown> = {}, id?: string): ConvAction => ({
  type: "frame",
  frame: { kind, data: { kind, ...data }, id } as Frame,
});
const run = (s: ConvState, ...actions: ConvAction[]) => actions.reduce(conversationReducer, s);
const view = (s: ConvState) => orderedRows(s).map((r) => `${r.role}:${r.key}=${r.text ?? r.tool?.name ?? ""}${r.live ? "*" : ""}`);
const hello = (resume = "fresh", extra: Record<string, unknown> = {}) =>
  f("hello", { epoch: "e1", seq: 0, keyed: true, resume, turn: { active: false }, queued: [], ...extra });
const page = (rows: TranscriptMessage[], hasMore = false): ConvAction => ({ type: "page", rows, hasMore });

const textItem = (key: string, ...deltas: string[]): ConvAction[] => [
  f("item_start", { key, item: "text" }),
  ...deltas.map((t) => f("text_delta", { key, text: t })),
  f("item_stop", { key }),
];

describe("conversationReducer", () => {
  it("builds live rows from frames, a tool's call and result under one key", () => {
    const s = run(
      emptyConversation(),
      hello(),
      f("replay_end"),
      f("user_message", { key: "user:1", text: "go", ts: "2026-10-21T00:00:00Z" }),
      ...textItem("m1:0", "Hel", "lo"),
      f("item_start", { key: "t1", item: "tool_use" }),
      f("tool_use", { key: "t1", tool: { id: "t1", name: "ls" } }),
      f("tool_result", { key: "t1", text: "a.txt", is_error: false }),
    );
    expect(view(s)).toEqual(["user:user:1=go*", "agent:m1:0=Hello*", "tool:t1=ls*"]);
    expect(orderedRows(s)[2].tool?.result?.text).toBe("a.txt");
    expect(s.turn.active).toBe(true);
    expect(orderedRows(s)[1].paced).toBe(true);
  });

  it("marks replayed rows unpaced and remembers the last frame id", () => {
    const s = run(emptyConversation(), hello(), ...textItem("m1:0", "replayed"), f("replay_end", {}, "e1:4"));
    expect(orderedRows(s)[0].paced).toBe(false);
    expect(s.lastId).toBe("e1:4");
    expect(s.replaying).toBe(false);
  });

  it("drops frames for a key the stored page already has", () => {
    const s = run(
      emptyConversation(),
      page([{ role: "agent", key: "m1:0", text: "stored" }]),
      hello(),
      ...textItem("m1:0", "dup"),
      f("user_message", { key: "m1:0", text: "x" }),
      f("replay_end"),
    );
    expect(view(s)).toEqual(["agent:m1:0=stored"]);
  });

  it("a page row replaces the live row in place, keeping its key and pacing", () => {
    let s = run(emptyConversation(), hello(), f("replay_end"), f("user_message", { key: "user:1", text: "go" }), ...textItem("m1:0", "Hi"));
    s = run(s, f("settled", { keys: ["user:1", "m1:0"] }));
    expect(view(s)).toEqual(["user:user:1=go", "agent:m1:0=Hi"]);
    s = run(
      s,
      page([
        { role: "user", key: "user:1", text: "go", timestamp: "t" },
        { role: "agent", key: "m1:0", text: "Hi", timestamp: "t" },
      ]),
    );
    expect(view(s)).toEqual(["user:user:1=go", "agent:m1:0=Hi"]);
    expect(s.rows["m1:0"].paced).toBe(true);
    expect(s.rows["m1:0"].timestamp).toBe("t");
  });

  it("keeps older stored rows ahead of the last page and settled rows the page lacks after it", () => {
    let s = run(
      emptyConversation(),
      page(
        [
          { role: "user", key: "u0", text: "a" },
          { role: "agent", key: "a0", text: "b" },
        ],
        true,
      ),
    );
    expect(s.cursor).toBe("u0");
    expect(s.hasMore).toBe(true);
    s = run(s, hello(), f("replay_end"), f("user_message", { key: "u1", text: "c" }), ...textItem("a1", "d"));
    s = run(s, f("settled", { keys: ["u1", "a1"] }));
    // A page read before a1 was stored, overlapping from a0.
    s = run(s, page([{ role: "agent", key: "a0", text: "b" }, { role: "user", key: "u1", text: "c" }], true));
    expect(orderedRows(s).map((r) => r.key)).toEqual(["u0", "a0", "u1", "a1"]);
    expect(s.cursor).toBe("u0"); // older rows stayed ahead: the cursor is still theirs
  });

  it("prepends an older page and moves the cursor", () => {
    let s = run(emptyConversation(), page([{ role: "agent", key: "a1", text: "new" }], true));
    s = run(s, { type: "olderPage", rows: [{ role: "user", key: "u0", text: "old" }], hasMore: false });
    expect(orderedRows(s).map((r) => r.key)).toEqual(["u0", "a1"]);
    expect(s.cursor).toBe("u0");
    expect(s.hasMore).toBe(false);
  });

  it("folds a stored tool_result into its call by key", () => {
    const s = run(
      emptyConversation(),
      page([
        { role: "tool", key: "t1", tool: { id: "t1", name: "ls" } },
        { role: "tool_result", key: "t1", text: "out", tool: { id: "t1", name: "ls" } },
      ]),
    );
    expect(view(s)).toEqual(["tool:t1=ls"]);
    expect(s.rows.t1.tool?.result?.text).toBe("out");
  });

  it("unsaved keeps agent rows and marks a user send failed, in place", () => {
    let s = run(emptyConversation(), hello(), f("replay_end"), f("user_message", { key: "u1", text: "hi" }), ...textItem("a1", "partial"));
    s = run(s, f("unsaved", { keys: ["u1", "a1"] }));
    expect(view(s)).toEqual(["user:u1=hi*", "agent:a1=partial*"]);
    expect(s.rows.u1.failed).toBe(true);
    expect(s.rows.a1.unsaved).toBe(true);
  });

  it("swap drops the stale live rows and merges the page in one action", () => {
    let s = run(emptyConversation(), page([{ role: "user", key: "u0", text: "a" }]), hello(), f("replay_end"));
    s = run(s, ...textItem("gone", "x"), ...textItem("kept", "y"));
    s = run(s, {
      type: "swap",
      rows: [
        { role: "user", key: "u0", text: "a" },
        { role: "agent", key: "kept", text: "y" },
      ],
      hasMore: false,
      drop: "live",
    });
    expect(view(s)).toEqual(["user:u0=a", "agent:kept=y"]);
  });

  it("swap for tail_truncated drops only the evicted keys", () => {
    let s = run(emptyConversation(), hello(), ...textItem("old", "x"), ...textItem("live", "y"), f("replay_end"));
    s = run(s, { type: "swap", rows: [{ role: "agent", key: "old", text: "x stored" }], hasMore: false, drop: ["old"] });
    expect(view(s)).toEqual(["agent:old=x stored", "agent:live=y*"]);
  });

  it("a full-tail replay replaces live text instead of doubling it", () => {
    let s = run(emptyConversation(), hello(), f("replay_end"), ...textItem("a1", "one ", "two"));
    s = run(s, hello("resync"), f("item_start", { key: "a1", item: "text" }), f("text_delta", { key: "a1", text: "one two three" }));
    s = run(s, f("replay_end"));
    expect(view(s)).toEqual(["agent:a1=one two three*"]);
  });

  it("a resumed replay continues an item", () => {
    let s = run(emptyConversation(), hello(), f("replay_end"), f("item_start", { key: "a1", item: "text" }), f("text_delta", { key: "a1", text: "one " }));
    s = run(s, hello("resumed"), f("text_delta", { key: "a1", text: "two" }), f("replay_end"));
    expect(view(s)).toEqual(["agent:a1=one two*"]);
  });

  it("keeps subagent rows out of the main list", () => {
    const s = run(
      emptyConversation(),
      hello(),
      f("item_start", { key: "s1", item: "text", parent_tool_use_id: "task1" }),
      f("text_delta", { key: "s1", text: "sub" }),
      ...textItem("m1", "main"),
    );
    expect(view(s)).toEqual(["agent:m1=main*"]);
    expect(subagentRows(s, "task1").map((r) => r.text)).toEqual(["sub"]);
  });

  it("hello seeds the turn and queue; turn_end closes open items", () => {
    let s = run(emptyConversation(), hello("fresh", { turn: { active: true, started_at: "t0" }, queued: [{ id: "q", text: "later", queued_at: "t" }] }));
    expect(s.turn).toEqual({ active: true, startedAt: "t0", interrupted: false });
    expect(s.queued.map((q) => q.text)).toEqual(["later"]);
    s = run(s, f("replay_end"), f("item_start", { key: "a", item: "text" }), f("turn_end"));
    expect(s.turn.active).toBe(false);
    expect(s.rows.a.open).toBe(false);
  });

  it("a live user_message opens its turn (a queue delivery too); a replayed one doesn't", () => {
    // The runner sends a queued message's frame at delivery, as it hands it
    // to the provider (runner.sendTurn), so it opens a turn like any send.
    const queued = run(emptyConversation(), hello(), f("replay_end"), f("user_message", { key: "u", text: "q", queued: true }));
    expect(queued.turn.active).toBe(true);
    const replayed = run(emptyConversation(), hello(), f("user_message", { key: "u", text: "q" }), f("replay_end"));
    expect(replayed.turn.active).toBe(false);
  });

  it("rows stay current through settling and the stored copy, until the next turn starts", () => {
    let s = run(emptyConversation(), hello(), f("replay_end"), f("user_message", { key: "user:1", text: "go" }));
    s = run(s, f("item_start", { key: "t1", item: "tool_use" }), f("tool_use", { key: "t1", tool: { id: "t1", name: "ls" } }));
    s = run(s, f("settled", { keys: ["user:1", "t1"] }), page([{ role: "user", key: "user:1", text: "go" }, { role: "tool", key: "t1", tool: { id: "t1", name: "ls" } }]));
    expect(s.rows.t1.live).toBeFalsy();
    expect(s.rows.t1.current).toBe(true);
    s = run(s, f("turn_end"));
    expect(s.rows.t1.current).toBe(true); // the renderer folds it once the reveal drains
    s = run(s, f("user_message", { key: "user:2", text: "again" }));
    expect(s.rows.t1.current).toBe(false);
    expect(s.rows["user:2"].current).toBe(true);
  });

  it("a live item with no turn known to run starts one; a replayed or subagent one doesn't", () => {
    const idle = run(emptyConversation(), hello(), f("replay_end"));
    expect(run(idle, f("item_start", { key: "a", item: "text" })).turn.active).toBe(true);
    expect(run(idle, f("inbound", { key: "i", text: "from outside" })).turn.active).toBe(true);
    expect(run(idle, f("item_start", { key: "s", item: "text", parent_tool_use_id: "task1" })).turn.active).toBe(false);
    expect(run(emptyConversation(), hello(), f("item_start", { key: "a", item: "text" })).turn.active).toBe(false);
  });

  it("a turn started from outside (Stop's opposite: agent_state working) clears the last turn's rows", () => {
    let s = run(emptyConversation(), hello(), f("replay_end"), ...textItem("a", "done"), f("turn_end"));
    expect(s.rows.a.current).toBe(true);
    s = run(s, { type: "turn", turn: { active: true, startedAt: "t1" } });
    expect(s.turn).toMatchObject({ active: true, startedAt: "t1" });
    expect(s.rows.a.current).toBe(false);
  });

  it("a failed send ends the turn it opened; one with output after it doesn't", () => {
    let s = run(emptyConversation(), hello(), f("replay_end"), f("user_message", { key: "user:1", text: "go" }));
    expect(s.turn.active).toBe(true);
    s = run(s, f("unsaved", { keys: ["user:1"] }));
    expect(s.rows["user:1"].failed).toBe(true);
    expect(s.turn.active).toBe(false);

    let t = run(emptyConversation(), hello(), f("replay_end"), f("user_message", { key: "user:1", text: "go" }), ...textItem("a", "working"));
    t = run(t, f("unsaved", { keys: ["user:1"] }));
    expect(t.turn.active).toBe(true);
  });

  it("a stored tool row keeps the live display kind and title the store doesn't have", () => {
    let s = run(emptyConversation(), hello(), f("replay_end"));
    s = run(
      s,
      f("item_start", { key: "call_1", item: "tool_use" }),
      f("tool_use", { key: "call_1", tool: { id: "call_1", name: "shell", kind: "execute", title: "shell · ls" } }),
      f("tool_result", { key: "call_1", text: "a.txt", is_error: false }),
    );
    s = run(
      s,
      page([
        { role: "tool", key: "call_1", tool: { id: "call_1", name: "shell", status: "completed" } },
        { role: "tool_result", key: "call_1", text: "a.txt", tool: { id: "call_1", name: "shell" } },
      ]),
    );
    expect(s.rows.call_1.tool).toMatchObject({ name: "shell", kind: "execute", title: "shell · ls", status: "completed" });
    expect(s.rows.call_1.tool?.result?.text).toBe("a.txt");
    expect(s.rows.call_1.live).toBeFalsy();
  });

  it("a tool call watched live keeps its measured duration through the stored merge", () => {
    let s = run(emptyConversation(), hello(), f("replay_end"));
    s = run(s, f("item_start", { key: "c", item: "tool_use" }), f("tool_use", { key: "c", tool: { id: "c", name: "shell" } }), f("tool_result", { key: "c", text: "x" }));
    s = run(s, f("settled", { keys: ["c"] })); // settled before the turn-end page lands
    const started = s.rows.c.timestamp;
    const ended = s.rows.c.tool?.result?.timestamp;
    s = run(
      s,
      page([
        { role: "tool", key: "c", tool: { id: "c", name: "shell" }, timestamp: "2026-10-21T00:00:00Z" },
        { role: "tool_result", key: "c", text: "x", timestamp: "2026-10-21T00:00:00Z" },
      ]),
    );
    expect(s.rows.c.timestamp).toBe(started);
    expect(s.rows.c.tool?.result?.timestamp).toBe(ended);
  });
});
