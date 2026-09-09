import { describe, expect, it } from "vitest";
import { displayPath } from "./displayPath";

describe("displayPath", () => {
  it("strips the worktree prefix when the path is under it", () => {
    expect(
      displayPath(
        "/home/drew/.mads/worktrees/ses_x/tsk_a/services/session/internal/runner/runner.go",
        "/home/drew/.mads/worktrees/ses_x/tsk_a",
      ),
    ).toBe("services/session/internal/runner/runner.go");
  });

  it("tolerates a trailing slash on worktreeDir", () => {
    expect(displayPath("/wt/src/a.ts", "/wt/")).toBe("src/a.ts");
  });

  it("does not strip a sibling directory that merely shares the prefix string", () => {
    expect(displayPath("/wt-other/src/a.ts", "/wt")).toBe("/wt-other/src/a.ts");
  });

  it("collapses the home dir to ~ when the path is outside the worktree", () => {
    expect(displayPath("/home/drew/repo/mads/ui/src/App.tsx", "/wt")).toBe(
      "~/repo/mads/ui/src/App.tsx",
    );
    expect(displayPath("/Users/drew/repo/x.go")).toBe("~/repo/x.go");
  });

  it("collapses a bare home dir to ~", () => {
    expect(displayPath("/home/drew")).toBe("~");
  });

  it("leaves non-home absolute paths alone", () => {
    expect(displayPath("/tmp/scratch.txt", "/wt")).toBe("/tmp/scratch.txt");
  });

  it("passes already-relative paths through unchanged", () => {
    expect(displayPath("services/data/internal/ingest.go")).toBe(
      "services/data/internal/ingest.go",
    );
    expect(displayPath("services/data/internal/ingest.go", "/wt")).toBe(
      "services/data/internal/ingest.go",
    );
  });
});
