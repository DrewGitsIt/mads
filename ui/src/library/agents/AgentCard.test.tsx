import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { AgentCard } from "./AgentCard";
import type { Agent } from "../../router/types";

function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "a1",
    name: "code-reviewer",
    prompt: "You review code.",
    created_at: "t",
    updated_at: "t",
    ...overrides,
  };
}

describe("AgentCard", () => {
  it("renders name, description, quadrant, and a system chip when applicable", () => {
    render(
      <AgentCard
        agent={makeAgent({
          description: "Reviews diffs.",
          quadrant: "autonomous_executor",
          system: true,
        })}
        onClick={vi.fn()}
      />,
    );
    expect(screen.getByText("code-reviewer")).toBeTruthy();
    expect(screen.getByText("Reviews diffs.")).toBeTruthy();
    expect(screen.getByText("Autonomous executor")).toBeTruthy();
    expect(screen.getByText("System")).toBeTruthy();
  });

  it("calls onClick when the card is clicked", () => {
    const onClick = vi.fn();
    render(<AgentCard agent={makeAgent()} onClick={onClick} />);
    fireEvent.click(screen.getByText("code-reviewer"));
    expect(onClick).toHaveBeenCalled();
  });
});
