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

  it("renders a failing check's fix_hint and still allows Continue", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 })),
    );
    const onNext = vi.fn();
    render(<PreflightStep onNext={onNext} onBack={vi.fn()} />);
    await waitFor(() => expect(screen.getByText("provider:goose")).toBeTruthy());
    expect(screen.getByText("mads has only been proven against < 1.49.0, detected 1.50.1")).toBeTruthy();
    fireEvent.click(screen.getByText("Continue"));
    expect(onNext).toHaveBeenCalledTimes(1);
  });

  it("shows mergiraf as an optional warning with install guidance, and still allows Continue", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(fixturePreflightChecks), { status: 200 })),
    );
    const onNext = vi.fn();
    render(<PreflightStep onNext={onNext} onBack={vi.fn()} />);
    await waitFor(() => expect(screen.getByText("brew install mergiraf")).toBeTruthy());
    expect(screen.getByText(/merge code edits that touch the same file automatically/)).toBeTruthy();
    fireEvent.click(screen.getByText("Continue"));
    expect(onNext).toHaveBeenCalledTimes(1);
  });
});
