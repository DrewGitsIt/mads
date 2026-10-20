import { describe, expect, it } from "vitest";
import type { ChangeSet } from "../router/types";
import { file } from "./testChangeFile";
import {
  basename,
  clipPrompt,
  countNonScratch,
  dirname,
  filterFiles,
  tokenMatch,
  nextTurnSelection,
  railFiles,
  railStops,
  resolveRailTurn,
  sizeBarWidth,
  sortBySize,
  sortTierThenSize,
  stepRailStop,
  stepTurn,
  stepTurnSelection,
} from "./changesModel";

describe("sortTierThenSize", () => {
  it("orders by tier first, then size descending, then path", () => {
    const files = [
      file("gen.lock", { tier: "generated", added: 900 }),
      file("small.go", { added: 1 }),
      file("a_test.go", { tier: "tests", added: 500 }),
      file("big.go", { added: 300, deleted: 10 }),
      file("b.go", { added: 1 }),
      file("tmp.txt", { tier: "scratch", added: 2000 }),
      file("Makefile", { tier: "config", added: 3 }),
    ];
    expect(sortTierThenSize(files).map((f) => f.display)).toEqual([
      "big.go",
      "b.go",
      "small.go",
      "a_test.go",
      "Makefile",
      "gen.lock",
      "tmp.txt",
    ]);
  });

  it("sortBySize ignores tier", () => {
    const files = [file("a.go", { added: 1 }), file("x.snap", { tier: "generated", added: 50 })];
    expect(sortBySize(files).map((f) => f.display)).toEqual(["x.snap", "a.go"]);
  });
});

describe("path helpers", () => {
  it("splits basename and dirname", () => {
    expect(basename("internal/auth/token.go")).toBe("token.go");
    expect(dirname("internal/auth/token.go")).toBe("internal/auth");
    expect(basename("go.mod")).toBe("go.mod");
    expect(dirname("go.mod")).toBe("");
    expect(dirname("/tmp.log")).toBe("/");
  });
});

describe("clipPrompt", () => {
  it("keeps short prompts and flattens whitespace", () => {
    expect(clipPrompt("fix the\n  retry test")).toBe("fix the retry test");
  });

  it("cuts long prompts to the limit with an ellipsis", () => {
    const clipped = clipPrompt("x".repeat(300));
    expect(clipped).toHaveLength(120);
    expect(clipped.endsWith("…")).toBe(true);
  });
});

describe("sizeBarWidth", () => {
  it("clamps to 4–44px on a sqrt scale", () => {
    expect(sizeBarWidth(0, 0)).toBe(4);
    expect(sizeBarWidth(100, 0)).toBe(22);
    expect(sizeBarWidth(10_000, 0)).toBe(44);
  });
});

describe("filterFiles", () => {
  const files = [
    file("internal/auth/token.go", { turns: [2, 4] }),
    file("cmd/login.go", { turns: [6] }),
    file("README.md", { turns: [] }),
  ];

  it("filters by any selected turn", () => {
    expect(filterFiles(files, { turns: [4, 6] }).files.map((f) => f.display)).toEqual([
      "internal/auth/token.go",
      "cmd/login.go",
    ]);
  });

  it("filters by a case-insensitive path query", () => {
    expect(filterFiles(files, { query: "LOGIN" }).files.map((f) => f.display)).toEqual(["cmd/login.go"]);
  });

  it("passes everything with no turns and no query", () => {
    expect(filterFiles(files, { turns: null, query: "  " }).files).toHaveLength(3);
  });

  const ts = [
    file("ui/src/files/changesModel.ts"),
    file("ui/src/files/ChangesTab.tsx"),
    file("ui/src/files/ChangesList.tsx"),
    file("pkg/changes/build.go"),
    file("pkg/changes/builder.go"),
  ];
  const shown = (r: { files: { display: string }[] }) => r.files.map((f) => f.display);

  it("prefers whole-token matches and counts the partial ones it hides", () => {
    const r = filterFiles(ts, { query: ".ts" });
    expect(shown(r)).toEqual(["ui/src/files/changesModel.ts"]);
    expect(r.partial).toBe(2);
    expect(shown(filterFiles(ts, { query: "build" }))).toEqual(["pkg/changes/build.go"]);
  });

  it("shows the partial matches on request", () => {
    const r = filterFiles(ts, { query: ".ts", includePartial: true });
    expect(r.files).toHaveLength(3);
    expect(r.partial).toBe(0);
  });

  it("falls back to substring matching when nothing matches a whole token", () => {
    const r = filterFiles(ts, { query: "buil" });
    expect(shown(r)).toEqual(["pkg/changes/build.go", "pkg/changes/builder.go"]);
    expect(r.partial).toBe(0);
  });

  it("treats a camelCase hump as a word edge", () => {
    expect(shown(filterFiles(ts, { query: "tab" }))).toEqual(["ui/src/files/ChangesTab.tsx"]);
    // "changes" is a whole token in every path here (a folder or a camelCase prefix).
    expect(filterFiles(ts, { query: "changes" }).files).toHaveLength(5);
  });
});

