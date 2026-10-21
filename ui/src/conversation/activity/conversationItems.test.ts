import { describe, expect, it } from "vitest";
import type { Delegate, Envelope, TranscriptMessage } from "../../router/types";
import {
  mergeDelegates,
  appendSubagentItem,
  applyTaskEvent,
  buildItems,
  finishRunningTasks,
  foldHistoryEvents,
  replayedCompactStatus,
  rowsSubStream,
  rowsToEntries,
  resumeRunningTasks,
  transcriptSubStream,
  type SourceEntry,
  type TaskModel,
} from "./conversationItems";

let n = 0;
const msg = (text: string): SourceEntry => ({ type: "message", message: { role: "agent", text }, key: `m${n++}` });
const tool = (name: string, id?: string, input?: unknown): SourceEntry => ({
  type: "tool",
  tool: { id, name, input },
});

describe("buildItems — grouping", () => {
  it("always groups consecutive tool events, even a single call", () => {
    const items = buildItems([tool("Read", "t1")]);
    expect(items).toHaveLength(1);
    expect(items[0].type).toBe("activity");
  });

  it("splits groups on prose, keeps runs together", () => {
    const items = buildItems([
      msg("investigating"),
      tool("Grep", "t1"),
      tool("Read", "t2"),
      msg("found it"),
      tool("Bash", "t3"),
    ]);
    expect(items.map((i) => i.type)).toEqual(["message", "activity", "message", "activity"]);
    expect(items[1].type === "activity" && items[1].entries).toHaveLength(2);
  });

  it("folds thinking entries into the surrounding group", () => {
    const items = buildItems([
      tool("Read", "t1"),
      { type: "thinking", text: "the ledger never distinguishes" },
      tool("Bash", "t2"),
    ]);
    expect(items).toHaveLength(1);
    expect(items[0].type === "activity" && items[0].entries.map((e) => e.type)).toEqual([
      "tool",
      "thinking",
      "tool",
    ]);
  });

  it("keys a group by its first tool id, stable across a live→history re-render", () => {
    const live = buildItems([tool("Read", "toolu_9")]);
    const historical = buildItems([msg("hi"), tool("Read", "toolu_9")]);
    expect(live[0].key).toBe(historical[1].key);
  });

  it("keys every item by its row: a message's, a group's first row's, the plan's and a task's call's", () => {
    const entries = rowsToEntries([
      { key: "u1", role: "user", text: "go" },
      { key: "th1", role: "thinking", text: "hmm" },
      { key: "k:t1", role: "tool", tool: { id: "t1", name: "Read" } },
      { key: "k:p1", role: "tool", tool: { id: "p1", name: "TodoWrite", input: { todos: [] } } },
      { key: "k:a1", role: "tool", tool: { id: "a1", name: "Agent", kind: "agent" } },
      { key: "a1", role: "agent", text: "done" },
    ]);
    const items = buildItems(entries);
    expect(items.map((i) => `${i.type}:${i.key}`)).toEqual([
      "message:u1",
      "activity:th1",
      "plan:k:p1",
      "task:k:a1",
      "message:a1",
    ]);
    // Inserting a row ahead re-keys nothing after it.
    const more = buildItems(rowsToEntries([{ key: "u0", role: "user", text: "earlier" }])).concat(items);
    expect(more.slice(1).map((i) => i.key)).toEqual(items.map((i) => i.key));
  });
});

describe("rowsToEntries", () => {
  it("marks only the current turn's rows live, and carries row keys onto activity entries", () => {
    const entries = rowsToEntries([
      { key: "t1", role: "tool", tool: { id: "t1", name: "Read" }, live: true },
      { key: "t2", role: "tool", tool: { id: "t2", name: "Read" }, current: true },
      { key: "th", role: "thinking", text: "x", current: true },
    ]);
    expect(entries.map((e) => (e.type === "card" ? undefined : e.live))).toEqual([undefined, true, true]);
    expect(entries.map((e) => (e.type === "card" ? undefined : e.key))).toEqual(["t1", "t2", "th"]);
  });
});

