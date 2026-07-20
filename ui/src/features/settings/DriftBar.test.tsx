import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { DriftBar } from "./DriftBar";
import type { UseDrift } from "./state/useDrift";
import { fixtureProfile } from "../../router/fixtures";

function makeDrift(overrides: Partial<UseDrift> = {}): UseDrift {
  return {
    activeProfile: fixtureProfile,
    isDrifted: true,
    fieldDiff: [],
    credentialDiff: [],
    revert: vi.fn().mockResolvedValue(true),
    updateProfile: vi.fn().mockResolvedValue(true),
    saveAsNewProfile: vi.fn().mockResolvedValue(true),
    ...overrides,
  };
}

describe("DriftBar", () => {
  it("renders nothing when there is no drift", () => {
    const { container } = render(<DriftBar drift={makeDrift({ isDrifted: false })} />);
    expect(container.innerHTML).toBe("");
  });

  it("shows the MODIFIED bar naming the active profile when drifted", () => {
    render(<DriftBar drift={makeDrift()} />);
    expect(screen.getByText("Modified")).toBeTruthy();
    expect(screen.getByText(/focus-mode/)).toBeTruthy();
  });

  it("Update profile calls updateProfile()", () => {
    const drift = makeDrift();
    render(<DriftBar drift={drift} />);
    fireEvent.click(screen.getByText("Update profile"));
    expect(drift.updateProfile).toHaveBeenCalled();
  });

  it("Revert calls revert()", () => {
    const drift = makeDrift();
    render(<DriftBar drift={drift} />);
    fireEvent.click(screen.getByText("Revert"));
    expect(drift.revert).toHaveBeenCalled();
  });

  it("Save as new profile reveals a name field, then calls saveAsNewProfile() with it", () => {
    const drift = makeDrift();
    render(<DriftBar drift={drift} />);
    fireEvent.click(screen.getByText("Save as new profile"));
    fireEvent.change(screen.getByLabelText("New profile name"), { target: { value: "weekend-mode" } });
    fireEvent.click(screen.getByText("Save"));
    expect(drift.saveAsNewProfile).toHaveBeenCalledWith("weekend-mode");
  });
});
