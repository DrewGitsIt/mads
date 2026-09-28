import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { SessionRail } from "./SessionRail";
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

function labelsOn(card: HTMLElement): Record<string, string> {
  const line = card.querySelector('[class*="context"]')!;
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
    expect(labelsOn(cardFor("in the repo")).cwd).toBeUndefined();
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
    expect(labelsOn(cardFor("default persona")).agent).toBeUndefined();
    expect(labelsOn(cardFor("other persona")).agent).toBe("reviewer");
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
