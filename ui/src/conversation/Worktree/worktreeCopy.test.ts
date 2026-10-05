import { describe, expect, it } from "vitest";
import type { LandRefusalReason } from "../../router/types";
import { commitPathReason, nameFiles, refusalCopy, resolveConflictPrompt, undoRefusalCopy } from "./worktreeCopy";

const EVERY_REASON: LandRefusalReason[] = [
  "not_isolated",
  "busy",
  "pipeline",
  "mid_merge",
  "off_branch",
  "dirty",
  "base_not_checked_out",
  "primary_mid_merge",
  "nothing_to_land",
  "merge_failed",
  "base_moving",
  "primary_dirty",
  "primary_untracked",
  "primary_refused",
  "locked",
];

describe("refusalCopy", () => {
  it.each(EVERY_REASON)("has its own sentence for %s", (reason) => {
    const copy = refusalCopy(reason, "main", ["scripts/deploy.sh"]);
    expect(copy.title).not.toBe("Land didn't run");
    expect(copy.title.length).toBeGreaterThan(0);
    expect(copy.body.length).toBeGreaterThan(0);
  });

  it("gives every reason a different title or body", () => {
    const seen = new Set(EVERY_REASON.map((r) => JSON.stringify(refusalCopy(r, "main", ["a.go"]))));
    expect(seen.size).toBe(EVERY_REASON.length);
  });

  it("says busy in the mockup's words", () => {
    expect(refusalCopy("busy", "main", undefined)).toMatchObject({
      title: "Can't land while the agent is working",
      body: "Land waits for the current turn to finish. Nothing changed.",
    });
  });

  it("names the files that block the checkout", () => {
    expect(refusalCopy("primary_dirty", "main", ["scripts/deploy.sh"]).body).toBe(
      "Git won't land over scripts/deploy.sh in your checkout. Commit or stash it there, then Land again.",
    );
    expect(refusalCopy("primary_untracked", "main", ["a", "b"]).body).toContain("a and b, which aren't tracked");
    expect(refusalCopy("dirty", "main", ["x.go"]).body).toBe("Commit x.go first. Land only moves committed work.");
  });

  it("names the base branch", () => {
    expect(refusalCopy("base_not_checked_out", "release", undefined).title).toBe(
      "release isn't checked out in your checkout",
    );
    expect(refusalCopy("nothing_to_land", "main", undefined).body).toContain("already in main");
  });

  it("offers Land again only where the user fixes something and comes back", () => {
    expect(refusalCopy("busy", "main", undefined).retry).toBe(false);
    expect(refusalCopy("nothing_to_land", "main", undefined).retry).toBe(false);
    expect(refusalCopy("primary_dirty", "main", undefined).retry).toBe(true);
  });

  it("falls back for a reason it doesn't know", () => {
    expect(refusalCopy("something_new", "main", undefined).title).toBe("Land didn't run");
  });
});

describe("worktree copy helpers", () => {
  it("names a few files, then counts the rest", () => {
    expect(nameFiles(["a"])).toBe("a");
    expect(nameFiles(["a", "b"])).toBe("a and b");
    expect(nameFiles(["a", "b", "c"])).toBe("a, b and c");
    expect(nameFiles(["a", "b", "c", "d", "e"])).toBe("a, b, c and 2 more");
  });

  it("drafts an editable resolve message naming base and files", () => {
    expect(resolveConflictPrompt("main", ["scripts/deploy.sh", "ci.yml"])).toBe(
      "Merge main into this branch and resolve the conflicts in scripts/deploy.sh, ci.yml, keeping both sides' changes. Then commit the merge.",
    );
  });

  it("explains undo refusals and per-path commit outcomes", () => {
    expect(undoRefusalCopy("not_tip")).toMatch(/newer commits/);
    expect(undoRefusalCopy("locked")).toMatch(/try again in a moment/i);
    expect(undoRefusalCopy("already_pushed")).toBe("Can't undo: it has already been pushed.");
    expect(commitPathReason("already_committed")).toBe("already committed");
    expect(commitPathReason("rejected", "outside_folder")).toBe("outside this session's folder");
  });
});
