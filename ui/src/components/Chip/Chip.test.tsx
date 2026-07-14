import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Chip } from "./Chip";

describe("Chip", () => {
  it("renders its label", () => {
    render(<Chip>Neutral</Chip>);
    expect(screen.getByText("Neutral")).toBeTruthy();
  });

  it("defaults to the neutral tone", () => {
    render(<Chip>Neutral</Chip>);
    expect(screen.getByText("Neutral").className).toMatch(/neutral/);
  });

  it.each(["ok", "warn", "bad", "info", "draft"] as const)(
    "applies the %s tone class",
    (tone) => {
      render(<Chip tone={tone}>{tone}</Chip>);
      expect(screen.getByText(tone).className).toMatch(new RegExp(tone));
    },
  );
});
