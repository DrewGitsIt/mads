import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { PreflightChecks } from "./PreflightChecks";
import { fixturePreflightChecks } from "../../router/fixtures";
import type { PreflightCheck } from "../../router/types";

describe("PreflightChecks", () => {
  it("hides providers that aren't installed but keeps installed-and-broken ones", () => {
    render(<PreflightChecks checks={fixturePreflightChecks} loading={false} />);
    expect(screen.getByText("provider:claude-code")).toBeTruthy();
    expect(screen.getByText("provider:goose")).toBeTruthy();
    expect(screen.queryByText("provider:kiro-cli")).toBeNull();
  });

  it("lists every probed provider, detected or not, behind the (i) button", () => {
    render(<PreflightChecks checks={fixturePreflightChecks} loading={false} />);
    expect(screen.queryByText("Providers checked")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Providers checked" }));
    expect(screen.getByText("claude-code")).toBeTruthy();
    expect(screen.getByText("claude-code v2.1.3")).toBeTruthy();
    expect(screen.getByText("kiro-cli")).toBeTruthy();
    expect(screen.getByText("not detected")).toBeTruthy();
    expect(screen.getByText("install Kiro CLI and ensure `kiro-cli` is on PATH")).toBeTruthy();
  });

  it("flags when no provider at all was detected", () => {
    const checks: PreflightCheck[] = [
      { name: "git", healthy: true },
      { name: "provider:claude-code", healthy: false, not_installed: true },
    ];
    render(<PreflightChecks checks={checks} loading={false} />);
    expect(screen.getByText("no agent harness detected")).toBeTruthy();
  });

  it("omits the (i) button when no provider was probed", () => {
    render(<PreflightChecks checks={[{ name: "git", healthy: true }]} loading={false} />);
    expect(screen.queryByRole("button", { name: "Providers checked" })).toBeNull();
  });
});
