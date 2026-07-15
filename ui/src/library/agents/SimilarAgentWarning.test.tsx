import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { SimilarAgentWarning } from "./SimilarAgentWarning";

describe("SimilarAgentWarning", () => {
  it("renders the matched agent name and score", () => {
    render(<SimilarAgentWarning similar={{ agent: "code-reviewer", score: 0.87 }} />);
    expect(screen.getByText(/code-reviewer/)).toBeTruthy();
    expect(screen.getByText(/87% match/)).toBeTruthy();
  });

  it("calls onViewExisting when the link is clicked", () => {
    const onViewExisting = vi.fn();
    render(
      <SimilarAgentWarning
        similar={{ agent: "code-reviewer", score: 0.87 }}
        onViewExisting={onViewExisting}
      />,
    );
    fireEvent.click(screen.getByText("View existing agent"));
    expect(onViewExisting).toHaveBeenCalled();
  });
});
