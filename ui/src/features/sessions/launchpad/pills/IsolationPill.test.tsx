import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { IsolationPill } from "./IsolationPill";
import type { WorkspaceRepo } from "../../../../router/types";

describe("IsolationPill", () => {
  it("reports the selected isolation mode", () => {
    const onChange = vi.fn();
    render(<IsolationPill onChange={onChange} />);
    fireEvent.click(screen.getByText("Select isolation"));
    fireEvent.click(screen.getByText("New worktree"));
    expect(onChange).toHaveBeenCalledWith("worktree");
  });

  it("does not show a repo checklist when the multiRepo flag is off", () => {
    const repos: WorkspaceRepo[] = [
      { path: "mads-core", primary: true },
      { path: "mads-ui" },
    ];
    render(<IsolationPill value="worktree" onChange={() => {}} repos={repos} />);
    fireEvent.click(screen.getByText("New worktree"));
    expect(screen.queryByText("mads-ui")).toBeNull();
  });

  it("shows the resolved default isolation mode when unset", () => {
    render(
      <IsolationPill resolved={{ value: "worktree", source: "builtin" }} onChange={vi.fn()} />,
    );
    expect(screen.getByText(/New worktree \(default\)/)).toBeTruthy();
  });

  it("shows the plain placeholder when nothing resolves", () => {
    render(<IsolationPill onChange={vi.fn()} />);
    expect(screen.getByText("Select isolation")).toBeTruthy();
  });
});
