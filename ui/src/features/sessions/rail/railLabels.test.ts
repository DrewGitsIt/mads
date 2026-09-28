import { describe, expect, it } from "vitest";
import { railLabelsFor, railTooltip, workspaceFacts } from "./railLabels";
import type { RailLabelContext } from "./railLabels";
import type { Session, Workspace } from "../../../router/types";

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

function makeWorkspace(overrides: Partial<Workspace>): Workspace {
  return {
    id: "ws",
    name: "mads",
    defaults: {},
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

const MADS = makeWorkspace({
  name: "mads",
  repos: [{ path: "/home/drew/repo/mads", primary: true }, { path: "/home/drew/repo/mads-ui" }],
  defaults: { agent: "builder" },
});

function context(overrides: Partial<RailLabelContext> = {}): RailLabelContext {
  return { multiWorkspace: false, facts: workspaceFacts([MADS]), ...overrides };
}

describe("workspaceFacts", () => {
  // The live case: the router canonicalises Session.workspace to the workspace
  // *id* (sessiondefaults.go), so a facts map keyed only by name never hits.
  it("answers for a session that references its workspace by id", () => {
    const facts = workspaceFacts([MADS]);
    expect(facts.get("ws")?.primaryRepo).toBe("/home/drew/repo/mads");
    expect(facts.get("ws")?.defaultAgent).toBe("builder");
    expect(facts.get("ws")?.name).toBe("mads");
  });

  it("prefers the repo marked primary", () => {
    expect(workspaceFacts([MADS]).get("mads")?.primaryRepo).toBe("/home/drew/repo/mads");
  });

  it("falls back to the only repo when none is marked primary", () => {
    const ws = makeWorkspace({ name: "solo", repos: [{ path: "/home/drew/repo/solo" }] });
    expect(workspaceFacts([ws]).get("solo")?.primaryRepo).toBe("/home/drew/repo/solo");
  });

  it("reads the default persona from either defaults.agent or defaults.persona", () => {
    const facts = workspaceFacts([
      makeWorkspace({ name: "a", defaults: { agent: "builder" } }),
      makeWorkspace({ id: "ws-b", name: "b", defaults: { persona: "reviewer" } }),
    ]);
    expect(facts.get("a")?.defaultAgent).toBe("builder");
    expect(facts.get("b")?.defaultAgent).toBe("reviewer");
  });
});

describe("railLabelsFor", () => {
  it("shows the branch for an isolated session", () => {
    const labels = railLabelsFor(
      makeSession({
        workspace: "mads",
        isolation: "worktree",
        worktree_branch: "wt/goose-acp",
        worktree_dir: "/home/drew/repo/mads-wt/goose-acp",
        cwd: "/home/drew/repo/mads-wt/goose-acp",
      }),
      context(),
    );
    expect(labels.branch).toBe("wt/goose-acp");
  });

  it("shows no branch for a session working in the repo itself", () => {
    const labels = railLabelsFor(
      makeSession({ workspace: "mads", isolation: "current_dir", cwd: "/home/drew/repo/mads" }),
      context(),
    );
    expect(labels.branch).toBeUndefined();
  });

  it("shows the workspace only when the visible list spans more than one", () => {
    const session = makeSession({ workspace: "mads" });
    expect(railLabelsFor(session, context()).workspace).toBeUndefined();
    expect(railLabelsFor(session, context({ multiWorkspace: true })).workspace).toBe("mads");
  });

  it("labels an id-referenced workspace with its name, never the id", () => {
    const session = makeSession({ workspace: "ws" });
    expect(railLabelsFor(session, context({ multiWorkspace: true })).workspace).toBe("mads");
  });

  // The whole defect, in the shape the running instance has it: an
  // id-referenced current_dir session in a single-workspace instance. Nothing
  // it carries is worth a label, so the context line carries nothing.
  it("gives a current_dir session on its workspace's default agent no labels at all", () => {
    const labels = railLabelsFor(
      makeSession({ workspace: "ws", isolation: "current_dir", cwd: ".", agent: "builder" }),
      context(),
    );
    expect(labels).toEqual({
      branch: undefined,
      workspace: undefined,
      cwd: undefined,
      agent: undefined,
      retro: false,
    });
  });

  it("never labels a cwd that is not a location", () => {
    for (const cwd of [".", "./", "", " "]) {
      expect(railLabelsFor(makeSession({ workspace: "ws", cwd }), context()).cwd).toBeUndefined();
    }
  });

  it("still labels a cwd that differs from the primary repo of an id-referenced workspace", () => {
    const labels = railLabelsFor(
      makeSession({ workspace: "ws", cwd: "/home/drew/repo/mads-ui" }),
      context(),
    );
    expect(labels.cwd).toBe("~/repo/mads-ui");
  });

  it("drops the agent label for the default agent of an id-referenced workspace", () => {
    expect(
      railLabelsFor(makeSession({ workspace: "ws", agent: "builder" }), context()).agent,
    ).toBeUndefined();
    expect(railLabelsFor(makeSession({ workspace: "ws", agent: "reviewer" }), context()).agent).toBe(
      "reviewer",
    );
  });

  it("hides the cwd when it is the workspace's primary repo", () => {
    const labels = railLabelsFor(
      makeSession({ workspace: "mads", cwd: "/home/drew/repo/mads" }),
      context(),
    );
    expect(labels.cwd).toBeUndefined();
  });

  it("ignores a trailing slash when comparing against the primary repo", () => {
    const labels = railLabelsFor(
      makeSession({ workspace: "mads", cwd: "/home/drew/repo/mads/" }),
      context(),
    );
    expect(labels.cwd).toBeUndefined();
  });

  it("shows the cwd, home-collapsed, when it differs from the primary repo", () => {
    const labels = railLabelsFor(
      makeSession({ workspace: "mads", cwd: "/home/drew/repo/mads-ui" }),
      context(),
    );
    expect(labels.cwd).toBe("~/repo/mads-ui");
  });

  it("shows the cwd for a session with no workspace to judge it against", () => {
    const labels = railLabelsFor(makeSession({ cwd: "/home/drew/scratch" }), context());
    expect(labels.cwd).toBe("~/scratch");
  });

  it("does not spend the card's second slot repeating the worktree the branch already names", () => {
    const labels = railLabelsFor(
      makeSession({
        workspace: "mads",
        isolation: "worktree",
        worktree_branch: "wt/goose-acp",
        worktree_dir: "/home/drew/repo/mads-wt/goose-acp",
        cwd: "/home/drew/repo/mads-wt/goose-acp",
      }),
      context(),
    );
    expect(labels.cwd).toBeUndefined();
  });

  it("still shows the cwd of an isolated session that is not in its worktree", () => {
    const labels = railLabelsFor(
      makeSession({
        workspace: "mads",
        worktree_branch: "wt/goose-acp",
        worktree_dir: "/home/drew/repo/mads-wt/goose-acp",
        cwd: "/home/drew/repo/other",
      }),
      context(),
    );
    expect(labels.cwd).toBe("~/repo/other");
  });

  it("shows the agent only when it is not the workspace's default persona", () => {
    expect(railLabelsFor(makeSession({ workspace: "mads", agent: "builder" }), context()).agent).toBeUndefined();
    expect(railLabelsFor(makeSession({ workspace: "mads", agent: "reviewer" }), context()).agent).toBe(
      "reviewer",
    );
  });

  it("marks retro sessions, by record or by tag", () => {
    expect(railLabelsFor(makeSession({ retro_id: "retro-1" }), context()).retro).toBe(true);
    expect(railLabelsFor(makeSession({ tags: ["retro"] }), context()).retro).toBe(true);
    expect(railLabelsFor(makeSession({ tags: ["other"] }), context()).retro).toBe(false);
  });
});

describe("railTooltip", () => {
  it("carries the fields the resting card does not show", () => {
    const tooltip = railTooltip(
      makeSession({
        workspace: "mads",
        cwd: "/home/drew/repo/mads",
        worktree_branch: "wt/goose-acp",
        agent: "reviewer",
        model: "claude-opus-5",
        provider: "claude-code",
        trust: "full",
        isolation: "worktree",
        last_usage: { input_tokens: 1000, output_tokens: 10, context_tokens: 90_000 },
      }),
    );
    expect(tooltip.split("\n")).toEqual([
      "Workspace: mads",
      "Directory: ~/repo/mads",
      "Branch: wt/goose-acp",
      "Agent: reviewer",
      "Model: claude-opus-5",
      "Provider: claude-code",
      "Trust: full",
      "Isolation: worktree",
      "Context: 45% of 200k",
    ]);
  });

  it("names the workspace an id reference points at", () => {
    expect(railTooltip(makeSession({ workspace: "ws" }), context())).toBe("Workspace: mads");
  });

  it("falls back to the reference when it resolves to no workspace", () => {
    expect(railTooltip(makeSession({ workspace: "wsp_gone" }), context())).toBe(
      "Workspace: wsp_gone",
    );
  });

  it("prefers the provider-reported window over the static table", () => {
    const tooltip = railTooltip(
      makeSession({
        model: "local-thing",
        last_usage: { input_tokens: 0, output_tokens: 0, context_tokens: 64_000, context_window: 128_000 },
      }),
    );
    expect(tooltip).toContain("Context: 50% of 128k");
  });

  it("omits every field the record does not carry", () => {
    expect(railTooltip(makeSession({}))).toBe("");
  });
});
