import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PreflightChecks } from "./PreflightChecks";
import { fixturePreflightChecks } from "../../router/fixtures";
import type { PreflightCheck } from "../../router/types";

describe("PreflightChecks", () => {
  it("hides providers that aren't installed but keeps installed-and-broken ones", () => {
    render(<PreflightChecks checks={fixturePreflightChecks} loading={false} onRerun={vi.fn()} />);
    expect(screen.getByText("Claude Code")).toBeTruthy();
    expect(screen.getByText("goose")).toBeTruthy();
    expect(screen.queryByText("kiro-cli")).toBeNull();
  });

  it("marks an unproven-version provider with a warning, not a failure", () => {
    render(<PreflightChecks checks={fixturePreflightChecks} loading={false} onRerun={vi.fn()} />);
    expect(screen.getByText("goose").textContent).toContain("⚠");
    expect(screen.getByText("goose").textContent).not.toContain("✗");
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

  it("names checks for people and keeps the raw id on the row", () => {
    const checks: PreflightCheck[] = [
      { name: "git", healthy: true },
      { name: "disk_space", healthy: true },
      { name: "data_service", healthy: true },
      { name: "provider:claude-code", healthy: true },
      { name: "provider:goose", healthy: true },
      { name: "mystery_check", healthy: true },
    ];
    render(<PreflightChecks checks={checks} loading={false} onRerun={vi.fn()} />);
    const rows = Array.from(document.querySelectorAll<HTMLElement>("li[data-preflight-check]"));
    expect(rows.map((r) => [r.dataset.preflightCheck, r.textContent])).toEqual([
      ["git", "✓Git"],
      ["disk_space", "✓Disk space"],
      ["data_service", "✓Data service"],
      ["provider:claude-code", "✓Claude Code"],
      ["provider:goose", "✓goose"],
      ["mystery_check", "✓mystery_check"],
    ]);
  });

  it("omits the (i) button when no provider was probed", () => {
    render(<PreflightChecks checks={[{ name: "git", healthy: true }]} loading={false} onRerun={vi.fn()} />);
    expect(screen.queryByRole("button", { name: "Providers checked" })).toBeNull();
  });

  describe("mergiraf (an optional tool)", () => {
    const absent = fixturePreflightChecks.find((c) => c.name === "mergiraf")!;
    const found: PreflightCheck = {
      name: "mergiraf",
      healthy: true,
      optional: true,
      purpose: "Lets Land and pipelines merge code edits that touch the same file automatically.",
      version: "mergiraf 0.20.0",
      path: "/opt/homebrew/bin/mergiraf",
      detail: "mergiraf 0.20.0 at /opt/homebrew/bin/mergiraf",
    };

    function row() {
      return document.querySelector('[data-preflight-check="mergiraf"]') as HTMLElement;
    }

    it("when absent, warns (never fails), says what it's for, and how to install it", () => {
      render(<PreflightChecks checks={[{ name: "git", healthy: true }, absent]} loading={false} onRerun={vi.fn()} />);
      const r = within(row());
      expect(row().textContent).toContain("⚠");
      expect(row().textContent).not.toContain("✗");
      expect(r.getByText("optional")).toBeTruthy();
      expect(r.getByText(/merge code edits that touch the same file automatically/)).toBeTruthy();
      expect(r.getByText("brew install mergiraf")).toBeTruthy();
      expect(r.getByRole("button", { name: "Copy" })).toBeTruthy();
      expect(r.getByRole("link").getAttribute("href")).toBe("https://codeberg.org/mergiraf/mergiraf/releases");
      // Not a provider: the "no agent harness" panel and the provider disclosure ignore it.
      expect(screen.queryByRole("region", { name: "No agent harness detected" })).toBeNull();
    });

    it("when found, shows the version and the path", () => {
      render(<PreflightChecks checks={[found]} loading={false} onRerun={vi.fn()} />);
      const r = within(row());
      expect(row().textContent).toContain("✓");
      expect(r.getByText(/mergiraf 0.20.0/)).toBeTruthy();
      expect(r.getByText("/opt/homebrew/bin/mergiraf")).toBeTruthy();
      expect(r.queryByText("brew install mergiraf")).toBeNull();
    });
  });
});
