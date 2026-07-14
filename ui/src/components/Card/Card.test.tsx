import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Card } from "./Card";

describe("Card", () => {
  it("renders its children inside the card container", () => {
    render(<Card>Card body</Card>);
    const el = screen.getByText("Card body");
    expect(el.className).toMatch(/card/);
  });
});
