import { describe, expect, it } from "vitest";
import { railLabelsFor, railTooltip, workspaceFactsByName } from "./railLabels";
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
  return { multiWorkspace: false, facts: workspaceFactsByName([MADS]), ...overrides };
}

describe("workspaceFactsByName", () => {
  it("prefers the repo marked primary", () => {
    expect(workspaceFactsByName([MADS]).get("mads")?.primaryRepo).toBe("/home/drew/repo/mads");
  });

  it("falls back to the only repo when none is marked primary", () => {
    const ws = makeWorkspace({ name: "solo", repos: [{ path: "/home/drew/repo/solo" }] });
    expect(workspaceFactsByName([ws]).get("solo")?.primaryRepo).toBe("/home/drew/repo/solo");
  });

  it("reads the default persona from either defaults.agent or defaults.persona", () => {
    const facts = workspaceFactsByName([
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
