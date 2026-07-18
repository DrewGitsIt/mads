import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { CommitList } from "./CommitList";
import { fixtureCommits, fixtureTasks } from "../../router/fixtures";

const taskTitles = new Map(fixtureTasks.map((t) => [t.id, t.title]));

describe("CommitList", () => {
  it("renders an empty state when there are no commits", () => {
    render(<CommitList commits={[]} taskTitles={taskTitles} onSelectFile={vi.fn()} />);
    expect(screen.getByText(/no commits linked/i)).toBeTruthy();
  });

  it("renders the trailer-linked commit with a Trailer chip", () => {
    render(
      <CommitList
        commits={fixtureCommits["session-files-clean"]}
        taskTitles={taskTitles}
        onSelectFile={vi.fn()}
      />,
    );
    expect(screen.getByText("a1b2c3d")).toBeTruthy();
    expect(screen.getByText(/add retry backoff/i)).toBeTruthy();
    expect(screen.getByText("Trailer").className).toMatch(/info/);
  });

  it("renders session/heuristic/orphan attribution across the observed session's commits", () => {
    render(
      <CommitList
        commits={fixtureCommits["session-files-observed"]}
        taskTitles={taskTitles}
        onSelectFile={vi.fn()}
      />,
    );
    expect(screen.getByText("Session").className).toMatch(/info/);
    expect(screen.getByText("Heuristic").className).toMatch(/warn/);
    expect(screen.getByText("Orphan").className).toMatch(/warn/);
  });

  it("renders a task-title chip for a commit with a resolvable task_id, and none for the orphan commit", () => {
    render(
      <CommitList
        commits={fixtureCommits["session-files-observed"]}
        taskTitles={taskTitles}
        onSelectFile={vi.fn()}
      />,
    );
    expect(screen.getAllByText("Investigate flaky metrics filtering").length).toBeGreaterThan(0);
    expect(screen.getByText(/chore: bump go\.sum/).closest("li")?.textContent).not.toMatch(
      /Investigate flaky metrics filtering/,
    );
  });

  it("renders the commit's file list when present", () => {
    render(
      <CommitList
        commits={fixtureCommits["session-files-clean"]}
        taskTitles={taskTitles}
        onSelectFile={vi.fn()}
      />,
    );
    expect(screen.getByText("services/data/internal/backoff.go")).toBeTruthy();
    expect(screen.getByText("services/data/internal/backoff_test.go")).toBeTruthy();
    expect(screen.getByText("docs/RETRY-BACKOFF.md")).toBeTruthy();
  });

  it("clicking a file row calls onSelectFile with the commit hash and file path", () => {
    const onSelectFile = vi.fn();
    render(
      <CommitList
        commits={fixtureCommits["session-files-clean"]}
        taskTitles={taskTitles}
        onSelectFile={onSelectFile}
      />,
    );

    fireEvent.click(screen.getByText("services/data/internal/backoff.go"));

    expect(onSelectFile).toHaveBeenCalledWith("a1b2c3d", "services/data/internal/backoff.go");
  });

  it("marks the selected file row via data-selected/aria-current, and leaves others unselected", () => {
    render(
      <CommitList
        commits={fixtureCommits["session-files-clean"]}
        taskTitles={taskTitles}
        selectedFile={{ hash: "a1b2c3d", path: "services/data/internal/backoff.go" }}
        onSelectFile={vi.fn()}
      />,
    );

    const selectedRow = screen.getByText("services/data/internal/backoff.go").closest("button");
    expect(selectedRow?.getAttribute("data-selected")).toBe("true");
    expect(selectedRow?.getAttribute("aria-current")).toBe("true");

    const otherRow = screen.getByText("services/data/internal/backoff_test.go").closest("button");
    expect(otherRow?.getAttribute("data-selected")).toBe("false");
  });

  it("file rows are real buttons, keyboard-focusable without bespoke key handling", () => {
    render(
      <CommitList
        commits={fixtureCommits["session-files-clean"]}
        taskTitles={taskTitles}
        onSelectFile={vi.fn()}
      />,
    );

    const row = screen.getByRole("button", { name: /services\/data\/internal\/backoff\.go/ });
    expect(row.getAttribute("type")).toBe("button");
  });
});
