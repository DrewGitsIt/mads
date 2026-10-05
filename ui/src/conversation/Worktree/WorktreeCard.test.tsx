import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { LandRefusalReason } from "../../router/types";
import { WorktreeCard } from "./WorktreeCard";
import type { WorktreeCardModel } from "./worktreeCards";
import { refusalCopy } from "./worktreeCopy";

const committed = (over: Partial<Extract<WorktreeCardModel, { kind: "committed" }>> = {}): WorktreeCardModel => ({
  kind: "committed",
  id: "c",
  seq: 1,
  timestamp: "2026-10-05T14:02:00Z",
  hash: "a41c09e5d2",
  subject: "deploy: detect Ubuntu 24.04 runners by codename",
  files: 3,
  drafted: true,
  undone: false,
  tip: true,
  ...over,
});

describe("WorktreeCard", () => {
  it("shows a commit with its short hash and subject", () => {
    render(<WorktreeCard card={committed()} />);
    expect(screen.getByText("Committed 3 files")).toBeTruthy();
    expect(screen.getByText("a41c09e")).toBeTruthy();
    expect(screen.getByText(/detect Ubuntu 24.04/)).toBeTruthy();
  });

  it("offers Undo only on a one-click (drafted) commit that's still the tip", () => {
    const onUndo = vi.fn();
    const { rerender } = render(<WorktreeCard card={committed()} actions={{ onUndo }} />);
    expect(screen.getByRole("button", { name: "Undo" })).toBeTruthy();
    rerender(<WorktreeCard card={committed({ drafted: false })} actions={{ onUndo }} />);
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
    rerender(<WorktreeCard card={committed({ tip: false })} actions={{ onUndo }} />);
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
    rerender(<WorktreeCard card={committed({ undone: true })} actions={{ onUndo }} />);
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
    expect(screen.getByText(/Undone — its files are uncommitted again/)).toBeTruthy();
  });

  it("says why an Undo was refused", async () => {
    const onUndo = vi.fn().mockResolvedValue({ undone: false, hash: "a41c09e5d2", reason: "not_tip" });
    render(<WorktreeCard card={committed()} actions={{ onUndo }} />);
    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(onUndo).toHaveBeenCalledWith("a41c09e5d2");
    await waitFor(() => expect(screen.getByText(/newer commits sit on top/)).toBeTruthy());
  });

  it("shows a landed merge commit", () => {
    render(
      <WorktreeCard card={{ kind: "landed", id: "l", seq: 2, base: "main", mergeCommit: "7be2d31aa" }} />,
    );
    expect(screen.getByText("Landed into main")).toBeTruthy();
    expect(screen.getByText("7be2d31")).toBeTruthy();
    expect(screen.getByText(/keeps going on top of it/)).toBeTruthy();
  });

  it("names conflicted files and asks the agent with an editable message", () => {
    const onAskAgent = vi.fn();
    render(
      <WorktreeCard
        card={{ kind: "conflicted", id: "x", seq: 3, base: "main", files: ["scripts/deploy.sh", "ci.yml"], latest: true }}
        actions={{ onAskAgent }}
      />,
    );
    expect(screen.getByText("main moved on: 2 files conflict")).toBeTruthy();
    expect(screen.getByText(/your checkout is untouched/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Ask the agent to resolve" }));
    expect(onAskAgent).toHaveBeenCalledWith(expect.stringContaining("resolve the conflicts in scripts/deploy.sh, ci.yml"));
  });

  it.each<LandRefusalReason>([
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
  ])("renders a refusal card for %s", (reason) => {
    render(
      <WorktreeCard
        card={{ kind: "refused", id: "r", seq: 4, base: "main", reason, files: ["a.go"], latest: true }}
      />,
    );
    const copy = refusalCopy(reason, "main", ["a.go"]);
    expect(screen.getByText(copy.title)).toBeTruthy();
    expect(screen.getByText(copy.body)).toBeTruthy();
    expect(document.querySelector(`[data-reason="${reason}"]`)).toBeTruthy();
  });

  it("shows git's own words for a refused merge, and Land again on the latest card", () => {
    const onLandAgain = vi.fn();
    const card: WorktreeCardModel = {
      kind: "refused",
      id: "r",
      seq: 5,
      base: "main",
      reason: "primary_refused",
      detail: "error: pre-merge-commit hook declined",
      latest: true,
    };
    const { rerender } = render(<WorktreeCard card={card} actions={{ onLandAgain }} />);
    expect(screen.getByText("error: pre-merge-commit hook declined").tagName).toBe("PRE");
    fireEvent.click(screen.getByRole("button", { name: "Land again" }));
    expect(onLandAgain).toHaveBeenCalled();
    rerender(<WorktreeCard card={{ ...card, latest: false }} actions={{ onLandAgain }} />);
    expect(screen.queryByRole("button", { name: "Land again" })).toBeNull();
  });

  it("renders a folder repair as a quiet card", () => {
    render(<WorktreeCard card={{ kind: "ensured", id: "e", seq: 6, title: "Recreated this session's folder", body: "b" }} />);
    expect(screen.getByRole("note", { name: "Recreated this session's folder" })).toBeTruthy();
  });
});

describe("WorktreeCard — pipeline merges into the session", () => {
  it("quietly says which edits a pipeline saved, and for which task", () => {
    render(<WorktreeCard card={{ kind: "pipelineSaved", id: "p", seq: 4, task: "Add docs", files: ["notes.txt"] }} />);
    expect(screen.getByText("Pipeline saved your edits to notes.txt before merging “Add docs”.")).toBeTruthy();
    expect(document.querySelector('[data-worktree-card="pipeline-saved"]')?.className).toMatch(/quiet/);
  });

  it("warns which files changed under a working agent", () => {
    render(
      <WorktreeCard card={{ kind: "pipelineMerged", id: "m", seq: 5, task: "Add docs", files: ["notes.txt", "docs/a.md"] }} />,
    );
    expect(screen.getByText("A pipeline merged while the agent was working")).toBeTruthy();
    expect(
      screen.getByText("A pipeline merged “Add docs” into this folder while the agent was working — changed: notes.txt and docs/a.md."),
    ).toBeTruthy();
    expect(document.querySelector('[data-worktree-card="pipeline-merged"]')?.className).toMatch(/warn/);
  });
});
