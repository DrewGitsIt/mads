import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ChangeList } from "./ChangeList";
import { fixtureFileChanges } from "../../router/fixtures";

describe("ChangeList", () => {
  it("renders an empty state when there are no changes", () => {
    render(<ChangeList changes={[]} onSelect={vi.fn()} />);
    expect(screen.getByText(/no file changes/i)).toBeTruthy();
  });

  it("renders a row per change with its A/M/D badge, path, and attribution", () => {
    const changes = fixtureFileChanges["session-files-clean"];
    render(<ChangeList changes={changes} onSelect={vi.fn()} />);

    expect(screen.getByText("services/data/internal/ingest.go")).toBeTruthy();
    expect(screen.getByText("M")).toBeTruthy();
    expect(screen.getByText("A")).toBeTruthy();
    expect(screen.getByText("D")).toBeTruthy();
    expect(screen.getAllByText("autonomous-executor").length).toBe(3);
  });

  it("renders the draft-tone Unattributed chip for an observed, unattributed change", () => {
    const changes = fixtureFileChanges["session-files-observed"];
    render(<ChangeList changes={changes} onSelect={vi.fn()} />);

    expect(screen.getByText("ui/src/App.module.css")).toBeTruthy();
    expect(screen.getByText("Unattributed").className).toMatch(/draft/);
  });

  it("calls onSelect with the row's path on click", () => {
    const onSelect = vi.fn();
    const changes = fixtureFileChanges["session-files-clean"];
    render(<ChangeList changes={changes} onSelect={onSelect} />);

    fireEvent.click(screen.getByText("services/data/internal/ingest.go"));
    expect(onSelect).toHaveBeenCalledWith("services/data/internal/ingest.go");
  });

  it("renders absolute paths repo-relative to worktreeDir, keeping the full path in title", () => {
    const changes = [
      {
        id: 1,
        session_id: "s1",
        path: "/wt/services/session/internal/runner/runner.go",
        change: "modified" as const,
        origin: "tool_use" as const,
        attributed: true,
        actor: "ses_other",
        timestamp: "2026-08-29T00:00:01Z",
      },
    ];
    render(<ChangeList changes={changes} onSelect={vi.fn()} worktreeDir="/wt" />);

    const shown = screen.getByText("services/session/internal/runner/runner.go");
    expect(shown.closest("[title]")?.getAttribute("title")).toBe(
      "/wt/services/session/internal/runner/runner.go",
    );
    expect(screen.queryByText("/wt/services/session/internal/runner/runner.go")).toBeNull();
  });

  it("selects by the raw (absolute) path even when the display is relativized", () => {
    const onSelect = vi.fn();
    const changes = [
      {
        id: 1,
        session_id: "s1",
        path: "/wt/src/a.ts",
        change: "modified" as const,
        origin: "tool_use" as const,
        attributed: true,
        timestamp: "2026-08-29T00:00:01Z",
      },
    ];
    render(<ChangeList changes={changes} onSelect={onSelect} worktreeDir="/wt" />);

    fireEvent.click(screen.getByText("src/a.ts"));
    expect(onSelect).toHaveBeenCalledWith("/wt/src/a.ts");
  });

  it("hides the attribution chip for the viewed session's own actor (case-insensitive), keeps foreign ones", () => {
    const base = {
      session_id: "ses_58f8c18e9b4b0433",
      change: "modified" as const,
      origin: "tool_use" as const,
      attributed: true,
      timestamp: "2026-08-29T00:00:01Z",
    };
    const changes = [
      { ...base, id: 1, path: "src/own.ts", actor: "SES_58F8C18E9B4B0433" },
      { ...base, id: 2, path: "src/foreign.ts", actor: "ses_other" },
    ];
    render(
      <ChangeList changes={changes} onSelect={vi.fn()} sessionId="ses_58f8c18e9b4b0433" />,
    );

    expect(screen.queryByText("SES_58F8C18E9B4B0433")).toBeNull();
    expect(screen.getByText("ses_other")).toBeTruthy();
  });

  it("still shows the actor chip when no sessionId is provided", () => {
    const changes = fixtureFileChanges["session-files-clean"];
    render(<ChangeList changes={changes} onSelect={vi.fn()} />);
    expect(screen.getAllByText("autonomous-executor").length).toBe(3);
  });

  it("marks the selected row via data-selected", () => {
    const changes = fixtureFileChanges["session-files-clean"];
    render(
      <ChangeList
        changes={changes}
        selectedPath="services/data/internal/ingest.go"
        onSelect={vi.fn()}
      />,
    );
    const row = screen.getByText("services/data/internal/ingest.go").closest("button");
    expect(row?.getAttribute("data-selected")).toBe("true");
  });
});
