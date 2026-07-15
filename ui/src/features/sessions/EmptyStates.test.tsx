import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { NoSessionsEmptyState } from "./EmptyStates";

describe("NoSessionsEmptyState", () => {
  it("renders teaching copy", () => {
    render(<NoSessionsEmptyState />);
    expect(screen.getByText("No sessions yet")).toBeTruthy();
  });
});
