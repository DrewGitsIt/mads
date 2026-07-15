import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Card } from "./Card";

describe("Card", () => {
  it("renders its children inside the card container", () => {
    render(<Card>Card body</Card>);
    const el = screen.getByText("Card body");
    expect(el.className).toMatch(/card/);
  });

  it("applies the hoverable class and tabIndex when hoverable", () => {
    render(<Card hoverable>Hoverable body</Card>);
    const el = screen.getByText("Hoverable body");
    expect(el.className).toMatch(/hoverable/);
    expect(el.getAttribute("tabindex")).toBe("0");
  });

  it("applies the selected class", () => {
    render(<Card selected>Selected body</Card>);
    expect(screen.getByText("Selected body").className).toMatch(/selected/);
  });
});
