import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ProfilesSection } from "./ProfilesSection";
import { fixtureConfig, fixtureProfile } from "../../../router/fixtures";
import type { UseConfig } from "../state/useConfig";
import type { UseProfiles } from "../state/useProfiles";
import type { Profile } from "../../../router/types";

function makeConfig(overrides: Partial<UseConfig> = {}): UseConfig {
  return {
    initial: fixtureConfig,
    saved: fixtureConfig,
    draft: fixtureConfig,
    loading: false,
    fieldErrors: {},
    dirtyRestartFields: [],
    setDraft: vi.fn(),
    save: vi.fn().mockResolvedValue(true),
    refetch: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

function makeProfiles(overrides: Partial<UseProfiles> = {}): UseProfiles {
  return {
    profiles: [fixtureProfile],
    loading: false,
    create: vi.fn(),
    update: vi.fn(),
    remove: vi.fn(),
    activate: vi.fn().mockResolvedValue(fixtureConfig),
    ...overrides,
  };
}

describe("ProfilesSection", () => {
  it("shows the ACTIVE badge only on the profile matching config.saved.active_profile_id", () => {
    const secondProfile: Profile = { ...fixtureProfile, id: "prf_other", name: "other-profile" };
    render(
      <ProfilesSection
        config={makeConfig({ saved: { ...fixtureConfig, active_profile_id: "prf_focus" } })}
        profiles={makeProfiles({ profiles: [fixtureProfile, secondProfile] })}
        credentialNames={[]}
        onSwitched={vi.fn()}
      />,
    );

    const focusCard = screen.getByText("focus-mode").closest("div")!;
    expect(within(focusCard).queryByText("Active")).toBeTruthy();
    const otherCard = screen.getByText("other-profile").closest("div")!;
    expect(within(otherCard).queryByText("Active")).toBeNull();
  });

  it("renders no edit/delete controls, only a switch action for inactive profiles", () => {
    render(
      <ProfilesSection
        config={makeConfig({ saved: { ...fixtureConfig, active_profile_id: undefined } })}
        profiles={makeProfiles()}
        credentialNames={[]}
        onSwitched={vi.fn()}
      />,
    );

    expect(screen.queryByRole("button", { name: /^edit$/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /^delete$/i })).toBeNull();
    expect(screen.getByRole("button", { name: /switch to this profile/i })).toBeTruthy();
  });

  it("clicking Switch opens the ProfileSwitchDialog", () => {
    render(
      <ProfilesSection
        config={makeConfig({ saved: { ...fixtureConfig, active_profile_id: undefined } })}
        profiles={makeProfiles()}
        credentialNames={[]}
        onSwitched={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /switch to this profile/i }));
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("applying the switch calls onSwitched with the pre-switch config and closes the dialog", async () => {
    const onSwitched = vi.fn();
    const previous = { ...fixtureConfig, active_profile_id: undefined };
    const activate = vi.fn().mockResolvedValue({ ...fixtureConfig, active_profile_id: "prf_focus" });
    render(
      <ProfilesSection
        config={makeConfig({ saved: previous, refetch: vi.fn().mockResolvedValue(undefined) })}
        profiles={makeProfiles({ activate })}
        credentialNames={[]}
        onSwitched={onSwitched}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /switch to this profile/i }));
    fireEvent.click(screen.getByRole("button", { name: /switch profile/i }));

    // fixtureProfile's snapshot changes concurrency_limit, a restart-required field.
    await waitFor(() => expect(activate).toHaveBeenCalledWith("prf_focus"));
    await waitFor(() => expect(onSwitched).toHaveBeenCalledWith(previous, true));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("cancelling the dialog closes it without switching", () => {
    const activate = vi.fn();
    render(
      <ProfilesSection
        config={makeConfig({ saved: { ...fixtureConfig, active_profile_id: undefined } })}
        profiles={makeProfiles({ activate })}
        credentialNames={[]}
        onSwitched={vi.fn()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /switch to this profile/i }));
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }));

    expect(screen.queryByRole("dialog")).toBeNull();
    expect(activate).not.toHaveBeenCalled();
  });
});