describe("rowsSubStream", () => {
  it("folds a subagent's keyed rows into the dock's stream shape", () => {
    expect(
      rowsSubStream([
        { key: "a", role: "agent", text: "looking", parent: "tu1" },
        { key: "t", role: "tool", tool: { id: "s1", name: "Read", result: { text: "ok" } }, parent: "tu1" },
        { key: "h", role: "thinking", text: "", parent: "tu1" },
      ]).map((i) => i.type),
    ).toEqual(["text", "tool"]);
  });
});

describe("buildItems — plan widget (T14)", () => {
  const todos1 = { todos: [{ content: "a", status: "in_progress" }, { content: "b", status: "pending" }] };
  const todos2 = { todos: [{ content: "a", status: "completed" }, { content: "b", status: "in_progress" }] };

  it("renders TodoWrite as a plan item, not an activity row", () => {
    const items = buildItems([tool("TodoWrite", "t1", todos1)]);
    expect(items).toHaveLength(1);
    expect(items[0].type).toBe("plan");
  });

  it("successive TodoWrite calls update the same widget in place", () => {
    const items = buildItems([
      tool("TodoWrite", "t1", todos1),
      tool("Read", "t2"),
      tool("TodoWrite", "t3", todos2),
    ]);
    const plans = items.filter((i) => i.type === "plan");
    expect(plans).toHaveLength(1);
    expect(plans[0].type === "plan" && plans[0].todos[0].status).toBe("completed");
    // The two TodoWrite calls don't break the Read's group either side.
    expect(items.map((i) => i.type)).toEqual(["plan", "activity"]);
  });
});

describe("buildItems — task cards (T15)", () => {
  it("renders an agent-kind tool_use as a task item, outside any group", () => {
    const items = buildItems([
      tool("Read", "t1"),
      { type: "tool", tool: { id: "t2", name: "Task", kind: "agent", input: { description: "verify" } } },
      tool("Bash", "t3"),
    ]);
    expect(items.map((i) => i.type)).toEqual(["activity", "task", "activity"]);
  });
});

describe("task lifecycle folding", () => {
  it("folds started→progress→notification into a completed model", () => {
    let tasks = new Map<string, TaskModel>();
    tasks = applyTaskEvent(
      tasks,
      { phase: "started", task_id: "tk1", tool_use_id: "tu1", description: "verify", subagent_type: "general-purpose" },
      true,
    );
    expect(tasks.get("tk1")?.running).toBe(true);
    tasks = applyTaskEvent(
      tasks,
      { phase: "progress", task_id: "tk1", total_tokens: 9800, tool_uses: 4, duration_ms: 27000, last_tool_name: "Bash" },
      true,
    );
    expect(tasks.get("tk1")?.totalTokens).toBe(9800);
    tasks = applyTaskEvent(
      tasks,
      { phase: "notification", task_id: "tk1", status: "completed", summary: "all good" },
      true,
    );
    const t = tasks.get("tk1")!;
    expect(t.running).toBe(false);
    expect(t.completed).toBe(true);
    expect(t.summary).toBe("all good");
  });

  it("a progress line's description is the current step, never the task's name", () => {
    let tasks = new Map<string, TaskModel>();
    tasks = applyTaskEvent(
      tasks,
      { phase: "started", task_id: "tk1", tool_use_id: "tu1", description: "Empirical color harmony science" },
      true,
    );
    tasks = applyTaskEvent(
      tasks,
      { phase: "progress", task_id: "tk1", description: "Searching for Ou Luo 2006 colour harmony" },
      true,
    );
    const t = tasks.get("tk1")!;
    expect(t.description).toBe("Empirical color harmony science");
    expect(t.currentStep).toBe("Searching for Ou Luo 2006 colour harmony");
  });

  it("a replayed lifecycle never claims running; no notification = interrupted-shape", () => {
    let tasks = new Map<string, TaskModel>();
    tasks = applyTaskEvent(tasks, { phase: "started", task_id: "tk1", tool_use_id: "tu1" }, false);
    tasks = applyTaskEvent(tasks, { phase: "progress", task_id: "tk1", total_tokens: 100 }, false);
    const t = tasks.get("tk1")!;
    expect(t.running).toBe(false);
    expect(t.completed).toBe(false);
  });

  it("finishRunningTasks marks still-running tasks interrupted at turn end", () => {
    let tasks = new Map<string, TaskModel>();
    tasks = applyTaskEvent(tasks, { phase: "started", task_id: "tk1" }, true);
    tasks = finishRunningTasks(tasks);
    expect(tasks.get("tk1")?.running).toBe(false);
    expect(tasks.get("tk1")?.status).toBe("interrupted");
  });
});

