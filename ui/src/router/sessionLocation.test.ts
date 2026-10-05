import { describe, expect, it } from "vitest";
import { isLocation, pathBasename, sessionLocation } from "./sessionLocation";
import type { Session } from "./types";

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

const ISOLATED: Partial<Session> = {
  dir: "/home/drew/repo/mads",
  cwd: "/home/drew/.mads/session-worktrees/ses_k3f9",
  worktree_dir: "/home/drew/.mads/session-worktrees/ses_k3f9",
  worktree_branch: "mads/session/ses_k3f9",
  worktree_base: "main",
};

describe("sessionLocation", () => {
  it("prefers the workspace name", () => {
    expect(sessionLocation(makeSession({ dir: "/home/drew/repo/mads-ui" }), "mads").name).toBe("mads");
  });

  it("falls back to the chosen folder's basename", () => {
    const where = sessionLocation(makeSession({ dir: "/home/drew/scratch/notes/" }));
    expect(where).toEqual({ name: "notes", folder: "/home/drew/scratch/notes/", worktree: undefined });
  });

  it("falls back to cwd when no folder was recorded", () => {
    expect(sessionLocation(makeSession({ cwd: "/srv/app" })).name).toBe("app");
  });

  // An isolated session runs inside its worktree, whose basename is the
  // session id — the folder the user chose is the answer, the worktree rides
  // along for tooltips.
  it("names an isolated session by the folder the user chose, never its worktree", () => {
    const where = sessionLocation(makeSession(ISOLATED));
    expect(where.name).toBe("mads");
    expect(where.folder).toBe("/home/drew/repo/mads");
    expect(where.worktree).toBe("/home/drew/.mads/session-worktrees/ses_k3f9");
  });

  it("does not fall back to an isolated session's cwd", () => {
    expect(sessionLocation(makeSession({ ...ISOLATED, dir: undefined })).name).toBeUndefined();
  });

  it("names nothing for a record with no place on it", () => {
    expect(sessionLocation(makeSession({ cwd: "." })).name).toBeUndefined();
    expect(sessionLocation(makeSession({})).name).toBeUndefined();
  });
});

describe("isLocation", () => {
  it("rejects the forms that name no place", () => {
    for (const dir of [undefined, "", " ", ".", "./"]) expect(isLocation(dir)).toBe(false);
    expect(isLocation("/x")).toBe(true);
  });
});

describe("pathBasename", () => {
  it("takes the last segment, ignoring trailing slashes", () => {
    expect(pathBasename("/home/drew/repo/mads")).toBe("mads");
    expect(pathBasename("/home/drew/repo/mads//")).toBe("mads");
    expect(pathBasename("~/repo/mads")).toBe("mads");
    expect(pathBasename("mads")).toBe("mads");
    expect(pathBasename("/")).toBe("/");
  });
});
