import { describe, expect, it } from "vitest";
import { railLabelsFor, railTooltip, workspaceFacts } from "./railLabels";
import type { RailLabelContext } from "./railLabels";
import { agentIndex } from "../../../router/agentRef";
import type { Agent, Session, Workspace } from "../../../router/types";

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
});

function makeAgent(overrides: Partial<Agent>): Agent {
  return {
    id: "agt_1",
    name: "agent",
    prompt: "",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

function context(overrides: Partial<RailLabelContext> = {}): RailLabelContext {
  return {
    facts: workspaceFacts([MADS]),
    agents: agentIndex([]),
    ...overrides,
  };
}

describe("workspaceFacts", () => {
  // The live case: the router canonicalises Session.workspace to the workspace
  // *id* (sessiondefaults.go), so a facts map keyed only by name never hits.
  it("answers for a session that references its workspace by id or by name", () => {
    const facts = workspaceFacts([MADS]);
    expect(facts.get("ws")?.name).toBe("mads");
    expect(facts.get("mads")?.name).toBe("mads");
  });
});

describe("railLabelsFor", () => {
  it("shows the location and the branch for an isolated session", () => {
    const labels = railLabelsFor(
      makeSession({
        workspace: "mads",
        dir: "/home/drew/repo/mads",
        worktree_branch: "mads/session/ses_k3f9",
        worktree_base: "main",
        worktree_dir: "/home/drew/.mads/session-worktrees/ses_k3f9",
        cwd: "/home/drew/.mads/session-worktrees/ses_k3f9",
      }),
      context(),
    );
    expect(labels).toEqual({ location: "mads", branch: "mads/session/ses_k3f9", retro: false });
  });

  it("shows no branch for a session working in the repo itself", () => {
    const labels = railLabelsFor(
      makeSession({ workspace: "mads", isolation: "current_dir", cwd: "/home/drew/repo/mads" }),
      context(),
    );
    expect(labels.branch).toBeUndefined();
  });

  // Always, not only when the list spans more than one workspace: "which
  // project is this?" is asked of every card.
  it("shows the workspace in a single-workspace rail", () => {
    expect(railLabelsFor(makeSession({ workspace: "mads" }), context()).location).toBe("mads");
  });

  it("labels an id-referenced workspace with its name, never the id", () => {
    expect(railLabelsFor(makeSession({ workspace: "ws" }), context()).location).toBe("mads");
  });

  it("names the workspace even when the folder is somewhere else", () => {
    const labels = railLabelsFor(
      makeSession({ workspace: "ws", dir: "/home/drew/repo/mads-ui" }),
      context(),
    );
    expect(labels.location).toBe("mads");
  });

  it("falls back to the folder's basename for a session with no workspace", () => {
    expect(railLabelsFor(makeSession({ dir: "/home/drew/scratch" }), context()).location).toBe("scratch");
    expect(railLabelsFor(makeSession({ cwd: "/home/drew/notes" }), context()).location).toBe("notes");
  });

  // An isolated session's cwd is its worktree, named for the session id — the
  // folder the user chose is the answer.
  it("never names an isolated session after its worktree", () => {
    const labels = railLabelsFor(
      makeSession({
        dir: "/home/drew/repo/sidecar",
        worktree_branch: "mads/session/ses_k3f9",
        worktree_dir: "/home/drew/.mads/session-worktrees/ses_k3f9",
        cwd: "/home/drew/.mads/session-worktrees/ses_k3f9",
      }),
      context(),
    );
    expect(labels.location).toBe("sidecar");
  });

  it("gives a session that names no place no location", () => {
    for (const cwd of [".", "./", "", " "]) {
      expect(railLabelsFor(makeSession({ cwd }), context()).location).toBeUndefined();
    }
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
        dir: "/home/drew/repo/mads",
        cwd: "/home/drew/.mads/session-worktrees/ses_k3f9",
        worktree_dir: "/home/drew/.mads/session-worktrees/ses_k3f9",
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
      "Folder: ~/repo/mads",
      "Worktree: ~/.mads/session-worktrees/ses_k3f9",
      "Branch: wt/goose-acp",
      "Agent: reviewer",
      "Model: claude-opus-5",
      "Provider: claude-code",
      "Trust: full",
      "Isolation: worktree",
      "Context: 45% of 200k",
    ]);
  });

  // The context line no longer carries the persona at all — the harness mark
  // on the title row took the glance it was competing for. The tooltip is
  // where it lives now, so this is where the reference still has to resolve:
  // an `agt_...` is a hash, and printing one answers nothing.
  it("names the persona an agt_ reference points at, rather than printing the id", () => {
    const partner = makeAgent({ id: "agt_3212544bb2e37c92", name: "mads-coding-partner" });
    const tooltip = railTooltip(
      makeSession({ agent: "agt_3212544bb2e37c92" }),
      context({ agents: agentIndex([partner]) }),
    );
    expect(tooltip).toBe("Agent: mads-coding-partner");
  });

  // A session whose persona record is gone still has a persona line — but
  // a hash in it answers nothing, so it reads the way the header pill does.
  it("reads an unresolvable persona id as Unknown agent", () => {
    expect(railTooltip(makeSession({ agent: "agt_deleted" }), context())).toBe("Agent: Unknown agent");
  });

  // The record stores cwd "." for any session without an explicitly chosen
  // directory; "Folder: ." is not an answer.
  it("does not offer a bare dot as a folder", () => {
    expect(railTooltip(makeSession({ cwd: "." }), context())).toBe("");
    expect(railTooltip(makeSession({ cwd: "./" }), context())).toBe("");
  });

  // A requested level the provider can't run says what it runs as instead,
  // never just the request.
  it("shows trust with its fallback when the session runs at another level", () => {
    expect(
      railTooltip(makeSession({ trust: "auto_review", effective_trust: "limited" }), context()),
    ).toBe("Trust: auto_review → limited");
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