describe("subagent stream folding", () => {
  it("appends text/tool/thinking and folds tool_results by id", () => {
    let streams = new Map();
    streams = appendSubagentItem(streams, "tu1", { kind: "text", text: "spawning" });
    streams = appendSubagentItem(streams, "tu1", {
      kind: "tool_use",
      tool: { id: "s1", name: "Bash", input: { command: "go test" } },
    });
    streams = appendSubagentItem(streams, "tu1", {
      kind: "tool_result",
      tool: { id: "s1", name: "" },
      text: "ok",
    });
    const items = streams.get("tu1")!;
    expect(items).toHaveLength(2);
    expect(items[1].type === "tool" && items[1].tool.result?.text).toBe("ok");
  });

  it("stamps the tool entry's start and the folded result's end so durations can render", () => {
    let streams = new Map();
    streams = appendSubagentItem(streams, "tu1", {
      kind: "tool_use",
      tool: { id: "s1", name: "Bash", input: { command: "go test" } },
      timestamp: "2026-09-09T10:00:00Z",
    });
    streams = appendSubagentItem(streams, "tu1", {
      kind: "tool_result",
      tool: { id: "s1", name: "" },
      text: "ok",
      timestamp: "2026-09-09T10:00:03Z",
    });
    const items = streams.get("tu1")!;
    expect(items[0].type === "tool" && items[0].timestamp).toBe("2026-09-09T10:00:00Z");
    expect(items[0].type === "tool" && items[0].tool.result?.timestamp).toBe("2026-09-09T10:00:03Z");
  });
});

describe("foldHistoryEvents (T17)", () => {
  const env = (seq: number, type: string, payload: unknown): Envelope => ({
    seq,
    timestamp: `2026-09-09T10:00:0${seq}Z`,
    type,
    session_id: "s1",
    actor: "system",
    payload,
  });

  it("folds statuses, task lifecycles and subagent streams from the event log; never thinking", () => {
    const h = foldHistoryEvents([
      env(3, "session.thinking", { text: "pondering" }),
      env(1, "session.tool_status_changed", { tool_use_id: "t1", status: "interrupted" }),
      env(2, "session.subagent_task", { phase: "started", task_id: "tk1", tool_use_id: "tu1", description: "audit" }),
      env(4, "session.subagent_event", {
        parent_tool_use_id: "tu1",
        kind: "tool_use",
        tool: { id: "s1", name: "Read", path: "a.go" },
      }),
    ]);
    expect(h.statuses.get("t1")).toBe("interrupted");
    expect(h.tasks.get("tk1")?.description).toBe("audit");
    expect(h.tasks.get("tk1")?.running).toBe(false);
    expect(h.subagentStreams.get("tu1")).toHaveLength(1);
    expect(h).not.toHaveProperty("thinking");
  });
});

describe("resumeRunningTasks", () => {
  it("revives only un-notified tasks that started inside the in-flight turn", () => {
    let tasks = new Map<string, TaskModel>();
    tasks = applyTaskEvent(tasks, { phase: "started", task_id: "old" }, false, "2026-09-28T10:00:00Z");
    tasks = applyTaskEvent(tasks, { phase: "started", task_id: "now" }, false, "2026-09-28T10:05:00Z");
    tasks = applyTaskEvent(tasks, { phase: "started", task_id: "done" }, false, "2026-09-28T10:05:01Z");
    tasks = applyTaskEvent(tasks, { phase: "notification", task_id: "done", status: "completed" }, false);

    const resumed = resumeRunningTasks(tasks, "2026-09-28T10:04:00Z");
    expect(resumed.get("old")?.running).toBe(false);
    expect(resumed.get("now")?.running).toBe(true);
    expect(resumed.get("done")?.running).toBe(false);
    // The start stamp is the first event's, not a later phase's.
    expect(resumed.get("done")?.startedAt).toBe("2026-09-28T10:05:01Z");
  });

  it("leaves everything alone on an unparseable turn start", () => {
    const tasks = applyTaskEvent(new Map(), { phase: "started", task_id: "t" }, false, "2026-09-28T10:05:00Z");
    expect(resumeRunningTasks(tasks, "").get("t")?.running).toBe(false);
  });
});

