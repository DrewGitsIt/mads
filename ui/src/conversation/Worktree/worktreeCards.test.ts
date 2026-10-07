import { describe, expect, it } from "vitest";
import type { Envelope } from "../../router/types";
import { foldWorktreeEvents, landCardFromResult, withEchoes } from "./worktreeCards";

let seq = 1;
const env = (type: string, payload: unknown, s = seq++): Envelope => ({
  seq: s,
  type,
  timestamp: `2026-10-05T10:00:${String(s).padStart(2, "0")}Z`,
  actor: "system",
  session_id: "ses",
  payload,
});
const land = { kind: "land", branch: "mads/session/ses", base: "main", target_branch: "main" };

describe("foldWorktreeEvents", () => {
  it("draws a card only for the Commit button's commits, not the agent's own", () => {
    const cards = foldWorktreeEvents([
      env("commit.created", { hash: "agent1", orphan: true }),
      env("commit.created", { hash: "mine1", source: "commit", message: "Fix it", files: 2, drafted: true }),
      env("commit.created", { hash: "merge1", source: "land", message: "Land x" }),
    ]);
    expect(cards).toHaveLength(1);
    expect(cards[0]).toMatchObject({ kind: "committed", hash: "mine1", subject: "Fix it", files: 2, drafted: true });
  });

  it("marks a commit no longer the tip once anything lands or commits on top", () => {
    const [c] = foldWorktreeEvents([
      env("commit.created", { hash: "mine", source: "commit", message: "m", drafted: true }),
      env("commit.created", { hash: "agent" }),
    ]);
    expect(c).toMatchObject({ kind: "committed", tip: false });
  });

  it("marks an undone commit", () => {
    const [c] = foldWorktreeEvents([
      env("commit.created", { hash: "mine", source: "commit", message: "m", drafted: true }),
      env("commit.undone", { hash: "mine" }),
    ]);
    expect(c).toMatchObject({ undone: true, tip: false });
  });

  it("reads only Land's merge events, ignoring the pipeline's land queue", () => {
    const cards = foldWorktreeEvents([
      env("merge.conflicted", { branch: "mads/tsk_x", files: ["a"], fork_sha: "f" }),
      env("merge.completed", { ...land, merge_commit: "7be2d31", fast_forwarded: true }),
      env("merge.conflicted", { ...land, files: ["a.go"], step: "base" }),
      env("merge.refused", { ...land, reason: "busy" }),
    ]);
    expect(cards.map((c) => c.kind)).toEqual(["landed", "conflicted", "refused"]);
    expect(cards[0]).toMatchObject({ base: "main", mergeCommit: "7be2d31", fastForwarded: true });
    // Only the newest Land outcome offers to go again.
    expect(cards[1]).toMatchObject({ latest: false });
    expect(cards[2]).toMatchObject({ latest: true, reason: "busy" });
  });

  it("collapses back-to-back identical refusals into the newest, counted", () => {
    const dirty = { ...land, reason: "dirty", files: ["a.go", "x.txt"], outside: ["x.txt"] };
    const cards = foldWorktreeEvents([
      env("merge.refused", dirty, 10),
      env("merge.refused", { ...dirty, files: ["x.txt", "a.go"] }, 11),
      env("merge.refused", dirty, 12),
    ]);
    expect(cards).toHaveLength(1);
    expect(cards[0]).toMatchObject({ kind: "refused", id: "wt-10", seq: 12, repeats: 3, latest: true, outside: ["x.txt"] });
    expect(cards[0].timestamp).toBe("2026-10-05T10:00:12Z");
  });

  it("keeps refusals apart when anything differs or something happened in between", () => {
    const cards = foldWorktreeEvents([
      env("merge.refused", { ...land, reason: "dirty", files: ["a.go"] }, 20),
      env("merge.refused", { ...land, reason: "dirty", files: ["a.go", "b.go"] }, 21),
      env("merge.refused", { ...land, reason: "busy" }, 22),
      env("commit.created", { hash: "c1", source: "commit", message: "m" }, 23),
      env("merge.refused", { ...land, reason: "busy" }, 24),
    ]);
    expect(cards.map((c) => c.kind)).toEqual(["refused", "refused", "refused", "committed", "refused"]);
    expect(cards.every((c) => !("repeats" in c) || c.repeats === undefined)).toBe(true);
  });

  it("counts a replayed and a live copy of one event once", () => {
    const e = env("merge.refused", { ...land, reason: "busy" });
    expect(foldWorktreeEvents([e, { ...e }])).toHaveLength(1);
  });

  it("keeps a first uneventful folder creation quiet, but says when one was recreated", () => {
    const cards = foldWorktreeEvents([
      env("worktree.ensured", { action: "created", dir: "/d", branch: "b", base: "main" }),
      env("worktree.ensured", { action: "reattached", dir: "/d", branch: "b", base: "main" }),
      env("worktree.ensured", { action: "created", dir: "/d", branch: "b", base: "main", base_missing: true }),
      env("worktree.ensured", { action: "not_isolated", reason: "detached_head" }),
      env("worktree.ensured", { action: "reused", dir: "/d", branch: "b", stale_moved_to: "/d.stale-1" }),
    ]);
    expect(cards.map((c) => (c.kind === "ensured" ? c.title : c.kind))).toEqual([
      "Recreated this session's folder",
      "Recreated this session's folder and branch",
      "Working in your checkout directly",
      "Tidied this session's folder",
    ]);
    expect(cards[1]).toMatchObject({ body: expect.stringContaining("main no longer exists") });
    expect(cards[2]).toMatchObject({ body: expect.stringContaining("detached HEAD") });
    expect(cards[3]).toMatchObject({ body: expect.stringContaining("/d.stale-1") });
  });
});

