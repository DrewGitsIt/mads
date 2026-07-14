import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Gallery } from "./Gallery";

const COMPONENT_NAMES = [
  "button",
  "chip",
  "card",
  "panel",
  "sidesheet",
  "toast",
  "resizablepanel",
  "iconrail",
  "draft",
];

describe("Gallery", () => {
  it("renders without throwing", () => {
    expect(() => render(<Gallery />)).not.toThrow();
  });

  it("renders both theme sections", () => {
    render(<Gallery />);
    expect(screen.getByTestId("theme-dark")).toBeTruthy();
    expect(screen.getByTestId("theme-light")).toBeTruthy();
  });

  it.each(COMPONENT_NAMES)("renders the %s group in both themes", (name) => {
    render(<Gallery />);
    const dark = screen.getByTestId("theme-dark");
    const light = screen.getByTestId("theme-light");
    expect(dark.querySelector(`[data-testid="group-${name}"]`)).toBeTruthy();
    expect(light.querySelector(`[data-testid="group-${name}"]`)).toBeTruthy();
  });
});
