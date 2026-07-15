import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { GenerationProgress } from "./GenerationProgress";

describe("GenerationProgress", () => {
  it("highlights the active step and marks earlier ones done", () => {
    render(<GenerationProgress phase="gathering" onCancel={() => {}} />);
    expect(screen.getByText("Starting").className).toMatch(/done/);
    expect(screen.getByText("Gathering").className).toMatch(/active/);
    expect(screen.getByText("Generating").className).toMatch(/pending/);
  });

  it("calls onCancel when Cancel is clicked", () => {
    const onCancel = vi.fn();
    render(<GenerationProgress phase="starting" onCancel={onCancel} />);
    fireEvent.click(screen.getByText("Cancel"));
    expect(onCancel).toHaveBeenCalledOnce();
  });
});
