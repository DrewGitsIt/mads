import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Toast } from "./Toast";

describe("Toast", () => {
  it("renders the title", () => {
    render(<Toast title="Run finished" />);
    expect(screen.getByRole("status").textContent).toContain("Run finished");
  });

  it("renders an optional description", () => {
    render(<Toast title="Run finished" description="Completed in 2m 14s" />);
    expect(screen.getByText("Completed in 2m 14s")).toBeTruthy();
  });

  it("omits the description node when not provided", () => {
    render(<Toast title="Run finished" />);
    expect(screen.queryByText("Completed", { exact: false })).toBeNull();
  });

  it("applies the tone class", () => {
    render(<Toast tone="bad" title="Run failed" />);
    expect(screen.getByRole("status").className).toMatch(/bad/);
  });
});
