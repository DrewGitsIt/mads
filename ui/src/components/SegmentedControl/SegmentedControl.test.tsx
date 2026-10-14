import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { SegmentedControl } from "./SegmentedControl";

const OPTIONS = [
  { value: "dark", label: "Dark" },
  { value: "light", label: "Light" },
  { value: "system", label: "System" },
];

function radio(name: string): HTMLButtonElement {
  return screen.getByRole("radio", { name }) as HTMLButtonElement;
}

describe("SegmentedControl", () => {
  it("is a radiogroup named by its label", () => {
    render(
      <>
        <span id="lbl">Theme</span>
        <SegmentedControl aria-labelledby="lbl" value="dark" options={OPTIONS} onChange={vi.fn()} />
      </>,
    );
    expect(screen.getByRole("radiogroup", { name: "Theme" })).toBeTruthy();
    expect(screen.getAllByRole("radio")).toHaveLength(3);
  });

  it("marks only the chosen option checked and makes it the single tab stop", () => {
    render(<SegmentedControl value="light" options={OPTIONS} onChange={vi.fn()} />);
    expect(radio("Light").getAttribute("aria-checked")).toBe("true");
    expect(radio("Dark").getAttribute("aria-checked")).toBe("false");
    expect(screen.getAllByRole("radio").map((r) => r.tabIndex)).toEqual([-1, 0, -1]);
  });

  it("gives the first option the tab stop when nothing matches", () => {
    render(<SegmentedControl value="sepia" options={OPTIONS} onChange={vi.fn()} />);
    expect(screen.getAllByRole("radio").map((r) => r.tabIndex)).toEqual([0, -1, -1]);
    expect(screen.queryByRole("radio", { checked: true })).toBeNull();
  });

  it("calls onChange on click, but not for the current value", () => {
    const onChange = vi.fn();
    render(<SegmentedControl value="dark" options={OPTIONS} onChange={onChange} />);
    fireEvent.click(radio("Dark"));
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.click(radio("System"));
    expect(onChange).toHaveBeenCalledWith("system");
  });

  it("arrow keys move the choice and focus, wrapping at the ends", () => {
    const onChange = vi.fn();
    render(<SegmentedControl value="dark" options={OPTIONS} onChange={onChange} />);
    fireEvent.keyDown(radio("Dark"), { key: "ArrowRight" });
    expect(onChange).toHaveBeenLastCalledWith("light");
    expect(document.activeElement).toBe(radio("Light"));
    fireEvent.keyDown(radio("Dark"), { key: "ArrowLeft" });
    expect(onChange).toHaveBeenLastCalledWith("system");
    fireEvent.keyDown(radio("Dark"), { key: "End" });
    expect(onChange).toHaveBeenLastCalledWith("system");
  });

  it("disables every option", () => {
    render(<SegmentedControl value="dark" options={OPTIONS} onChange={vi.fn()} disabled />);
    for (const r of screen.getAllByRole("radio")) expect((r as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole("radiogroup").getAttribute("aria-disabled")).toBe("true");
  });

  it("a disabled option shows its title, can't be chosen, and arrow keys skip it", () => {
    const onChange = vi.fn();
    const opts = [
      { value: "a", label: "A" },
      { value: "b", label: "B", disabled: true, title: "Needs a plug-in" },
      { value: "c", label: "C" },
    ];
    render(<SegmentedControl value="a" options={opts} onChange={onChange} />);
    const b = radio("B") as HTMLButtonElement;
    expect(b.disabled).toBe(true);
    expect(b.title).toBe("Needs a plug-in");
    fireEvent.click(b);
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.keyDown(radio("A"), { key: "ArrowRight" });
    expect(onChange).toHaveBeenLastCalledWith("c");
  });
});