describe("withEchoes", () => {
  it("shows a Land answer until its event arrives, then only the event's card", () => {
    const echo = { afterSeq: 5, card: landCardFromResult({ outcome: "refused", reason: "busy", base: "main" }, "t") };
    const before = foldWorktreeEvents([env("merge.refused", { ...land, reason: "pipeline" }, 4)]);
    expect(withEchoes(before, { commits: [], land: echo }).map((c) => c.kind)).toEqual(["refused", "refused"]);
    const after = foldWorktreeEvents([
      env("merge.refused", { ...land, reason: "pipeline" }, 4),
      env("merge.refused", { ...land, reason: "busy" }, 6),
    ]);
    expect(withEchoes(after, { commits: [], land: echo })).toHaveLength(2);
  });

  it("folds an echo identical to the last logged refusal into it, so a repeat click doesn't stack a copy", () => {
    const logged = foldWorktreeEvents([env("merge.refused", { ...land, reason: "dirty", files: ["x.txt"] }, 30)]);
    const card = landCardFromResult({ outcome: "refused", reason: "dirty", base: "main", files: ["x.txt"] }, "t");
    const shown = withEchoes(logged, { commits: [], land: { afterSeq: 30, card } });
    expect(shown).toHaveLength(1);
    expect(shown[0]).toMatchObject({ kind: "refused", id: "wt-30", repeats: 2, latest: true });
    // The memoized fold is left alone.
    expect(logged[0]).toMatchObject({ seq: 30 });
    expect("repeats" in logged[0] ? logged[0].repeats : undefined).toBeUndefined();
  });

  it("drops a commit echo once its hash is logged", () => {
    const echo = {
      kind: "committed" as const,
      id: "e",
      seq: Infinity,
      hash: "abc",
      subject: "s",
      drafted: true,
      undone: false,
      tip: true,
    };
    expect(withEchoes([], { commits: [echo] })).toHaveLength(1);
    const logged = foldWorktreeEvents([env("commit.created", { hash: "abc", source: "commit", message: "s" })]);
    expect(withEchoes(logged, { commits: [echo] })).toHaveLength(1);
  });
});

