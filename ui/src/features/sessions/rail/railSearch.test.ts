import { describe, expect, it } from "vitest";
import {
  isEditableElement,
  matchReason,
  matchSession,
  parseSearchQuery,
  scopeSuggestions,
} from "./railSearch";
import type { Session } from "../../../router/types";

function makeSession(overrides: Partial<Session>): Session {
  return {
    id: "s1",
    name: "session",
    activity: "active",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

// The mockup's own worked example: a workspace name that shares no substring
// with the directory it lives in, which is why both are indexed.
const railReadability = makeSession({
  id: "ui",
  name: "rail readability pass",
  workspace: "Main work",
  cwd: "~/repo/mads-ui",
});

describe("parseSearchQuery", () => {
  it("splits on whitespace and lowercases", () => {
    expect(parseSearchQuery("  Goose   ACP ")).toEqual([{ needle: "goose" }, { needle: "acp" }]);
  });

  it("reads a scope prefix into the term's field", () => {
    expect(parseSearchQuery("workspace:mads dir:~/repo")).toEqual([
      { field: "workspace", needle: "mads" },
      { field: "dir", needle: "~/repo" },
    ]);
  });

  it("keeps a quoted scope value as one term", () => {
    expect(parseSearchQuery('workspace:"Main work"')).toEqual([
      { field: "workspace", needle: "main work" },
    ]);
  });

  it("drops a half-typed token rather than matching everything", () => {
    expect(parseSearchQuery("workspace:")).toEqual([]);
  });
});

describe("matchSession", () => {
  const session = makeSession({
    name: "goose ACP transcript gap",
    workspace: "mads",
    cwd: "~/repo/mads",
    worktree_branch: "wt/goose-acp",
  });

  it("matches on each of the four fields", () => {
    expect(matchSession(session, parseSearchQuery("transcript"))?.where).toEqual(["name"]);
    expect(matchSession(makeSession({ name: "x", workspace: "sidecar" }), parseSearchQuery("sidecar"))?.where).toEqual(
      ["workspace"],
    );
    expect(matchSession(makeSession({ name: "x", cwd: "~/repo/sidecar" }), parseSearchQuery("repo/side"))?.where).toEqual(
      ["dir"],
    );
    expect(matchSession(makeSession({ name: "x", worktree_branch: "wt/digest" }), parseSearchQuery("wt/"))?.where).toEqual(
      ["branch"],
    );
  });

  it("finds a session whose workspace name shares no substring with its cwd", () => {
    expect(matchSession(railReadability, parseSearchQuery("mads"))?.where).toEqual(["dir"]);
    expect(matchSession(railReadability, parseSearchQuery("main"))?.where).toEqual(["workspace"]);
  });

  it("ANDs the terms together", () => {
    expect(matchSession(session, parseSearchQuery("goose acp"))).toBeTruthy();
    expect(matchSession(session, parseSearchQuery("goose nope"))).toBeUndefined();
  });

  it("is case-insensitive substring, not fuzzy", () => {
    expect(matchSession(session, parseSearchQuery("ACP TRANS"))).toBeTruthy();
    expect(matchSession(session, parseSearchQuery("GOOSE"))).toBeTruthy();
    // "gap" reordered — a fuzzy matcher would take it, a substring one does not.
    expect(matchSession(session, parseSearchQuery("gpa"))).toBeUndefined();
  });

  it("pins a scoped term to its one field", () => {
    expect(matchSession(railReadability, parseSearchQuery("dir:mads"))).toBeTruthy();
    expect(matchSession(railReadability, parseSearchQuery("workspace:mads"))).toBeUndefined();
    expect(matchSession(railReadability, parseSearchQuery('workspace:"main work"'))).toBeTruthy();
  });

  it("ranks name prefix over name substring over everything else", () => {
    expect(matchSession(session, parseSearchQuery("goose"))?.rank).toBe(0);
    expect(matchSession(session, parseSearchQuery("acp"))?.rank).toBe(1);
    expect(matchSession(session, parseSearchQuery("wt/"))?.rank).toBe(2);
  });

  it("does not rank a scoped term on the name it was forbidden to look at", () => {
    const named = makeSession({ name: "mads rail", workspace: "mads" });
    expect(matchSession(named, parseSearchQuery("mads"))?.rank).toBe(0);
    expect(matchSession(named, parseSearchQuery("workspace:mads"))?.rank).toBe(2);
  });
});

describe("matchReason", () => {
  it("is absent when the name carried the match", () => {
    const match = matchSession(railReadability, parseSearchQuery("readability"))!;
    expect(matchReason(railReadability, match)).toBeUndefined();
  });

  it("names the field that did carry it", () => {
    const match = matchSession(railReadability, parseSearchQuery("mads"))!;
    expect(matchReason(railReadability, match)).toEqual({ field: "dir", value: "~/repo/mads-ui" });
  });

  it("prefers workspace, then branch, then dir — one reason, never an inventory", () => {
    const all = makeSession({
      name: "x",
      workspace: "~/repo",
      cwd: "~/repo/mads",
      worktree_branch: "~/repo-branch",
    });
    const match = matchSession(all, parseSearchQuery("~/repo"))!;
    expect(match.where).toEqual(["workspace", "dir", "branch"]);
    expect(matchReason(all, match)).toEqual({ field: "workspace", value: "~/repo" });
  });
});

describe("scopeSuggestions", () => {
  const sessions = [
    makeSession({ id: "a", workspace: "mads", cwd: "~/repo/mads" }),
    makeSession({ id: "b", workspace: "mads", cwd: "~/repo/mads" }),
    makeSession({ id: "c", workspace: "Main work", cwd: "~/repo/mads-ui" }),
  ];

  it("offers a committable token per matching workspace and directory, with counts", () => {
    expect(scopeSuggestions(sessions, "mads")).toEqual([
      { field: "workspace", value: "mads", token: "workspace:mads", count: 2 },
      { field: "dir", value: "~/repo/mads", token: "dir:~/repo/mads", count: 2 },
      { field: "dir", value: "~/repo/mads-ui", token: "dir:~/repo/mads-ui", count: 1 },
    ]);
  });

  it("quotes a value with whitespace so the token survives a re-parse", () => {
    const [chip] = scopeSuggestions(sessions, "main");
    expect(chip.token).toBe('workspace:"Main work"');
    expect(parseSearchQuery(chip.token)).toEqual([{ field: "workspace", needle: "main work" }]);
  });

  it("offers nothing for an empty query or one that already carries a scope", () => {
    expect(scopeSuggestions(sessions, "  ")).toEqual([]);
    expect(scopeSuggestions(sessions, "workspace:mads")).toEqual([]);
  });
});

describe("isEditableElement", () => {
  it("is true for the fields that take the user's typing", () => {
    expect(isEditableElement(document.createElement("textarea"))).toBe(true);
    expect(isEditableElement(document.createElement("input"))).toBe(true);
    expect(isEditableElement(document.createElement("div"))).toBe(false);
    expect(isEditableElement(null)).toBe(false);
  });
});
