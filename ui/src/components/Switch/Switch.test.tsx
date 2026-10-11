import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Switch } from "./Switch";

describe("Switch", () => {
  it("exposes its state as role=switch aria-checked", () => {
    const { rerender } = render(<Switch aria-label="Sleep" checked={false} onChange={vi.fn()} />);
    expect(screen.getByRole("switch", { name: "Sleep" }).getAttribute("aria-checked")).toBe("false");
    rerender(<Switch aria-label="Sleep" checked onChange={vi.fn()} />);
    expect(screen.getByRole("switch", { name: "Sleep" }).getAttribute("aria-checked")).toBe("true");
  });

  it("reports the flipped state on click", () => {
    const onChange = vi.fn();
    render(<Switch aria-label="Sleep" checked onChange={onChange} />);
    fireEvent.click(screen.getByRole("switch"));
    expect(onChange).toHaveBeenCalledWith(false);
  });

  it("is named and flipped by an external label", () => {
    const onChange = vi.fn();
    render(
      <>
        <label htmlFor="s">Auto-approve</label>
        <Switch id="s" checked={false} onChange={onChange} />
      </>,
    );
    expect(screen.getByLabelText("Auto-approve").getAttribute("role")).toBe("switch");
    fireEvent.click(screen.getByText("Auto-approve"));
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it("does nothing when disabled", () => {
    const onChange = vi.fn();
    render(<Switch aria-label="Sleep" checked={false} onChange={onChange} disabled />);
    fireEvent.click(screen.getByRole("switch"));
    expect(onChange).not.toHaveBeenCalled();
  });
});
