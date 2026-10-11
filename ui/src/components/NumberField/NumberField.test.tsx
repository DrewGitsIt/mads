import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { NumberField, type NumberFieldProps } from "./NumberField";

/** Holds the value like a real parent, reporting each change to onChange. */
function Controlled({ initial, onChange, ...rest }: Omit<NumberFieldProps, "value"> & { initial: number }) {
  const [value, setValue] = useState(initial);
  return (
    <NumberField
      {...rest}
      value={value}
      onChange={(n) => {
        setValue(n);
        onChange(n);
      }}
    />
  );
}

function input(): HTMLInputElement {
  return screen.getByRole("spinbutton") as HTMLInputElement;
}

describe("NumberField", () => {
  it("keeps an external label association", () => {
    render(
      <>
        <label htmlFor="n">Limit</label>
        <NumberField id="n" value={3} onChange={vi.fn()} />
      </>,
    );
    expect((screen.getByLabelText("Limit") as HTMLInputElement).value).toBe("3");
  });

  it("renders prefix and unit around the input", () => {
    render(<NumberField value={2} onChange={vi.fn()} prefix="$" unit="ms" />);
    expect(screen.getByText("$")).toBeTruthy();
    expect(screen.getByText("ms")).toBeTruthy();
  });

  it("reports each parseable keystroke live, unclamped", () => {
    const onChange = vi.fn();
    render(<NumberField value={3} onChange={onChange} min={1} max={10} />);
    fireEvent.change(input(), { target: { value: "50" } });
    expect(onChange).toHaveBeenLastCalledWith(50);
  });

  it("does not coerce an emptied field to 0 mid-edit, and reverts it on blur", () => {
    const onChange = vi.fn();
    render(<NumberField value={3} onChange={onChange} />);
    fireEvent.change(input(), { target: { value: "" } });
    expect(input().value).toBe("");
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.blur(input());
    expect(input().value).toBe("3");
    expect(onChange).not.toHaveBeenCalled();
  });

  it("rejects non-numeric keystrokes", () => {
    const onChange = vi.fn();
    render(<NumberField value={3} onChange={onChange} />);
    fireEvent.change(input(), { target: { value: "3a" } });
    expect(input().value).toBe("3");
    expect(onChange).not.toHaveBeenCalled();
  });

  it("clamps on blur", () => {
    const onChange = vi.fn();
    render(<NumberField value={3} onChange={onChange} live={false} min={1} max={10} />);
    fireEvent.change(input(), { target: { value: "50" } });
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.blur(input());
    expect(onChange).toHaveBeenCalledWith(10);
    expect(input().value).toBe("10");
  });

  it("commits on Enter when not live", () => {
    const onChange = vi.fn();
    render(<NumberField value={3} onChange={onChange} live={false} />);
    fireEvent.change(input(), { target: { value: "7" } });
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(onChange).toHaveBeenCalledWith(7);
  });

  it("steps with ArrowUp/ArrowDown by step", () => {
    const onChange = vi.fn();
    render(<Controlled initial={2000} onChange={onChange} step={250} />);
    fireEvent.keyDown(input(), { key: "ArrowUp" });
    expect(onChange).toHaveBeenLastCalledWith(2250);
    fireEvent.keyDown(input(), { key: "ArrowDown" });
    expect(onChange).toHaveBeenLastCalledWith(2000);
  });

  it("steppers clamp to min/max and disable at the bound", () => {
    const onChange = vi.fn();
    const { rerender } = render(<NumberField value={9} onChange={onChange} min={1} max={10} step={5} />);
    fireEvent.click(screen.getByRole("button", { name: "Increase" }));
    expect(onChange).toHaveBeenLastCalledWith(10);
    rerender(<NumberField value={10} onChange={onChange} min={1} max={10} step={5} />);
    expect((screen.getByRole("button", { name: "Increase" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.keyDown(input(), { key: "ArrowDown" });
    fireEvent.keyDown(input(), { key: "ArrowDown" });
    expect(onChange).toHaveBeenLastCalledWith(1);
  });

  it("keeps decimal steps free of float drift", () => {
    const onChange = vi.fn();
    render(<NumberField value={0.1} onChange={onChange} step={0.2} />);
    fireEvent.keyDown(input(), { key: "ArrowUp" });
    expect(onChange).toHaveBeenLastCalledWith(0.3);
  });

  it("keeps the steppers out of the tab order", () => {
    render(<NumberField value={1} onChange={vi.fn()} />);
    for (const b of screen.getAllByRole("button")) expect(b.tabIndex).toBe(-1);
  });

  it("resyncs the text when the value changes from outside", () => {
    const { rerender } = render(<NumberField value={3} onChange={vi.fn()} />);
    rerender(<NumberField value={8} onChange={vi.fn()} />);
    expect(input().value).toBe("8");
  });
});