describe("foldWorktreeEvents — a pipeline merging into the session (D8)", () => {
  const task = { branch: "mads/tsk_x", target_branch: "mads/session/ses", fork_sha: "f", trunk_head: "t" };

  it("stays quiet for a task merge that didn't touch the agent's work", () => {
    expect(foldWorktreeEvents([env("merge.completed", { ...task, task_title: "Add docs" })])).toEqual([]);
  });

  it("says when it saved the agent's edits, and when it merged mid-turn", () => {
    const cards = foldWorktreeEvents([
      env("commit.created", { hash: "save1", source: "pipeline_save", message: "Save session edits before pipeline merge" }),
      env("merge.completed", {
        ...task,
        task_title: "Add docs",
        auto_committed: ["notes.txt"],
        auto_commits: ["save1"],
        waited_out: true,
        files: ["notes.txt", "docs/a.md"],
      }),
    ]);
    expect(cards).toHaveLength(2);
    expect(cards[0]).toMatchObject({ kind: "pipelineSaved", task: "Add docs", files: ["notes.txt"] });
    expect(cards[1]).toMatchObject({ kind: "pipelineMerged", task: "Add docs", files: ["notes.txt", "docs/a.md"] });
    expect(new Set(cards.map((c) => c.id)).size).toBe(2);
  });
});

describe("auto-merged Land (merge.auto_resolved)", () => {
  const auto = { ...land, files: ["a.go", "b.ts"], merge_commit: "9f8e7d6c5b" };

  it("draws a card naming what mergiraf merged, offering its actions while it's the latest outcome", () => {
    const cards = foldWorktreeEvents([
      env("commit.created", { hash: "mine", source: "commit", message: "m", drafted: true }),
      env("merge.auto_resolved", auto),
    ]);
    expect(cards[0]).toMatchObject({ kind: "committed", tip: false });
    expect(cards[1]).toMatchObject({
      kind: "autoResolved",
      base: "main",
      files: ["a.go", "b.ts"],
      mergeCommit: "9f8e7d6c5b",
      latest: true,
    });
  });

  it("stops being the latest once the next Land lands", () => {
    const cards = foldWorktreeEvents([
      env("merge.auto_resolved", auto),
      env("merge.completed", { ...land, merge_commit: "abc", fast_forwarded: true }),
    ]);
    expect(cards.map((c) => c.kind)).toEqual(["autoResolved", "landed"]);
    expect(cards[0]).toMatchObject({ latest: false });
  });

  it("ignores the pipeline's task merges (no kind land)", () => {
    expect(foldWorktreeEvents([env("merge.auto_resolved", { files: ["a.go"] })])).toEqual([]);
  });

  it("echoes a Land answer of auto_resolved until its event arrives", () => {
    const card = landCardFromResult(
      { outcome: "auto_resolved", base: "main", files: ["a.go"], merge_commit: "9f8e7d6c5b" },
      "2026-10-05T10:01:00Z",
    );
    expect(card).toMatchObject({ kind: "autoResolved", files: ["a.go"], mergeCommit: "9f8e7d6c5b", latest: true });
    expect(withEchoes([], { land: { afterSeq: 0, card }, commits: [] })).toHaveLength(1);
    const logged = foldWorktreeEvents([env("merge.auto_resolved", auto, 500)]);
    expect(withEchoes(logged, { land: { afterSeq: 0, card }, commits: [] })).toHaveLength(1);
  });
});

describe("foldWorktreeEvents — session.trust_changed", () => {
  it("draws each trust change where it was logged, raw levels kept for the renderer", () => {
    const cards = foldWorktreeEvents([
      env("commit.created", { hash: "mine", source: "commit", message: "m", drafted: true }, 700),
      env("session.trust_changed", { from: "limited", to: "full", restarted: true }, 701),
      env("session.trust_changed", { from: "full", to: "auto_review", restarted: false, restart_error: "boom" }, 702),
    ]);
    expect(cards.map((c) => c.kind)).toEqual(["committed", "trustChanged", "trustChanged"]);
    expect(cards[1]).toMatchObject({ id: "wt-701", from: "limited", to: "full", restarted: true, restartError: undefined });
    expect(cards[2]).toMatchObject({ from: "full", to: "auto_review", restarted: false, restartError: "boom" });
    // Not a commit or a land: the commit is still the tip Undo applies to.
    expect(cards[0]).toMatchObject({ kind: "committed", tip: true });
  });
});
