import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { SessionRail } from "./SessionRail";
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

const WORKSPACES: Workspace[] = [
  {
    id: "ws-mads",
    name: "mads",
    defaults: { agent: "builder" },
    repos: [{ path: "/home/drew/repo/mads", primary: true }],
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
  },
  {
    id: "ws-sidecar",
    name: "sidecar",
    defaults: {},
    repos: [{ path: "/home/drew/repo/sidecar", primary: true }],
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
  },
];

function cards() {
  return [...screen.getByLabelText("Session list").querySelectorAll('div[role="button"]')];
}

function cardFor(name: string) {
  return screen.getByText(name).closest('[role="button"]') as HTMLElement;
}

/** Whether this card spends a line on context at all (railLabels.reservesContextLine). */
function hasContextLine(card: HTMLElement): boolean {
  return card.querySelector('[class*="context"]') !== null;
}

function labelsOn(card: HTMLElement): Record<string, string> {
  const line = card.querySelector('[class*="context"]');
  if (!line) return {};
  return Object.fromEntries(
    [...line.querySelectorAll("[data-label]")].map((el) => [
      el.getAttribute("data-label")!,
      el.textContent!,
    ]),
  );
}

describe("SessionRail adaptive context labels", () => {
  it("shows the branch for an isolated session and nothing for one working in the repo", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({
            id: "wt",
            name: "isolated one",
            workspace: "mads",
            isolation: "worktree",
            worktree_branch: "wt/goose-acp",
            worktree_dir: "/home/drew/repo/mads-wt/goose-acp",
            cwd: "/home/drew/repo/mads-wt/goose-acp",
          }),
          makeSession({
            id: "plain",
            name: "plain one",
            workspace: "mads",
            isolation: "current_dir",
            cwd: "/home/drew/repo/mads",
          }),
        ]}
        workspaces={WORKSPACES}
        openTaskCounts={{}}
      />,
    );
    expect(labelsOn(cardFor("isolated one"))).toEqual({ branch: "wt/goose-acp" });
    expect(labelsOn(cardFor("plain one"))).toEqual({});
    expect(hasContextLine(cardFor("plain one"))).toBe(false);
  });

  // The shape the running instance actually has: the router writes the
  // workspace *id* onto the session (sessiondefaults.go), and an
  // isolation:current_dir session stores cwd ".". Neither is a label — and
  // since no session in this rail has one either, the line is not reserved at
  // all. An empty line still costs its reserved height, which on this instance
  // was 17px of blank on every card.
  it("gives no context line at all to a rail whose sessions have no labels", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({
            id: "a",
            name: "current dir one",
            workspace: "ws-mads",
            isolation: "current_dir",
            cwd: ".",
            agent: "builder",
          }),
          makeSession({
            id: "b",
            name: "current dir two",
            workspace: "ws-mads",
            isolation: "current_dir",
            cwd: ".",
          }),
        ]}
        workspaces={WORKSPACES}
        openTaskCounts={{}}
      />,
    );
    for (const name of ["current dir one", "current dir two"]) {
      expect(cardFor(name).querySelector('[class*="context"]')).toBeNull();
    }
  });

  // The reservation is per card, not per rail. One session with a branch used
  // to give every other card an empty 17px line — the unaccounted-for
  // whitespace in the rail. The reservation protects a card that can gain or
  // lose a label (see the filter test below); a card that can do neither was
  // just carrying blank.
  it("spends the line only on the cards that have something to put on it", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a", name: "plain one", workspace: "ws-mads", cwd: "." }),
          makeSession({
            id: "b",
            name: "isolated one",
            workspace: "ws-mads",
            isolation: "worktree",
            worktree_branch: "feat/x",
          }),
        ]}
        workspaces={WORKSPACES}
        openTaskCounts={{}}
      />,
    );
    expect(labelsOn(cardFor("isolated one")).branch).toBe("feat/x");
    expect(hasContextLine(cardFor("plain one"))).toBe(false);
  });

  it("labels an id-referenced workspace with its name across a mixed list", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a", name: "by id", workspace: "ws-mads", cwd: "." }),
          makeSession({ id: "b", name: "by name", workspace: "sidecar", cwd: "." }),
        ]}
        workspaces={WORKSPACES}
        openTaskCounts={{}}
      />,
    );
    expect(labelsOn(cardFor("by id"))).toEqual({ workspace: "mads" });
    expect(labelsOn(cardFor("by name"))).toEqual({ workspace: "sidecar" });
  });

  it("shows the cwd only when it differs from the workspace's primary repo", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a", name: "in the repo", workspace: "mads", cwd: "/home/drew/repo/mads" }),
          makeSession({ id: "b", name: "elsewhere", workspace: "mads", cwd: "/home/drew/repo/mads-ui" }),
        ]}
        workspaces={WORKSPACES}
        openTaskCounts={{}}
      />,
    );
    expect(hasContextLine(cardFor("in the repo"))).toBe(false);
    expect(labelsOn(cardFor("elsewhere")).cwd).toBe("~/repo/mads-ui");
  });

  it("shows the agent only when it is not the workspace's default persona", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a", name: "default persona", workspace: "mads", agent: "builder" }),
          makeSession({ id: "b", name: "other persona", workspace: "mads", agent: "reviewer" }),
        ]}
        workspaces={WORKSPACES}
        openTaskCounts={{}}
      />,
    );
    expect(hasContextLine(cardFor("default persona"))).toBe(false);
    expect(labelsOn(cardFor("other persona")).agent).toBe("reviewer");
  });

  // What the live rail actually showed: the launchpad stages the persona *id*,
  // so cards read "agt_3212544bb2e37c92". Resolving it also fixes the
  // comparison against the workspace default, which is stored as a name — an
  // id never matched one, so a session running the default claimed otherwise.
  it("names the persona an agt_ reference points at, and drops one it cannot resolve", () => {
    const agents: Agent[] = [
      { id: "agt_3212544bb2e37c92", name: "mads-coding-partner", prompt: "", created_at: "", updated_at: "" },
      { id: "agt_builder", name: "builder", prompt: "", created_at: "", updated_at: "" },
    ];
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a", name: "partner one", workspace: "mads", agent: "agt_3212544bb2e37c92" }),
          makeSession({ id: "b", name: "default by id", workspace: "mads", agent: "agt_builder" }),
          makeSession({ id: "c", name: "gone one", workspace: "mads", agent: "agt_deleted" }),
        ]}
        workspaces={WORKSPACES}
        agents={agents}
        openTaskCounts={{}}
      />,
    );
    expect(labelsOn(cardFor("partner one")).agent).toBe("mads-coding-partner");
    expect(hasContextLine(cardFor("default by id"))).toBe(false);
    // A hash nobody can read is worse than no label.
    expect(hasContextLine(cardFor("gone one"))).toBe(false);
  });

  it("shows the workspace only while the visible list spans more than one, and filtering it down to one does not change the card", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({
            id: "needs",
            name: "blocked one",
            workspace: "mads",
            activity: "inactive",
            cwd: "/home/drew/repo/mads",
          }),
          makeSession({ id: "idle", name: "calm one", workspace: "sidecar" }),
        ]}
        workspaces={WORKSPACES}
        openTaskCounts={{ needs: { running: 0, ready: 0, blocked: 1 } }}
      />,
    );

    // Two workspaces on screen: the name earns its place on every card.
    expect(labelsOn(cardFor("blocked one")).workspace).toBe("mads");
    expect(labelsOn(cardFor("calm one")).workspace).toBe("sidecar");
    const before = cards().map((card) => card.querySelectorAll('[class*="context"]').length);

    // "Needs you" leaves one workspace visible, so the name stops saying
    // anything and drops off — but the line it lived on stays reserved, which
    // is what keeps the surviving card's height identical.
    fireEvent.click(screen.getByRole("button", { name: /Needs you/ }));
    expect(cards()).toHaveLength(1);
    expect(labelsOn(cardFor("blocked one"))).toEqual({});
    const after = cards().map((card) => card.querySelectorAll('[class*="context"]').length);
    expect(before.every((n) => n === 1)).toBe(true);
    expect(after).toEqual([1]);
  });

  it("reserves the context line's height in CSS, so gaining or losing a label cannot reflow the rail", () => {
    const css = readFileSync("src/features/sessions/rail/SessionRail.module.css", "utf8");
    const rest = css.match(/\.context \{([^}]*)\}/)![1];
    expect(rest).toMatch(/min-height:\s*\d/);
    expect(rest).toContain("white-space: nowrap");
    expect(rest).toContain("overflow: hidden");
  });

  it("marks a retro session with a glyph rather than a word", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "r", name: "retro one", retro_id: "retro-1" })]}
        openTaskCounts={{}}
      />,
    );
    expect(labelsOn(cardFor("retro one"))).toEqual({ retro: "↺" });
  });

  it("puts the rest of the record in the card's tooltip", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({
            id: "a",
            name: "tooltipped",
            workspace: "mads",
            cwd: "/home/drew/repo/mads",
            model: "claude-opus-5",
            provider: "claude-code",
            trust: "limited",
            isolation: "current_dir",
            last_usage: { input_tokens: 10, output_tokens: 1, context_tokens: 100_000 },
          }),
        ]}
        workspaces={WORKSPACES}
        openTaskCounts={{}}
      />,
    );
    const title = cardFor("tooltipped").getAttribute("title")!;
    expect(title).toContain("Model: claude-opus-5");
    expect(title).toContain("Provider: claude-code");
    expect(title).toContain("Trust: limited");
    expect(title).toContain("Isolation: current_dir");
    expect(title).toContain("Context: 50% of 200k");
    // Including the labels the resting card adaptively dropped.
    expect(title).toContain("Workspace: mads");
    expect(title).toContain("Directory: ~/repo/mads");
  });
});
