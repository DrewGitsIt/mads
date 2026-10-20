import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
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

  describe("elapsed counter", () => {
    afterEach(() => vi.useRealTimers());

    // A turn that outlived a laptop sleep read "4069s", where only the last
    // digit moved — same scale as every other elapsed counter instead.
    it("reads an hour-long turn as hours and minutes, and keeps ticking", () => {
      vi.useFakeTimers();
      vi.setSystemTime(new Date("2026-10-14T18:07:49Z"));
      render(<WorkingIndicator since="2026-10-14T17:00:00Z" />);
      const status = screen.getByRole("status");
      expect(status.textContent).toContain("1h 07m");
      expect(status.textContent).not.toMatch(/\d{4}s/);

      act(() => vi.advanceTimersByTime(60_000));
      expect(status.textContent).toContain("1h 08m");
    });

    it("keeps seconds under a minute", () => {
      vi.useFakeTimers();
      vi.setSystemTime(new Date("2026-10-14T18:00:42Z"));
      render(<WorkingIndicator since="2026-10-14T18:00:00Z" />);
      expect(screen.getByRole("status").textContent).toContain("42s");
    });
  });
});
