import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PreflightChecks } from "./PreflightChecks";
import { fixturePreflightChecks } from "../../router/fixtures";
import type { PreflightCheck } from "../../router/types";

describe("PreflightChecks", () => {
  it("hides providers that aren't installed but keeps installed-and-broken ones", () => {
    render(<PreflightChecks checks={fixturePreflightChecks} loading={false} onRerun={vi.fn()} />);
    expect(screen.getByText("provider:claude-code")).toBeTruthy();
    expect(screen.getByText("provider:goose")).toBeTruthy();
    expect(screen.queryByText("provider:kiro-cli")).toBeNull();
  });

  it("marks an unproven-version provider with a warning, not a failure", () => {
    render(<PreflightChecks checks={fixturePreflightChecks} loading={false} onRerun={vi.fn()} />);
    expect(screen.getByText("provider:goose").textContent).toContain("⚠");
    expect(screen.getByText("provider:goose").textContent).not.toContain("✗");
  });

  it("lists every probed provider, detected or not, behind the (i) button", () => {
    render(<PreflightChecks checks={fixturePreflightChecks} loading={false} onRerun={vi.fn()} />);
    expect(screen.queryByText("Providers checked")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Providers checked" }));
    expect(screen.getByText("claude-code")).toBeTruthy();
    expect(screen.getByText("claude-code v2.1.3")).toBeTruthy();
    expect(screen.getByText("kiro-cli")).toBeTruthy();
    expect(screen.getByText("not detected")).toBeTruthy();
    expect(screen.getByText("install Kiro CLI and ensure `kiro-cli` is on PATH")).toBeTruthy();
  });

  it("offers each provider's own installer, alphabetically, when none was detected", () => {
    const checks: PreflightCheck[] = [
      { name: "git", healthy: true },
      {
        name: "provider:kiro-cli",
        healthy: false,
        not_installed: true,
        install_url: "https://kiro.dev/docs/cli/",
        install_command: "curl -fsSL https://cli.kiro.dev/install | bash",
      },
      {
        name: "provider:claude-code",
        healthy: false,
        not_installed: true,
        install_url: "https://code.claude.com/docs/en/setup",
        install_command: "curl -fsSL https://claude.ai/install.sh | bash",
      },
    ];
    const onRerun = vi.fn();
    render(<PreflightChecks checks={checks} loading={false} onRerun={onRerun} />);

    const empty = screen.getByRole("region", { name: "No agent harness detected" });
    const items = within(empty).getAllByRole("listitem");
    expect(items.map((li) => li.textContent?.split("Install guide")[0])).toEqual(["claude-code", "kiro-cli"]);
    expect(within(items[0]).getByRole("link", { name: /Install guide/ }).getAttribute("href")).toBe(
      "https://code.claude.com/docs/en/setup",
    );
    expect(within(items[1]).getByText("curl -fsSL https://cli.kiro.dev/install | bash")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Check again" }));
    expect(onRerun).toHaveBeenCalledTimes(1);
  });

  it("shows no install options once any provider is detected", () => {
    render(<PreflightChecks checks={fixturePreflightChecks} loading={false} onRerun={vi.fn()} />);
    expect(screen.queryByRole("region", { name: "No agent harness detected" })).toBeNull();
  });

  it("omits the (i) button when no provider was probed", () => {
    render(<PreflightChecks checks={[{ name: "git", healthy: true }]} loading={false} onRerun={vi.fn()} />);
    expect(screen.queryByRole("button", { name: "Providers checked" })).toBeNull();
  });
});
