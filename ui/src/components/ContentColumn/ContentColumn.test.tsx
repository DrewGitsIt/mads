import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ContentColumn } from "./ContentColumn";

describe("ContentColumn", () => {
  it("renders its children", () => {
    render(<ContentColumn>content</ContentColumn>);
    expect(screen.getByText("content")).toBeTruthy();
  });

  it("applies the narrow variant class", () => {
    render(<ContentColumn variant="narrow">content</ContentColumn>);
    expect(screen.getByText("content").className).toMatch(/narrow/);
  });
});
