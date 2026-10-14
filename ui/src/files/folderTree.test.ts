import { describe, expect, it } from "vitest";
import { OUTSIDE_ROOT, buildFolderTree, type FolderNode } from "./folderTree";
import { file } from "./testChangeFile";

/** Indented outline: "dir/ (count +a −d)" and "file". */
function outline(n: FolderNode, depth = 0): string[] {
  const pad = "  ".repeat(depth);
  return [
    ...n.dirs.flatMap((d) => [
      `${pad}${d.name}/ (${d.count} +${d.added} −${d.deleted}${d.low ? " low" : ""})`,
      ...outline(d, depth + 1),
    ]),
    ...n.files.map((f) => `${pad}${f.display.split("/").pop()}`),
  ];
}

describe("buildFolderTree", () => {
  it("compacts single-child chains, puts folders before files, sorts alphabetically, rolls up", () => {
    const tree = buildFolderTree([
      file("internal/auth/token.go", { added: 10, deleted: 2 }),
      file("internal/auth/poll.go", { added: 5 }),
      file("internal/auth/testdata/ok.json", { added: 1, tier: "tests" }),
      file("cmd/mycli/login.go", { added: 3 }),
      file("Makefile", { added: 1, tier: "config" }),
      file("go.mod", { added: 2, tier: "config" }),
    ]);
    expect(outline(tree)).toEqual([
      "cmd/mycli/ (1 +3 −0)",
      "  login.go",
      "internal/auth/ (3 +16 −2)",
      "  testdata/ (1 +1 −0)",
      "    ok.json",
      "  poll.go",
      "  token.go",
      "go.mod",
      "Makefile",
    ]);
    expect(tree.count).toBe(6);
  });

  it("does not compact a folder that holds files beside its one subfolder", () => {
    const tree = buildFolderTree([file("ui/package.json"), file("ui/src/settings/Pane.tsx")]);
    expect(outline(tree)).toEqual(["ui/ (2 +2 −0)", "  src/settings/ (1 +1 −0)", "    Pane.tsx", "  package.json"]);
  });

  it("marks folders holding only generated or scratch files as low", () => {
    const tree = buildFolderTree([
      file("internal/mocks/mock_a.go", { tier: "generated" }),
      file("internal/mocks/mock_b.go", { tier: "generated" }),
      file("internal/x.go"),
    ]);
    const internal = tree.dirs[0];
    expect(internal.low).toBe(false);
    expect(internal.dirs[0].name).toBe("mocks");
    expect(internal.dirs[0].low).toBe(true);
  });

  it("puts files outside the session folder under their own root, last", () => {
    const tree = buildFolderTree([
      file("/tmp/devflow.log", { path: "/tmp/devflow.log", display: "/tmp/devflow.log", outside: true, tier: "scratch" }),
      file("zzz/a.go"),
    ]);
    expect(tree.dirs.map((d) => d.name)).toEqual(["zzz", OUTSIDE_ROOT]);
    const outside = tree.dirs[1];
    expect(outside.outside).toBe(true);
    expect(outside.dirs[0].name).toBe("tmp");
    expect(outside.dirs[0].files[0].display).toBe("/tmp/devflow.log");
  });

  it("gives every folder a stable key from the root", () => {
    const tree = buildFolderTree([file("a/b/c.go"), file("a/d.go")]);
    expect(tree.dirs[0].key).toBe("a");
    expect(tree.dirs[0].dirs[0].key).toBe("a/b");
  });
});
