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

describe("SessionRail context labels", () => {
  it("shows location then branch for an isolated session, and just the location for one in place", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({
            id: "wt",
            name: "isolated one",
            workspace: "mads",
            dir: "/home/drew/repo/mads",
            worktree_branch: "mads/session/wt",
            worktree_base: "main",
            worktree_dir: "/home/drew/.mads/session-worktrees/wt",
            cwd: "/home/drew/.mads/session-worktrees/wt",
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
    expect(labelsOn(cardFor("isolated one"))).toEqual({ location: "mads", branch: "mads/session/wt" });
    const order = [...cardFor("isolated one").querySelectorAll("[data-label]")].map((el) =>
      el.getAttribute("data-label"),
    );
    expect(order).toEqual(["location", "branch"]);
    expect(labelsOn(cardFor("plain one"))).toEqual({ location: "mads" });
  });

  // The shape the running instance has: the router writes the workspace *id*
  // onto the session, every card is in one workspace, and an
  // isolation:current_dir session stores cwd ".". The card still says which
  // workspace — by name, not by id.
  it("names the workspace on every card of a single-workspace rail", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a", name: "current dir one", workspace: "ws-mads", cwd: "." }),
          makeSession({ id: "b", name: "current dir two", workspace: "ws-mads", cwd: "." }),
        ]}
        workspaces={WORKSPACES}
        openTaskCounts={{}}
      />,
    );
    for (const name of ["current dir one", "current dir two"]) {
      expect(labelsOn(cardFor(name))).toEqual({ location: "mads" });
    }
  });

  it("names a session with no workspace by its folder", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a", name: "in a folder", dir: "/home/drew/scratch/notes" }),
          makeSession({
            id: "b",
            name: "isolated in a folder",
            dir: "/home/drew/repo/sidecar",
            worktree_branch: "mads/session/b",
            worktree_dir: "/home/drew/.mads/session-worktrees/b",
            cwd: "/home/drew/.mads/session-worktrees/b",
          }),
        ]}
        openTaskCounts={{}}
      />,
    );
    expect(labelsOn(cardFor("in a folder"))).toEqual({ location: "notes" });
    expect(labelsOn(cardFor("isolated in a folder"))).toEqual({
      location: "sidecar",
      branch: "mads/session/b",
    });
  });

  it("spends no line on a card that names no place", () => {
    render(
      <SessionRail
        sessions={[makeSession({ id: "a", name: "nowhere", cwd: "." })]}
        openTaskCounts={{}}
      />,
    );
    expect(hasContextLine(cardFor("nowhere"))).toBe(false);
  });

  // The persona used to take a slot on this line, resolved through agentRef so
  // a card read "mads-coding-partner" rather than "agt_3212544bb2e37c92". It
  // no longer appears at all: the harness mark below took the glance it was
  // competing for, and the persona was a label that is suppressed whenever it
  // matches the workspace default — usually absent, and rarely what you are
  // scanning for when it is not. It is still in the hover tooltip.
  it("puts no persona on the line, whatever persona it is", () => {
    const agents: Agent[] = [
      { id: "agt_3212544bb2e37c92", name: "mads-coding-partner", prompt: "", created_at: "", updated_at: "" },
    ];
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a", name: "default persona", workspace: "mads", agent: "builder" }),
          makeSession({ id: "b", name: "other persona", workspace: "mads", agent: "reviewer" }),
          makeSession({ id: "c", name: "persona by id", workspace: "mads", agent: "agt_3212544bb2e37c92" }),
        ]}
        workspaces={WORKSPACES}
        agents={agents}
        openTaskCounts={{}}
      />,
    );
    for (const name of ["default persona", "other persona", "persona by id"]) {
      expect(labelsOn(cardFor(name))).toEqual({ location: "mads" });
    }
  });

  // What the rail could not answer before: you have claude-code and goose
  // sessions interleaved and nothing on any card said which was which. The
  // mark rides the title row, which already reserves its height for the
  // stamp, so it costs no vertical space and never adds a line.
  it("marks each card with the harness it runs on, adding nothing to the context line", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "a", name: "on claude", workspace: "mads", provider: "claude-code" }),
          makeSession({ id: "b", name: "on goose", workspace: "mads", provider: "goose" }),
          makeSession({ id: "c", name: "on something new", workspace: "mads", provider: "zed-acp" }),
          makeSession({ id: "d", name: "no provider yet", workspace: "mads" }),
        ]}
        workspaces={WORKSPACES}
        openTaskCounts={{}}
      />,
    );
    const markOn = (name: string) => cardFor(name).querySelector('[class*="harness"]');
    expect(markOn("on claude")?.getAttribute("aria-label")).toBe("claude-code");
    expect(markOn("on goose")?.getAttribute("aria-label")).toBe("goose");
    // An unknown harness is legible the day it lands: no art required.
    expect(markOn("on something new")?.textContent).toBe("Z");
    // A record that has not said yet claims nothing.
    expect(markOn("no provider yet")).toBeNull();
    for (const name of ["on claude", "on goose", "on something new"]) {
      expect(labelsOn(cardFor(name))).toEqual({ location: "mads" });
    }
  });

  it("keeps a card's labels as a filter narrows the list", () => {
    render(
      <SessionRail
        sessions={[
          makeSession({ id: "needs", name: "blocked one", workspace: "mads", activity: "inactive" }),
          makeSession({ id: "idle", name: "calm one", workspace: "sidecar" }),
        ]}
        workspaces={WORKSPACES}
        openTaskCounts={{ needs: { running: 0, ready: 0, blocked: 1 } }}
      />,
    );
    expect(labelsOn(cardFor("blocked one"))).toEqual({ location: "mads" });
    expect(labelsOn(cardFor("calm one"))).toEqual({ location: "sidecar" });

    fireEvent.click(screen.getByTitle("Needs you"));
    expect(cards()).toHaveLength(1);
    expect(labelsOn(cardFor("blocked one"))).toEqual({ location: "mads" });
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
    // And the full path behind the card's short location.
    expect(title).toContain("Workspace: mads");
    expect(title).toContain("Folder: ~/repo/mads");
  });
});
