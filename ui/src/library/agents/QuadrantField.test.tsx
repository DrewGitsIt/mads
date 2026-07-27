import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QuadrantField } from "./QuadrantField";

describe("QuadrantField", () => {
  it("calls onChange with the selected quadrant", () => {
    const onChange = vi.fn();
    render(<QuadrantField onChange={onChange} />);
    fireEvent.click(screen.getByText("Investigator"));
    expect(onChange).toHaveBeenCalledWith("investigator");
  });

  it("shows quadrant descriptions inline", () => {
    render(<QuadrantField onChange={vi.fn()} />);
    expect(screen.getByText(/High clarity, high agency/)).toBeTruthy();
  });
});
