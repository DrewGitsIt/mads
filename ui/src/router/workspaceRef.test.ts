import { describe, expect, it } from "vitest";
import {
  EMPTY_WORKSPACE_INDEX,
  resolveWorkspaceRef,
  workspaceIndex,
  workspaceNameForRef,
} from "./workspaceRef";
import type { Workspace } from "./types";

function makeWorkspace(overrides: Partial<Workspace>): Workspace {
  return {
    id: "wsp_b8439a998b38a07f",
    name: "mads",
    defaults: {},
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

const MADS = makeWorkspace({});

describe("resolveWorkspaceRef", () => {
  // The router canonicalises Session.workspace to the id on create
  // (sessiondefaults.go), while the registry's own lookup still takes either —
  // so both forms have to resolve.
  it("resolves a reference by id", () => {
    expect(resolveWorkspaceRef("wsp_b8439a998b38a07f", workspaceIndex([MADS]))).toBe(MADS);
  });

  it("resolves a reference by name", () => {
    expect(resolveWorkspaceRef("mads", workspaceIndex([MADS]))).toBe(MADS);
  });

  it("resolves nothing for an absent or unknown reference", () => {
    expect(resolveWorkspaceRef(undefined, workspaceIndex([MADS]))).toBeUndefined();
    expect(resolveWorkspaceRef("wsp_gone", workspaceIndex([MADS]))).toBeUndefined();
    expect(resolveWorkspaceRef("mads", EMPTY_WORKSPACE_INDEX)).toBeUndefined();
  });

  it("lets an id win over a name that collides with it", () => {
    const decoy = makeWorkspace({ id: "wsp_decoy", name: "wsp_b8439a998b38a07f" });
    const index = workspaceIndex([decoy, MADS]);
    expect(resolveWorkspaceRef("wsp_b8439a998b38a07f", index)).toBe(MADS);
    expect(resolveWorkspaceRef("wsp_decoy", index)).toBe(decoy);
  });
});

describe("workspaceNameForRef", () => {
  it("turns an id into the name the user reads", () => {
    expect(workspaceNameForRef("wsp_b8439a998b38a07f", workspaceIndex([MADS]))).toBe("mads");
  });

  it("stands a reference in for itself when it resolves to nothing", () => {
    expect(workspaceNameForRef("wsp_gone", workspaceIndex([MADS]))).toBe("wsp_gone");
  });

  it("has no name for a session with no workspace", () => {
    expect(workspaceNameForRef(undefined, workspaceIndex([MADS]))).toBeUndefined();
  });
});