describe("tokenMatch", () => {
  it("needs a word edge on each side the query has a word character", () => {
    expect(tokenMatch("a/foo.ts", ".ts")).toBe(true);
    expect(tokenMatch("a/foo.tsx", ".ts")).toBe(false);
    expect(tokenMatch("a/foo.tsx", "foo.")).toBe(true);
    expect(tokenMatch("a/xfoo.tsx", "foo.")).toBe(false);
    expect(tokenMatch("a/foo.ts.bak", ".ts")).toBe(true);
    expect(tokenMatch("a/HTTPServer.go", "server")).toBe(false);
  });

  it("checks every occurrence, not just the first", () => {
    expect(tokenMatch("tsx/foo.ts", ".ts")).toBe(true);
    expect(tokenMatch("tsconfig/ts", "ts")).toBe(true);
  });
});

describe("turn selection", () => {
  const order = [1, 2, 3, 4, 5];

  it("a click selects one turn; clicking it again clears", () => {
    expect(nextTurnSelection(null, 3, false, order)).toEqual([3]);
    expect(nextTurnSelection([3], 3, false, order)).toBeNull();
    expect(nextTurnSelection([2, 3], 3, false, order)).toEqual([3]);
  });

  it("shift-click extends to a contiguous range in either direction", () => {
    expect(nextTurnSelection([2], 4, true, order)).toEqual([2, 3, 4]);
    expect(nextTurnSelection([4], 1, true, order)).toEqual([1, 2, 3, 4]);
    expect(nextTurnSelection([2, 3, 4], 5, true, order)).toEqual([2, 3, 4, 5]);
    expect(nextTurnSelection(null, 4, true, order)).toEqual([4]);
  });

  it("arrows step from the selection's edge, or enter from outside the strip", () => {
    expect(stepTurnSelection(null, 1, order)).toEqual([1]);
    expect(stepTurnSelection(null, -1, order)).toEqual([5]);
    expect(stepTurnSelection([2, 3], 1, order)).toEqual([4]);
    expect(stepTurnSelection([2, 3], -1, order)).toEqual([1]);
    expect(stepTurnSelection([5], 1, order)).toEqual([5]);
  });

  it("stepTurn clamps and starts at the first turn", () => {
    expect(stepTurn(null, 1, order)).toBe(1);
    expect(stepTurn(3, -1, order)).toBe(2);
    expect(stepTurn(5, 1, order)).toBe(5);
    expect(stepTurn(1, 1, [])).toBeNull();
  });
});

describe("rail turn stops", () => {
  const cs: Pick<ChangeSet, "files" | "turns"> = {
    turns: [1, 2, 3, 4].map((n) => ({ n, prompt: `p${n}`, at: "", files: 0 })),
    files: [
      file("a.go", { turns: [2], added: 5 }),
      file("b.go", { turns: [2, 4], added: 50 }),
      file("a_test.go", { tier: "tests", turns: [3] }),
      file("x.tmp", { tier: "scratch", turns: [1] }),
    ],
  };

  it("walks All turns, turns with files in the tier, then Latest", () => {
    expect(railStops(cs, "source")).toEqual([null, 2, 4, "latest"]);
    expect(railStops(cs, "tests")).toEqual([null, 3, "latest"]);
    expect(railStops(cs, "all")).toEqual([null, 1, 2, 3, 4, "latest"]);
  });

  it("has only All turns when the session has no turns", () => {
    expect(railStops({ files: [], turns: [] }, "source")).toEqual([null]);
  });

  it("Latest follows the newest turn, even when it has no files in the tier", () => {
    expect(resolveRailTurn(cs, "latest")).toBe(4);
    expect(railFiles(cs, "source", "latest").map((f) => f.display)).toEqual(["b.go"]);
    expect(railFiles(cs, "tests", "latest")).toEqual([]);
  });

  it("lists the tier's files by size, optionally in one turn", () => {
    expect(railFiles(cs, "source", null).map((f) => f.display)).toEqual(["b.go", "a.go"]);
    expect(railFiles(cs, "all", 1).map((f) => f.display)).toEqual(["x.tmp"]);
  });

  it("steps and clamps; an unknown stop starts from All turns", () => {
    const stops = railStops(cs, "source");
    expect(stepRailStop(stops, null, 1)).toBe(2);
    expect(stepRailStop(stops, 4, 1)).toBe("latest");
    expect(stepRailStop(stops, "latest", 1)).toBe("latest");
    expect(stepRailStop(stops, 3, 1)).toBe(2);
    expect(stepRailStop(stops, null, -1)).toBeNull();
  });

  it("counts non-scratch files", () => {
    expect(countNonScratch({ ...cs } as ChangeSet)).toBe(3);
    expect(countNonScratch(null)).toBe(0);
  });
});
