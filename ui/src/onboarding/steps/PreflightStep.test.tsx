import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PreflightStep } from "./PreflightStep";
import { fixturePreflightChecks } from "../../router/fixtures";

describe("PreflightStep", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders checks and always allows Continue", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 })),
    );
    const onNext = vi.fn();
    render(<PreflightStep onNext={onNext} onBack={vi.fn()} />);
    await waitFor(() => expect(screen.getByText("git")).toBeTruthy());
    fireEvent.click(screen.getByText("Continue"));
    expect(onNext).toHaveBeenCalledTimes(1);
  });

  it("calls onBack", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 })),
    );
    const onBack = vi.fn();
    render(<PreflightStep onNext={vi.fn()} onBack={onBack} />);
    await waitFor(() => expect(screen.getByText("git")).toBeTruthy());
    fireEvent.click(screen.getByText("Back"));
    expect(onBack).toHaveBeenCalledTimes(1);
  });
});
