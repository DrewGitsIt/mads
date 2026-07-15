import { describe, expect, it } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ToolCard } from "./ToolCard";

describe("ToolCard", () => {
  const tool = {
    name: "Edit",
    path: "services/data/internal/ingest.go",
    input: { old_string: "foo", new_string: "bar" },
  };

  it("defaults collapsed — input not shown", () => {
    render(<ToolCard tool={tool} />);
    expect(screen.getByRole("button").getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(/old_string/)).toBeNull();
  });

  it("expands to show input JSON and path on click", () => {
    render(<ToolCard tool={tool} />);
    fireEvent.click(screen.getByRole("button"));
    expect(screen.getByRole("button").getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText(/old_string/)).toBeTruthy();
    expect(screen.getAllByText(tool.path).length).toBeGreaterThan(0);
  });

  it("collapses again on a second click", () => {
    render(<ToolCard tool={tool} />);
    const button = screen.getByRole("button");
    fireEvent.click(button);
    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(/old_string/)).toBeNull();
  });
});
