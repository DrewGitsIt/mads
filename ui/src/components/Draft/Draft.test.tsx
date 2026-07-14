import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Draft, DraftChip } from "./Draft";

describe("DraftChip", () => {
  it("renders the Draft label with the draft chip tone", () => {
    render(<DraftChip />);
    expect(screen.getByText("Draft").className).toMatch(/draft/);
  });
});

describe("Draft", () => {
  it("wraps children with the draft provenance treatment", () => {
    render(<Draft>Proposed change</Draft>);
    expect(screen.getByText("Proposed change").className).toMatch(/draft/);
  });
});