describe("replayedCompactStatus", () => {
  const at = "2026-09-28T10:00:00Z";

  it("shows a running pass only while the session is active", () => {
    const latest = { status: { phase: "start" as const }, timestamp: at };
    expect(replayedCompactStatus(latest, true)).toEqual({ phase: "start" });
    expect(replayedCompactStatus(latest, false)).toBeUndefined();
  });

  it("shows a failure until it is dismissed, and never a success", () => {
    const failed = { status: { phase: "done" as const, error: "nope" }, timestamp: at };
    expect(replayedCompactStatus(failed, false)).toEqual({ phase: "done", error: "nope" });
    expect(replayedCompactStatus(failed, true, "2026-09-28T10:01:00Z")).toBeUndefined();
    expect(replayedCompactStatus(failed, true, "2026-09-28T09:59:00Z")).toEqual({ phase: "done", error: "nope" });
    expect(
      replayedCompactStatus({ status: { phase: "done", success: true }, timestamp: at }, true),
    ).toBeUndefined();
  });

  it("folds the latest session.compact entry out of the log", () => {
    const env = (seq: number, payload: unknown): Envelope => ({
      seq,
      timestamp: `2026-09-28T10:00:0${seq}Z`,
      type: "session.compact",
      session_id: "s",
      actor: "system",
      payload,
    });
    const h = foldHistoryEvents([env(2, { phase: "done", success: true }), env(1, { phase: "start" })]);
    expect(h.compact).toEqual({ status: { phase: "done", success: true }, timestamp: "2026-09-28T10:00:02Z" });
  });
});

describe("delegate records over the task-line fold", () => {
  const rec = (extra: Partial<Delegate>): Delegate => ({
    id: "dlg_1",
    session_id: "s1",
    harness_id: "a1",
    tool_use_id: "toolu_spawn",
    state: "running",
    runs: 1,
    started_at: "2026-10-08T10:00:00Z",
    created_at: "2026-10-08T10:00:00Z",
    updated_at: "2026-10-08T10:00:00Z",
    ...extra,
  });

  it("keeps the first anchor when a resumed delegate reports the resuming call's id", () => {
    let tasks = applyTaskEvent(new Map(), { phase: "started", task_id: "a1", tool_use_id: "toolu_spawn" }, true);
    tasks = applyTaskEvent(tasks, { phase: "notification", task_id: "a1", tool_use_id: "toolu_spawn", status: "completed" }, true);
    tasks = applyTaskEvent(tasks, { phase: "started", task_id: "a1", tool_use_id: "toolu_sendmessage" }, true);
    expect(tasks.get("a1")?.toolUseId).toBe("toolu_spawn");
  });

  it("lets the record decide running and how it ended, over turn-edge guesses", () => {
    // The fold concluded "interrupted" at a turn end; the record knows better.
    const folded = finishRunningTasks(
      applyTaskEvent(new Map(), { phase: "started", task_id: "a1", tool_use_id: "toolu_spawn", description: "audit" }, true),
    );
    expect(folded.get("a1")?.running).toBe(false);

    const live = mergeDelegates(folded, new Map([["a1", rec({ state: "running" })]])).get("a1")!;
    expect(live).toMatchObject({ running: true, completed: false, state: "running", description: "audit", startedAt: "2026-10-08T10:00:00Z" });

    const failed = mergeDelegates(folded, new Map([["a1", rec({ state: "failed", reason: "failed" })]])).get("a1")!;
    expect(failed).toMatchObject({ running: false, completed: false, state: "failed", status: "failed" });
  });

  it("totals a resumed delegate's runs: its record's prior runs plus the task lines' live one, the record's once it ends", () => {
    // Claude Code counts each run's tools and time from zero; tokens are its latest figure.
    let tasks = applyTaskEvent(new Map(), { phase: "started", task_id: "a1", tool_use_id: "toolu_spawn" }, true);
    tasks = applyTaskEvent(tasks, { phase: "notification", task_id: "a1", status: "completed", total_tokens: 456_816, tool_uses: 77, duration_ms: 1_745_070 }, true);
    tasks = applyTaskEvent(tasks, { phase: "started", task_id: "a1", tool_use_id: "toolu_relay" }, true);
    expect(tasks.get("a1")).toMatchObject({ toolUses: undefined, durationMs: undefined, totalTokens: 456_816 });
    tasks = applyTaskEvent(tasks, { phase: "progress", task_id: "a1", total_tokens: 456_386, tool_uses: 61, duration_ms: 800_000 }, true);

    const resumed = rec({ state: "running", runs: 3, tool_uses: 285, duration_ms: 5_161_773, prior_tool_uses: 285, prior_duration_ms: 5_161_773, total_tokens: 456_816 });
    expect(mergeDelegates(tasks, new Map([["a1", resumed]])).get("a1")).toMatchObject({
      running: true, toolUses: 346, durationMs: 800_000, priorDurationMs: 5_161_773, totalTokens: 456_386,
    });

    const ended = rec({ ...resumed, state: "completed", tool_uses: 350, duration_ms: 6_000_000, total_tokens: 456_400 });
    const done = mergeDelegates(tasks, new Map([["a1", ended]])).get("a1")!;
    expect(done).toMatchObject({ running: false, toolUses: 350, durationMs: 6_000_000, totalTokens: 456_400 });
    expect(done.priorDurationMs).toBeUndefined();
  });

  it("materializes a delegate the fold never saw, anchored and named from its record", () => {
    const got = mergeDelegates(new Map(), new Map([["a1", rec({ description: "audit", type: "Explore", state: "completed", duration_ms: 1900 })]]));
    expect(got.get("a1")).toMatchObject({
      taskId: "a1", toolUseId: "toolu_spawn", description: "audit", subagentType: "Explore", completed: true, durationMs: 1900,
    });
  });
});

