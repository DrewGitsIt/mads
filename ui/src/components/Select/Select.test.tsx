import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Select } from "./Select";

const OPTIONS = [
  { value: "a", label: "Alpha" },
  { value: "b", label: "Bravo" },
];

describe("Select", () => {
  it("shows the placeholder when value matches no option", () => {
    render(<Select value="" options={OPTIONS} onChange={vi.fn()} placeholder="—" />);
    expect(screen.getByRole("button").textContent).toContain("—");
  });

  it("shows the selected option's label on the trigger", () => {
    render(<Select value="b" options={OPTIONS} onChange={vi.fn()} />);
    expect(screen.getByRole("button").textContent).toContain("Bravo");
  });

  it("does not render the listbox until opened", () => {
    render(<Select value="" options={OPTIONS} onChange={vi.fn()} />);
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("opens on click and lists every option", () => {
    render(<Select value="" options={OPTIONS} onChange={vi.fn()} />);
    fireEvent.click(screen.getByRole("button"));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["Alpha", "Bravo"]);
  });

  it("commits the clicked option and closes", () => {
    const onChange = vi.fn();
    render(<Select value="" options={OPTIONS} onChange={onChange} />);
    fireEvent.click(screen.getByRole("button"));
    fireEvent.click(screen.getByText("Bravo"));
    expect(onChange).toHaveBeenCalledWith("b");
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("opens with ArrowDown and selects the highlighted option with Enter", () => {
    const onChange = vi.fn();
    render(<Select value="a" options={OPTIONS} onChange={onChange} />);
    const trigger = screen.getByRole("button");
    fireEvent.keyDown(trigger, { key: "ArrowDown" });
    expect(screen.getByRole("listbox")).toBeTruthy();
    fireEvent.keyDown(trigger, { key: "ArrowDown" });
    fireEvent.keyDown(trigger, { key: "Enter" });
    expect(onChange).toHaveBeenCalledWith("b");
  });

  it("closes on Escape without committing a change", () => {
    const onChange = vi.fn();
    render(<Select value="a" options={OPTIONS} onChange={onChange} />);
    const trigger = screen.getByRole("button");
    fireEvent.click(trigger);
    fireEvent.keyDown(trigger, { key: "Escape" });
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(onChange).not.toHaveBeenCalled();
  });
});
