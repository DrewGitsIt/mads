import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { CommitList } from "./CommitList";
import { fixtureCommits } from "../../router/fixtures";

describe("CommitList", () => {
  it("renders an empty state when there are no commits", () => {
    render(<CommitList commits={[]} />);
    expect(screen.getByText(/no commits linked/i)).toBeTruthy();
  });

  it("renders the trailer-linked commit with a Trailer chip", () => {
    render(<CommitList commits={fixtureCommits["session-files-clean"]} />);
    expect(screen.getByText("a1b2c3d")).toBeTruthy();
    expect(screen.getByText(/add retry backoff/i)).toBeTruthy();
    expect(screen.getByText("Trailer").className).toMatch(/info/);
  });

  it("renders session/heuristic/orphan attribution across the observed session's commits", () => {
    render(<CommitList commits={fixtureCommits["session-files-observed"]} />);
    expect(screen.getByText("Session").className).toMatch(/info/);
    expect(screen.getByText("Heuristic").className).toMatch(/warn/);
    expect(screen.getByText("Orphan").className).toMatch(/warn/);
  });
});
