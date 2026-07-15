import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { UpdatesAboutPanel } from "./UpdatesAboutPanel";

describe("UpdatesAboutPanel", () => {
  it("renders version info and a disabled update-check button", () => {
    render(<UpdatesAboutPanel />);
    expect(screen.getByText(/version/i)).toBeTruthy();
    expect((screen.getByRole("button", { name: /check for updates/i }) as HTMLButtonElement).disabled).toBe(
      true,
    );
  });

  it("renders About copy", () => {
    render(<UpdatesAboutPanel />);
    expect(screen.getByText(/orchestrates multi-agent/i)).toBeTruthy();
  });
});
