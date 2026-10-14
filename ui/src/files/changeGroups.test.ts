import { describe, expect, it } from "vitest";
import { buildCommitGroups, buildTierGroups, buildTurnGroups } from "./changeGroups";
import { file } from "./testChangeFile";

describe("buildTierGroups", () => {
  it("makes one section per non-empty tier, in tier order, with Generated and Scratch collapsed", () => {
    const groups = buildTierGroups([
      file("tmp.txt", { tier: "scratch" }),
      file("a.go"),
      file("pkg.lock", { tier: "generated" }),
      file("a_test.go", { tier: "tests" }),
    ]);
    expect(groups.map((g) => [g.title, g.defaultOpen])).toEqual([
      ["Source", true],
      ["Tests & fixtures", true],
      ["Generated", false],
      ["Scratch", false],
    ]);
    expect(groups[0].desc).toMatch(/read these first/);
    expect(groups[1].desc).toBeUndefined();
  });
});

describe("buildTurnGroups", () => {
  const turns = [1, 2, 3].map((n) => ({ n, prompt: n === 2 ? "" : `p${n}`, at: "", files: 0 }));

  it("lists every turn, a file under each turn that touched it, tier-then-size inside", () => {
    const groups = buildTurnGroups(
      [
        file("x.snap", { tier: "generated", added: 999, turns: [1] }),
        file("a.go", { added: 1, turns: [1, 3] }),
        file("b.go", { added: 10, turns: [1] }),
      ],
      turns,
    );
    expect(groups.map((g) => g.title)).toEqual(["Turn 1", "Turn 2", "Turn 3"]);
    expect(groups[0].entries.map((e) => e.file.display)).toEqual(["b.go", "a.go", "x.snap"]);
    expect(groups[1].entries).toEqual([]);
    expect(groups[1].prompt).toBe("");
    expect(groups[2].entries.map((e) => e.key)).toEqual(["u3:/w/a.go"]);
    expect(groups[2].entries[0].turn).toBe(3);
  });
});

describe("buildCommitGroups", () => {
  const commits = [
    { hash: "c3", message: "newest", timestamp: "2026-10-14T03:00:00Z", paths: [] },
    { hash: "c2", message: "empty now", timestamp: "2026-10-14T02:00:00Z", paths: [] },
    { hash: "c1", message: "oldest", timestamp: "2026-10-14T01:00:00Z", paths: [] },
  ];

  it("puts Uncommitted first, then commits in server order, a file under each commit holding it", () => {
    const groups = buildCommitGroups(
      [
        file("a.go", { commits: ["c3", "c1"], uncommitted: false }),
        file("b.go", { commits: ["c1"], uncommitted: true }),
        file("new.go", { uncommitted: true }),
      ],
      commits,
    );
    expect(groups.map((g) => g.title)).toEqual(["Uncommitted", "newest", "oldest"]);
    expect(groups[0].entries.map((e) => e.file.display)).toEqual(["b.go", "new.go"]);
    expect(groups[0].entries[0].commit).toBeUndefined();
    expect(groups[2].entries.map((e) => [e.file.display, e.commit])).toEqual([
      ["a.go", "c1"],
      ["b.go", "c1"],
    ]);
  });

  it("omits Uncommitted when everything is committed", () => {
    const groups = buildCommitGroups([file("a.go", { commits: ["c1"], uncommitted: false })], commits);
    expect(groups.map((g) => g.kind)).toEqual(["commit"]);
  });
});