describe("transcriptSubStream", () => {
  it("folds a subagent's own file into the dock's stream, with what only the file has", () => {
    const messages: TranscriptMessage[] = [
      { role: "user", text: "Map the transcript code", timestamp: "2026-10-09T10:00:00Z" },
      { role: "tool", tool: { id: "t1", name: "Grep", status: "completed" }, timestamp: "2026-10-09T10:00:01Z" },
      { role: "tool_result", text: "transcript.go", tool: { id: "t1", name: "" }, timestamp: "2026-10-09T10:00:02Z" },
      { role: "agent", text: "Here's the map.", timestamp: "2026-10-09T10:00:03Z" },
      { role: "agent", text: "Second paragraph." },
      { role: "user", text: "Also check imports", timestamp: "2026-10-09T10:05:00Z" },
    ];
    const items = transcriptSubStream(messages);
    expect(items.map((i) => i.type)).toEqual(["message", "tool", "text", "text", "message"]);
    expect(items[0]).toMatchObject({ type: "message", text: "Map the transcript code" });
    // The result folds into its call, stamped with the harness's own times.
    expect(items[1]).toMatchObject({
      type: "tool",
      timestamp: "2026-10-09T10:00:01Z",
      tool: { id: "t1", result: { text: "transcript.go", timestamp: "2026-10-09T10:00:02Z" } },
    });
    // Whole messages stay separate paragraphs, unlike live deltas.
    expect(items[2]).toEqual({ type: "text", text: "Here's the map.", timestamp: "2026-10-09T10:00:03Z" });
  });
});

describe("appendSubagentItem — text times", () => {
  it("stamps a paragraph with its first delta's time, which later deltas keep", () => {
    let streams = appendSubagentItem(new Map(), "tu1", { kind: "text", text: "Found ", timestamp: "2026-10-09T10:00:00Z" });
    streams = appendSubagentItem(streams, "tu1", { kind: "text", text: "three.", timestamp: "2026-10-09T10:00:04Z" });
    expect(streams.get("tu1")).toEqual([{ type: "text", text: "Found three.", timestamp: "2026-10-09T10:00:00Z" }]);
  });
});
