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
