import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { WorkingIndicator } from "./WorkingIndicator";

describe("WorkingIndicator", () => {
  // The session record's generic label used to land in the ticker too,
  // reading "Working… Working…".
  it("doesn't echo the generic label as its own activity line", () => {
    render(<WorkingIndicator activity="Working…" />);
    expect(screen.getByRole("status", { name: "Working…" })).toBeTruthy();
    expect(screen.queryByTestId("working-ticker")).toBeNull();
  });

  it("reads Compacting… for a compaction turn, with no activity line", () => {
    render(<WorkingIndicator activity="Compacting…" />);
    const status = screen.getByRole("status", { name: "Compacting…" });
    expect(status.textContent).toContain("Compacting…");
    expect(status.textContent).not.toContain("Working…");
    expect(screen.queryByTestId("working-ticker")).toBeNull();
  });

  it("still ticks a real activity under the generic label", () => {
    render(<WorkingIndicator activity="Running bash · make check" />);
    expect(screen.getByRole("status", { name: "Working…" })).toBeTruthy();
    expect(screen.getByTestId("working-ticker").textContent).toBe("Running bash · make check");
  });
});
