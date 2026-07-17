import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { CommitList } from "./CommitList";
import { fixtureCommits, fixtureTasks } from "../../router/fixtures";

const taskTitles = new Map(fixtureTasks.map((t) => [t.id, t.title]));

describe("CommitList", () => {
  it("renders an empty state when there are no commits", () => {
    render(<CommitList commits={[]} taskTitles={taskTitles} />);
    expect(screen.getByText(/no commits linked/i)).toBeTruthy();
  });

  it("renders the trailer-linked commit with a Trailer chip", () => {
    render(<CommitList commits={fixtureCommits["session-files-clean"]} taskTitles={taskTitles} />);
    expect(screen.getByText("a1b2c3d")).toBeTruthy();
    expect(screen.getByText(/add retry backoff/i)).toBeTruthy();
    expect(screen.getByText("Trailer").className).toMatch(/info/);
  });

  it("renders session/heuristic/orphan attribution across the observed session's commits", () => {
    render(<CommitList commits={fixtureCommits["session-files-observed"]} taskTitles={taskTitles} />);
    expect(screen.getByText("Session").className).toMatch(/info/);
    expect(screen.getByText("Heuristic").className).toMatch(/warn/);
    expect(screen.getByText("Orphan").className).toMatch(/warn/);
  });

  it("renders a task-title chip for a commit with a resolvable task_id, and none for the orphan commit", () => {
    render(<CommitList commits={fixtureCommits["session-files-observed"]} taskTitles={taskTitles} />);
    expect(screen.getAllByText("Investigate flaky metrics filtering").length).toBeGreaterThan(0);
    expect(screen.getByText(/chore: bump go\.sum/).closest("li")?.textContent).not.toMatch(
      /Investigate flaky metrics filtering/,
    );
  });

  it("renders the commit's file list when present", () => {
    render(<CommitList commits={fixtureCommits["session-files-clean"]} taskTitles={taskTitles} />);
    expect(screen.getByText("services/data/internal/backoff.go")).toBeTruthy();
    expect(screen.getByText("services/data/internal/backoff_test.go")).toBeTruthy();
    expect(screen.getByText("docs/RETRY-BACKOFF.md")).toBeTruthy();
  });
});
