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
    expect(cards[0]).toMatchObject({ base: "main", mergeCommit: "7be2d31" });
    // Only the newest Land outcome offers to go again.
    expect(cards[1]).toMatchObject({ latest: false });
    expect(cards[2]).toMatchObject({ latest: true, reason: "busy" });
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
